// Package app holds the use cases — the application's actual behaviour.
//
// It depends on the domain and on nothing else. Every outward capability it
// needs (persistence, hashing, events, mail, object storage) is declared here
// as an interface and supplied by main. That is the dependency rule: source
// dependencies point inwards, so the domain and use cases can be compiled and
// tested with no database, no HTTP server and no network.
package app

import (
	"context"
	"time"

	"github.com/blessnduta/ticketing-system/internal/domain/approval"
	"github.com/blessnduta/ticketing-system/internal/domain/asset"
	"github.com/blessnduta/ticketing-system/internal/domain/identity"
	"github.com/blessnduta/ticketing-system/internal/domain/shared"
	"github.com/blessnduta/ticketing-system/internal/domain/sla"
	"github.com/blessnduta/ticketing-system/internal/domain/tenant"
	"github.com/blessnduta/ticketing-system/internal/domain/ticket"
)

// ---------------------------------------------------------------------------
// Persistence ports
//
// Every read method takes an orgID as its first argument. This is not
// decoration: the repository implementations interpolate it into every WHERE
// clause, and a method that did not take it could not be written correctly.
// Making tenancy a required parameter turns "did we remember to scope this
// query?" from a review question into a compile error.
// ---------------------------------------------------------------------------

// TxManager runs a function inside a database transaction. Use cases that
// write more than one aggregate — create a ticket *and* its audit entry — go
// through this, so a partial write is impossible.
//
// The transaction is carried on the context rather than passed as a parameter,
// so repositories can join an ambient transaction without every port method
// growing a *sql.Tx argument it would otherwise ignore.
type TxManager interface {
	WithinTx(ctx context.Context, fn func(ctx context.Context) error) error
}

type OrganizationRepository interface {
	Create(ctx context.Context, org *tenant.Organization) error
	ByID(ctx context.Context, id shared.ID) (*tenant.Organization, error)
	BySlug(ctx context.Context, slug string) (*tenant.Organization, error)
	// NextTicketReference atomically increments the organisation's ticket
	// counter and returns the formatted reference, e.g. "ACME-1042".
	NextTicketReference(ctx context.Context, orgID shared.ID) (string, error)
}

type UserRepository interface {
	Create(ctx context.Context, user *identity.User) error
	Update(ctx context.Context, user *identity.User) error
	ByID(ctx context.Context, orgID, id shared.ID) (*identity.User, error)
	// ByEmail spans organisations because login happens before we know which
	// tenant the caller belongs to. It is the single deliberate exception to
	// org-scoping, and the reason email is globally unique.
	ByEmail(ctx context.Context, email string) (*identity.User, error)
	List(ctx context.Context, orgID shared.ID, filter UserFilter) ([]*identity.User, error)
	// CountInOrg supports the "first user becomes admin" bootstrap.
	CountInOrg(ctx context.Context, orgID shared.ID) (int, error)
}

type UserFilter struct {
	Role   *identity.Role
	TeamID *shared.ID
	Active *bool
	Search string
}

type TeamRepository interface {
	Create(ctx context.Context, team *identity.Team) error
	Update(ctx context.Context, team *identity.Team) error
	ByID(ctx context.Context, orgID, id shared.ID) (*identity.Team, error)
	List(ctx context.Context, orgID shared.ID) ([]*identity.Team, error)
}

type TicketRepository interface {
	Create(ctx context.Context, t *ticket.Ticket) error
	// Update writes the whole aggregate. It takes expectedUpdatedAt and fails
	// with a Conflict when the row moved underneath us — optimistic locking.
	// Two agents resolving the same ticket from stale tabs is a routine event
	// on a busy desk, and last-write-wins silently discards one of them.
	Update(ctx context.Context, t *ticket.Ticket, expectedUpdatedAt time.Time) error
	ByID(ctx context.Context, orgID, id shared.ID) (*ticket.Ticket, error)
	ByReference(ctx context.Context, orgID shared.ID, reference string) (*ticket.Ticket, error)
	Search(ctx context.Context, orgID shared.ID, query TicketQuery) (shared.Page[*ticket.Ticket], error)
	// DueForBreachCheck returns active tickets whose resolution deadline has
	// passed and that have not yet been alerted on. Bounded by limit so the
	// worker processes in batches rather than loading an unbounded set.
	DueForBreachCheck(ctx context.Context, before time.Time, limit int) ([]*ticket.Ticket, error)
	// Stats powers the reporting endpoints.
	Stats(ctx context.Context, orgID shared.ID, window ReportWindow) (TicketStats, error)
}

