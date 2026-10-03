package http

import (
	"time"

	"github.com/blessnduta/ticketing-system/internal/app"
	"github.com/blessnduta/ticketing-system/internal/domain/approval"
	"github.com/blessnduta/ticketing-system/internal/domain/asset"
	"github.com/blessnduta/ticketing-system/internal/domain/identity"
	"github.com/blessnduta/ticketing-system/internal/domain/shared"
	"github.com/blessnduta/ticketing-system/internal/domain/ticket"
)

// Response DTOs are separate types from domain entities, and the mapping below
// is written by hand. Serialising domain structs directly is faster to write
// and wrong for two reasons: it leaks every field ever added to the domain
// (PasswordHash being the obvious one), and it welds the public API contract
// to internal refactors, so renaming a field breaks every client.

// ---------------------------------------------------------------------------
// Identity
// ---------------------------------------------------------------------------

type userResponse struct {
	ID       shared.ID     `json:"id"`
	Email    string        `json:"email"`
	FullName string        `json:"full_name"`
	Role     identity.Role `json:"role"`
	TeamID   *shared.ID    `json:"team_id,omitempty"`
	Active   bool          `json:"active"`
}

// toUser omits PasswordHash by construction, not by a json:"-" tag. A tag can
// be removed by accident; a field that does not exist in the DTO cannot be
// serialised at all.
func toUser(user *identity.User) *userResponse {
	if user == nil {
		return nil
	}
	return &userResponse{
		ID:       user.ID,
		Email:    user.Email,
		FullName: user.FullName,
		Role:     user.Role,
		TeamID:   user.TeamID,
		Active:   user.Active,
	}
}

func toUsers(users []*identity.User) []*userResponse {
	out := make([]*userResponse, 0, len(users))
	for _, user := range users {
		out = append(out, toUser(user))
	}
	return out
}

type teamResponse struct {
	ID          shared.ID  `json:"id"`
	Name        string     `json:"name"`
	Description string     `json:"description"`
	EscalatesTo *shared.ID `json:"escalates_to,omitempty"`
}

func toTeams(teams []*identity.Team) []*teamResponse {
	out := make([]*teamResponse, 0, len(teams))
	for _, team := range teams {
		out = append(out, &teamResponse{
			ID: team.ID, Name: team.Name,
			Description: team.Description, EscalatesTo: team.EscalatesTo,
		})
	}
	return out
}

type authResponse struct {
	AccessToken  string        `json:"access_token"`
	RefreshToken string        `json:"refresh_token"`
	ExpiresAt    time.Time     `json:"expires_at"`
	User         *userResponse `json:"user"`
}

// ---------------------------------------------------------------------------
// Tickets
// ---------------------------------------------------------------------------