// TicketQuery is the filter set behind the ticket list — the endpoint that
// gets hit hardest, so its shape is designed rather than accreted.
type TicketQuery struct {
	// Scope is applied before any user-supplied filter and is derived from the
	// actor, never from the request. See ticketScopeFor.
	Scope VisibilityScope

	Statuses   []ticket.Status
	Priorities []ticket.Priority
	Kinds      []ticket.Kind
	TeamIDs    []shared.ID
	AssigneeID *shared.ID
	// Unassigned filters to the unowned queue. Distinct from AssigneeID being
	// nil, which means "don't filter on assignee at all".
	Unassigned  bool
	RequesterID *shared.ID
	AssetID     *shared.ID
	Tags        []string
	// Text is matched against the Postgres full-text index over subject,
	// description and reference.
	Text string
	// BreachedOnly and DueBefore drive the "what is about to go wrong" views.
	BreachedOnly bool
	DueBefore    *time.Time
	CreatedAfter *time.Time
	Sort         TicketSort
	Page         shared.Pagination
}

type TicketSort string

const (
	SortNewest     TicketSort = "newest"
	SortOldest     TicketSort = "oldest"
	SortPriority   TicketSort = "priority"
	SortDueSoonest TicketSort = "due_soonest"
	SortUpdated    TicketSort = "updated"
)

// VisibilityScope is the server-side authorisation filter, computed from the
// actor. It is a separate type from the rest of the query precisely so that no
// handler can construct a TicketQuery without one.
type VisibilityScope struct {
	OrgID shared.ID
	// All bypasses row filtering (managers and admins).
	All bool
	// RequesterID restricts to tickets the user raised.
	RequesterID *shared.ID
	// TeamID restricts to a team's queue.
	TeamID *shared.ID
	// IncludeUnrouted lets agents see tickets not yet assigned to any team,
	// so new tickets are not invisible until someone routes them.
	IncludeUnrouted bool
	// CanReadInternal controls whether internal notes are returned.
	CanReadInternal bool
}

type MessageRepository interface {
	Create(ctx context.Context, message *ticket.Message) error
	// ListForTicket filters internal notes out at the query level when
	// includeInternal is false. Filtering after fetching would mean the rows
	// still crossed a process boundary, which is one refactor away from a leak.
	ListForTicket(ctx context.Context, orgID, ticketID shared.ID, includeInternal bool) ([]*ticket.Message, error)
	// CountAgentRepliesBefore supports first-response detection.
	CountPublicAgentReplies(ctx context.Context, orgID, ticketID shared.ID) (int, error)
}

type AttachmentRepository interface {
	Create(ctx context.Context, attachment *ticket.Attachment) error
	ByID(ctx context.Context, orgID, id shared.ID) (*ticket.Attachment, error)
	ListForMessages(ctx context.Context, orgID shared.ID, messageIDs []shared.ID) ([]*ticket.Attachment, error)
}

type AuditRepository interface {
	Append(ctx context.Context, entry *ticket.AuditEntry) error
	ListForTicket(ctx context.Context, orgID, ticketID shared.ID, page shared.Pagination) (shared.Page[*ticket.AuditEntry], error)
}

type SLARepository interface {
	CreatePolicy(ctx context.Context, policy *sla.Policy) error
	UpdatePolicy(ctx context.Context, policy *sla.Policy) error
	ListPolicies(ctx context.Context, orgID shared.ID) ([]*sla.Policy, error)
	CreateCalendar(ctx context.Context, calendar *sla.Calendar) error
	CalendarByID(ctx context.Context, orgID, id shared.ID) (*sla.Calendar, error)
	ListCalendars(ctx context.Context, orgID shared.ID) ([]*sla.Calendar, error)
}