// ticketSummary is the list-row shape. Deliberately smaller than the detail
// response: a queue of 100 tickets should not ship 100 full descriptions, and
// a list endpoint that returns the detail payload is the most common cause of
// a slow ticket dashboard.
type ticketSummary struct {
	ID        shared.ID    `json:"id"`
	Reference string       `json:"reference"`
	Kind      ticket.Kind  `json:"kind"`
	Subject   string       `json:"subject"`
	Status    ticket.Status `json:"status"`
	Priority  ticket.Priority `json:"priority"`
	Impact    ticket.Impact   `json:"impact"`
	Urgency   ticket.Urgency  `json:"urgency"`
	Category  string       `json:"category"`
	Tags      []string     `json:"tags"`

	RequesterID shared.ID  `json:"requester_id"`
	AssigneeID  *shared.ID `json:"assignee_id,omitempty"`
	TeamID      *shared.ID `json:"team_id,omitempty"`

	ResolutionDue *time.Time `json:"resolution_due,omitempty"`
	// ResolvedAt and ClosedAt are on the summary, not just the detail, because
	// a list of finished tickets has to say when each one finished — "updated
	// 3 days ago" is not the same fact and reads as a fudge.
	ResolvedAt *time.Time `json:"resolved_at,omitempty"`
	ClosedAt   *time.Time `json:"closed_at,omitempty"`
	// Breached is computed server-side rather than left to the client to work
	// out from resolution_due. Client-side clocks are wrong often enough that
	// two agents would otherwise disagree about whether a ticket is late.
	Breached    bool      `json:"breached"`
	ReopenCount int       `json:"reopen_count"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

func toTicketSummary(t *ticket.Ticket, now time.Time) ticketSummary {
	tags := t.Tags
	if tags == nil {
		// An explicit empty array, never null: a client doing `tags.map(...)`
		// on null is a crash, and every client eventually does that.
		tags = []string{}
	}
	return ticketSummary{
		ID: t.ID, Reference: t.Reference, Kind: t.Kind, Subject: t.Subject,
		Status: t.Status, Priority: t.Priority, Impact: t.Impact, Urgency: t.Urgency,
		Category: t.Category, Tags: tags,
		RequesterID: t.RequesterID, AssigneeID: t.AssigneeID, TeamID: t.TeamID,
		ResolutionDue: t.ResolutionDue, ResolvedAt: t.ResolvedAt, ClosedAt: t.ClosedAt,
		Breached: t.BreachedAt(now),
		ReopenCount: t.ReopenCount, CreatedAt: t.CreatedAt, UpdatedAt: t.UpdatedAt,
	}
}

type messageResponse struct {
	ID         shared.ID         `json:"id"`
	AuthorID   *shared.ID        `json:"author_id,omitempty"`
	Body       string            `json:"body"`
	Visibility ticket.Visibility `json:"visibility"`
	System     bool              `json:"system"`
	CreatedAt  time.Time         `json:"created_at"`
}

type assetResponse struct {
	ID          shared.ID         `json:"id"`
	Tag         string            `json:"tag"`
	Name        string            `json:"name"`
	Kind        asset.Kind        `json:"kind"`
	Status      asset.Status      `json:"status"`
	Criticality asset.Criticality `json:"criticality"`
	Model       string            `json:"model,omitempty"`
	Location    string            `json:"location,omitempty"`
	OwnerID     *shared.ID        `json:"owner_id,omitempty"`
}

func toAsset(a *asset.Asset) assetResponse {
	return assetResponse{
		ID: a.ID, Tag: a.Tag, Name: a.Name, Kind: a.Kind, Status: a.Status,
		Criticality: a.Criticality, Model: a.Model, Location: a.Location, OwnerID: a.OwnerID,
	}
}

type approvalResponse struct {
	ID          shared.ID         `json:"id"`
	ApproverID  shared.ID         `json:"approver_id"`
	RequestedBy shared.ID         `json:"requested_by"`
	Decision    approval.Decision `json:"decision"`
	Comment     string            `json:"comment,omitempty"`
	DecidedAt   *time.Time        `json:"decided_at,omitempty"`
	CreatedAt   time.Time         `json:"created_at"`
}

// ticketDetail is the full read model for one ticket.
type ticketDetail struct {
	ticketSummary
	Description string  `json:"description"`
	Resolution  *string `json:"resolution,omitempty"`

	Requester *userResponse `json:"requester,omitempty"`
	Assignee  *userResponse `json:"assignee,omitempty"`

	Messages  []messageResponse  `json:"messages"`
	Assets    []assetResponse    `json:"assets"`
	Approvals []approvalResponse `json:"approvals"`

	FirstResponseDue *time.Time `json:"first_response_due,omitempty"`
	FirstResponseAt  *time.Time `json:"first_response_at,omitempty"`
	PausedSince      *time.Time `json:"paused_since,omitempty"`

	// AllowedTransitions comes from the same state machine the server
	// enforces, so the UI renders exactly the buttons that will be accepted.
	// Without it every client re-implements the transition table and drifts.
	AllowedTransitions []ticket.Status `json:"allowed_transitions"`
	// CanReadInternal tells the client whether this thread is complete, so it
	// can say "internal notes hidden" rather than imply there are none.
	CanReadInternal bool `json:"can_read_internal"`
}

func toTicketDetail(view *app.TicketView, now time.Time) ticketDetail {
	detail := ticketDetail{
		ticketSummary:      toTicketSummary(view.Ticket, now),
		Description:        view.Ticket.Description,
		Resolution:         view.Ticket.Resolution,
		Requester:          toUser(view.Requester),
		Assignee:           toUser(view.Assignee),
		Messages:           make([]messageResponse, 0, len(view.Messages)),
		Assets:             make([]assetResponse, 0, len(view.Assets)),
		Approvals:          make([]approvalResponse, 0, len(view.Approvals)),
		FirstResponseDue:   view.Ticket.FirstResponseDue,
		FirstResponseAt:    view.Ticket.FirstRespAt,
		PausedSince:        view.Ticket.PausedSince,
		AllowedTransitions: view.AllowedTransitions,
		CanReadInternal:    view.CanReadInternal,
	}
	if detail.AllowedTransitions == nil {
		detail.AllowedTransitions = []ticket.Status{}
	}

	for _, message := range view.Messages {
		detail.Messages = append(detail.Messages, messageResponse{
			ID: message.ID, AuthorID: message.AuthorID, Body: message.Body,
			Visibility: message.Visibility, System: message.System, CreatedAt: message.CreatedAt,
		})
	}
	for _, linked := range view.Assets {
		detail.Assets = append(detail.Assets, toAsset(linked))
	}
	for _, record := range view.Approvals {
		detail.Approvals = append(detail.Approvals, approvalResponse{
			ID: record.ID, ApproverID: record.ApproverID, RequestedBy: record.RequestedBy,
			Decision: record.Decision, Comment: record.Comment,
			DecidedAt: record.DecidedAt, CreatedAt: record.CreatedAt,
		})
	}
	return detail
}

// pageResponse is the envelope for every paginated collection, so clients
// implement pagination once.
type pageResponse[T any] struct {
	Items      []T        `json:"items"`
	NextCursor *shared.ID `json:"next_cursor,omitempty"`
	HasMore    bool       `json:"has_more"`
}

func toPage[D any, S any](page shared.Page[S], convert func(S) D) pageResponse[D] {
	items := make([]D, 0, len(page.Items))
	for _, item := range page.Items {
		items = append(items, convert(item))
	}
	return pageResponse[D]{Items: items, NextCursor: page.NextCursor, HasMore: page.HasMore}
}

// ---------------------------------------------------------------------------
// Reporting
// ---------------------------------------------------------------------------

type statsResponse struct {
	Created  int `json:"created"`
	Resolved int `json:"resolved"`
	Reopened int `json:"reopened"`
	Open     int `json:"open"`
	Breached int `json:"breached"`

	FirstResponseAttainment float64 `json:"first_response_attainment_pct"`
	ResolutionAttainment    float64 `json:"resolution_attainment_pct"`

	// Durations are seconds, not Go's nanosecond integers. A JSON consumer
	// reading 14400000000000 has to know it is nanoseconds; 14400 is
	// unambiguous in every language.
	MedianResolutionSeconds float64 `json:"median_resolution_seconds"`
	P90ResolutionSeconds    float64 `json:"p90_resolution_seconds"`

	ByStatus   map[string]int `json:"by_status"`
	ByPriority map[string]int `json:"by_priority"`
	ByKind     map[string]int `json:"by_kind"`

	Volume    []volumePoint     `json:"volume"`
	AgentLoad []agentLoadEntry  `json:"agent_load"`
}

type volumePoint struct {
	Day      string `json:"day"`
	Created  int    `json:"created"`
	Resolved int    `json:"resolved"`
}

type agentLoadEntry struct {
	UserID   shared.ID `json:"user_id"`
	FullName string    `json:"full_name"`
	Open     int       `json:"open"`
	Breached int       `json:"breached"`
}

func toStats(stats app.TicketStats) statsResponse {
	firstResponsePct, resolutionPct := app.SLAAttainment(stats)

	response := statsResponse{
		Created: stats.Created, Resolved: stats.Resolved, Reopened: stats.Reopened,
		Open: stats.Open, Breached: stats.Breached,
		FirstResponseAttainment: firstResponsePct,
		ResolutionAttainment:    resolutionPct,
		MedianResolutionSeconds: stats.MedianResolution.Seconds(),
		P90ResolutionSeconds:    stats.P90Resolution.Seconds(),
		ByStatus:                map[string]int{},
		ByPriority:              map[string]int{},
		ByKind:                  map[string]int{},
		Volume:                  make([]volumePoint, 0, len(stats.Volume)),
		AgentLoad:               make([]agentLoadEntry, 0, len(stats.AgentLoad)),
	}

	// Every status/priority/kind is emitted even at zero, so a chart's
	// categories are stable between refreshes instead of appearing and
	// vanishing as the data changes.
	for _, status := range ticket.AllStatuses {
		response.ByStatus[string(status)] = stats.ByStatus[status]
	}
	for _, priority := range []ticket.Priority{ticket.PriorityP1, ticket.PriorityP2, ticket.PriorityP3, ticket.PriorityP4} {
		response.ByPriority[string(priority)] = stats.ByPriority[priority]
	}
	for _, kind := range []ticket.Kind{ticket.KindIncident, ticket.KindServiceRequest, ticket.KindChange, ticket.KindProblem} {
		response.ByKind[string(kind)] = stats.ByKind[kind]
	}

	for _, point := range stats.Volume {
		response.Volume = append(response.Volume, volumePoint{
			Day: point.Day.Format("2006-01-02"), Created: point.Created, Resolved: point.Resolved,
		})
	}
	for _, load := range stats.AgentLoad {
		response.AgentLoad = append(response.AgentLoad, agentLoadEntry{
			UserID: load.UserID, FullName: load.FullName, Open: load.Open, Breached: load.Breached,
		})
	}
	return response
}

// ---------------------------------------------------------------------------
// SLA
// ---------------------------------------------------------------------------

type slaPolicyResponse struct {
	ID                   shared.ID  `json:"id"`
	Name                 string     `json:"name"`
	Priority             *string    `json:"priority,omitempty"`
	Kind                 *string    `json:"kind,omitempty"`
	TeamID               *shared.ID `json:"team_id,omitempty"`
	CalendarID           shared.ID  `json:"calendar_id"`
	FirstResponseSeconds int64      `json:"first_response_seconds"`
	ResolutionSeconds    int64      `json:"resolution_seconds"`
	Active               bool       `json:"active"`
}

type slaCalendarResponse struct {
	ID       shared.ID `json:"id"`
	Name     string    `json:"name"`
	Timezone string    `json:"timezone"`
	Windows  []window  `json:"windows"`
}

type window struct {
	Weekday   int `json:"weekday"`
	StartMins int `json:"start_mins"`
	EndMins   int `json:"end_mins"`
}

func toSLASnapshot(snapshot *app.PolicySnapshot) map[string]any {
	policies := make([]slaPolicyResponse, 0, len(snapshot.Policies))
	for _, policy := range snapshot.Policies {
		response := slaPolicyResponse{
			ID: policy.ID, Name: policy.Name, TeamID: policy.TeamID,
			CalendarID:           policy.CalendarID,
			FirstResponseSeconds: int64(policy.FirstResponse.Seconds()),
			ResolutionSeconds:    int64(policy.Resolution.Seconds()),
			Active:               policy.Active,
		}
		if policy.Priority != nil {
			value := string(*policy.Priority)
			response.Priority = &value
		}
		if policy.Kind != nil {
			value := string(*policy.Kind)
			response.Kind = &value
		}
		policies = append(policies, response)
	}

	calendars := make([]slaCalendarResponse, 0, len(snapshot.Calendars))
	for _, calendar := range snapshot.Calendars {
		windows := make([]window, 0, len(calendar.Windows))
		for _, w := range calendar.Windows {
			windows = append(windows, window{
				Weekday: int(w.Weekday), StartMins: w.StartMins, EndMins: w.EndMins,
			})
		}
		calendars = append(calendars, slaCalendarResponse{
			ID: calendar.ID, Name: calendar.Name, Timezone: calendar.Timezone, Windows: windows,
		})
	}

	return map[string]any{"policies": policies, "calendars": calendars}
}