type AssetRepository interface {
	Create(ctx context.Context, a *asset.Asset) error
	Update(ctx context.Context, a *asset.Asset) error
	ByID(ctx context.Context, orgID, id shared.ID) (*asset.Asset, error)
	List(ctx context.Context, orgID shared.ID, filter AssetFilter) (shared.Page[*asset.Asset], error)
	LinkToTicket(ctx context.Context, orgID, ticketID, assetID shared.ID) error
	UnlinkFromTicket(ctx context.Context, orgID, ticketID, assetID shared.ID) error
	ListForTicket(ctx context.Context, orgID, ticketID shared.ID) ([]*asset.Asset, error)
	// IncidentCounts returns how many incidents each asset accumulated in the
	// window — the input to "this model keeps failing".
	IncidentCounts(ctx context.Context, orgID shared.ID, since time.Time, limit int) ([]AssetIncidentCount, error)
}

type AssetFilter struct {
	Kind        *asset.Kind
	Status      *asset.Status
	Criticality *asset.Criticality
	OwnerID     *shared.ID
	Search      string
	Page        shared.Pagination
}

type AssetIncidentCount struct {
	Asset *asset.Asset
	Count int
}

type ApprovalRepository interface {
	Create(ctx context.Context, a *approval.Approval) error
	Update(ctx context.Context, a *approval.Approval) error
	ByID(ctx context.Context, orgID, id shared.ID) (*approval.Approval, error)
	ListForTicket(ctx context.Context, orgID, ticketID shared.ID) ([]*approval.Approval, error)
	ListPendingForApprover(ctx context.Context, orgID, approverID shared.ID) ([]*approval.Approval, error)
}

// RefreshTokenRepository stores hashed refresh tokens.
type RefreshTokenRepository interface {
	Store(ctx context.Context, token StoredRefreshToken) error
	// Consume atomically marks a token used and returns it, so a replayed
	// token is detected rather than honoured.
	Consume(ctx context.Context, tokenHash string, now time.Time) (StoredRefreshToken, error)
	// RevokeFamily invalidates an entire rotation chain. Called when a
	// consumed token is presented a second time, which means the token leaked.
	RevokeFamily(ctx context.Context, familyID shared.ID, now time.Time) error
	RevokeAllForUser(ctx context.Context, userID shared.ID, now time.Time) error
	DeleteExpired(ctx context.Context, before time.Time) (int64, error)
}

type StoredRefreshToken struct {
	TokenHash string
	UserID    shared.ID
	OrgID     shared.ID
	// FamilyID ties every token in a rotation chain together.
	FamilyID  shared.ID
	ExpiresAt time.Time
	ConsumedAt *time.Time
	RevokedAt  *time.Time
	CreatedAt  time.Time
}

type SavedViewRepository interface {
	Create(ctx context.Context, view *SavedView) error
	Delete(ctx context.Context, orgID, ownerID, id shared.ID) error
	List(ctx context.Context, orgID, ownerID shared.ID) ([]*SavedView, error)
	ByID(ctx context.Context, orgID, id shared.ID) (*SavedView, error)
}

// SavedView is a named ticket filter. It lives in app rather than domain
// because it is a workflow convenience with no invariants of its own — it is
// literally a stored TicketQuery.
type SavedView struct {
	ID      shared.ID
	OrgID   shared.ID
	OwnerID shared.ID
	Name    string
	// Shared makes the view visible to the owner's whole team, which is how a
	// desk standardises on "the P1 queue" instead of ten private variants.
	Shared    bool
	Filter    TicketQuery
	CreatedAt time.Time
}

// ---------------------------------------------------------------------------
// Service ports — capabilities the use cases need from the outside world.
// ---------------------------------------------------------------------------

// PasswordHasher isolates the hashing algorithm. Swapping bcrypt for argon2id
// later touches one adapter and nothing else.
type PasswordHasher interface {
	Hash(password string) (string, error)
	// Verify must be constant-time with respect to the comparison, and must
	// report NeedsRehash when the stored hash used weaker parameters than the
	// current policy, so cost can be raised without a mass reset.
	Verify(hash, password string) (ok bool, needsRehash bool)
}

// TokenIssuer mints and validates access tokens.
type TokenIssuer interface {
	Issue(actor identity.Actor, now time.Time) (token string, expiresAt time.Time, err error)
	Parse(token string) (identity.Actor, error)
	// IssueRealtimeTicket mints a short-lived, single-purpose credential for
	// the WebSocket handshake. Browsers cannot set headers on a WS upgrade, so
	// the alternative is putting the access token in a query string where it
	// lands in every proxy and access log. A 30-second ticket that only grants
	// a socket subscription is the smaller exposure.
	IssueRealtimeTicket(actor identity.Actor, now time.Time) (string, error)
	ParseRealtimeTicket(ticket string) (identity.Actor, error)
}

// EventPublisher decouples "something happened" from "therefore notify".
// The ticket service publishes; the notifier and the WebSocket hub subscribe.
// Neither is a dependency of the other.
type EventPublisher interface {
	Publish(ctx context.Context, event Event)
}

// Event is the payload crossing that boundary.
type Event struct {
	Type     EventType
	OrgID    shared.ID
	TicketID shared.ID
	// Audience carries the routing facts the realtime hub needs to decide who
	// may receive this event, so the hub never has to query the database on
	// the fan-out path.
	Audience EventAudience
	ActorID  *shared.ID
	Payload  map[string]any
	At       time.Time
}

type EventType string

const (
	EventTicketCreated       EventType = "ticket.created"
	EventTicketStatusChanged EventType = "ticket.status_changed"
	EventTicketAssigned      EventType = "ticket.assigned"
	EventTicketRouted        EventType = "ticket.routed"
	EventTicketMessageAdded  EventType = "ticket.message_added"
	EventTicketSLABreached   EventType = "ticket.sla_breached"
	EventTicketSLAWarning    EventType = "ticket.sla_warning"
	EventApprovalRequested   EventType = "approval.requested"
	EventApprovalDecided     EventType = "approval.decided"
)

// EventAudience is the visibility envelope for a realtime event.
type EventAudience struct {
	RequesterID shared.ID
	AssigneeID  *shared.ID
	TeamID      *shared.ID
	// InternalOnly excludes requesters entirely, whatever their relationship
	// to the ticket. Set for internal notes and for SLA machinery, which a
	// requester should never see.
	InternalOnly bool
}

// Notifier delivers out-of-band messages. Implementations must be safe to call
// from a background goroutine and must never block the request path.
type Notifier interface {
	Notify(ctx context.Context, notification Notification) error
}

type Notification struct {
	To       string // email address
	Subject  string
	Body     string
	TicketID shared.ID
	OrgID    shared.ID
}

// ObjectStore backs attachments. Presigned URLs mean file bytes never travel
// through the API process, which keeps a 25 MiB upload from occupying a
// request worker for its duration.
type ObjectStore interface {
	PresignUpload(ctx context.Context, key, contentType string, maxBytes int64, ttl time.Duration) (url string, err error)
	PresignDownload(ctx context.Context, key, filename string, ttl time.Duration) (url string, err error)
	Delete(ctx context.Context, key string) error
}

// ---------------------------------------------------------------------------
// Reporting shapes
// ---------------------------------------------------------------------------

type ReportWindow struct {
	From time.Time
	To   time.Time
	// TeamID narrows the report to one queue.
	TeamID *shared.ID
}

// TicketStats is the reporting payload. Percentiles rather than means for
// resolution time: support-desk durations have a long tail, and a mean
// resolution time is dominated by the handful of tickets that sat for a month.
// p50 tells you the typical experience; p90 tells you the bad one.
type TicketStats struct {
	Created        int
	Resolved       int
	Reopened       int
	Open           int
	Breached       int
	FirstResponseMet int
	FirstResponseTotal int
	ResolutionMet    int
	ResolutionTotal  int

	MedianResolution time.Duration
	P90Resolution    time.Duration

	ByStatus   map[ticket.Status]int
	ByPriority map[ticket.Priority]int
	ByKind     map[ticket.Kind]int
	// Volume is a daily time series for the trend chart.
	Volume []DailyCount
	// AgentLoad shows open tickets per assignee — where the queue is stuck.
	AgentLoad []AgentLoad
}

type DailyCount struct {
	Day      time.Time
	Created  int
	Resolved int
}

type AgentLoad struct {
	UserID   shared.ID
	FullName string
	Open     int
	Breached int
}
