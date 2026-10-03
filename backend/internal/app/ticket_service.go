package app

import (
	"context"
	"fmt"
	"time"

	"github.com/blessnduta/ticketing-system/internal/domain/approval"
	"github.com/blessnduta/ticketing-system/internal/domain/asset"
	"github.com/blessnduta/ticketing-system/internal/domain/identity"
	"github.com/blessnduta/ticketing-system/internal/domain/shared"
	"github.com/blessnduta/ticketing-system/internal/domain/ticket"
)

// TicketService is the core use case. It orchestrates the domain, persistence
// and events; it contains no business rules of its own — every rule it appears
// to apply is a call into the domain. If a rule starts creeping in here, it
// belongs in ticket.Ticket instead.
type TicketService struct {
	tx        TxManager
	tickets   TicketRepository
	messages  MessageRepository
	audit     AuditRepository
	users     UserRepository
	teams     TeamRepository
	orgs      OrganizationRepository
	assets    AssetRepository
	approvals ApprovalRepository
	slaEngine *SLAService
	events    EventPublisher
	clock     shared.Clock
}

func NewTicketService(
	tx TxManager,
	tickets TicketRepository,
	messages MessageRepository,
	audit AuditRepository,
	users UserRepository,
	teams TeamRepository,
	orgs OrganizationRepository,
	assets AssetRepository,
	approvals ApprovalRepository,
	slaEngine *SLAService,
	events EventPublisher,
	clock shared.Clock,
) *TicketService {
	return &TicketService{
		tx: tx, tickets: tickets, messages: messages, audit: audit, users: users,
		teams: teams, orgs: orgs, assets: assets, approvals: approvals,
		slaEngine: slaEngine, events: events, clock: clock,
	}
}

// ---------------------------------------------------------------------------
// Create
// ---------------------------------------------------------------------------

type CreateTicketInput struct {
	Kind        ticket.Kind
	Subject     string
	Description string
	Category    string
	Tags        []string
	Impact      ticket.Impact
	Urgency     ticket.Urgency
	TeamID      *shared.ID
	AssetIDs    []shared.ID
	// OnBehalfOf lets an agent raise a ticket for someone who phoned in —
	// which is most of them, on a real service desk.
	OnBehalfOf *shared.ID
}

// Create raises a ticket. Reference allocation, SLA target computation, the
// audit entry and the asset links all commit together or not at all: a ticket
// that exists without its audit trail is worse than no ticket.
func (s *TicketService) Create(ctx context.Context, actor identity.Actor, in CreateTicketInput) (*TicketView, error) {
	if err := actor.Require(identity.PermTicketCreate); err != nil {
		return nil, err
	}
	now := s.clock.Now()

	requesterID := actor.UserID
	if in.OnBehalfOf != nil && *in.OnBehalfOf != actor.UserID {
		// Only staff may file on someone else's behalf; otherwise a requester
		// could attribute tickets to colleagues.
		if err := actor.Require(identity.PermTicketReadTeam); err != nil {
			return nil, shared.Forbidden("ticket.on_behalf_forbidden",
				"you may only raise tickets for yourself")
		}
		if _, err := s.users.ByID(ctx, actor.OrgID, *in.OnBehalfOf); err != nil {
			return nil, shared.Invalid("ticket.requester_unknown", "that requester does not exist")
		}
		requesterID = *in.OnBehalfOf
	}

	if in.TeamID != nil {
		if _, err := s.teams.ByID(ctx, actor.OrgID, *in.TeamID); err != nil {
			return nil, shared.Invalid("ticket.team_unknown", "that team does not exist")
		}
	}

	var view *TicketView
	err := s.tx.WithinTx(ctx, func(ctx context.Context) error {
		reference, err := s.orgs.NextTicketReference(ctx, actor.OrgID)
		if err != nil {
			return err
		}

		t, err := ticket.New(ticket.NewInput{
			OrgID:       actor.OrgID,
			Reference:   reference,
			Kind:        in.Kind,
			Subject:     in.Subject,
			Description: in.Description,
			Category:    in.Category,
			Tags:        in.Tags,
			Impact:      in.Impact,
			Urgency:     in.Urgency,
			RequesterID: requesterID,
			TeamID:      in.TeamID,
		}, now)
		if err != nil {
			return err
		}

		if err := s.tickets.Create(ctx, t); err != nil {
			return err
		}

		for _, assetID := range in.AssetIDs {
			if _, err := s.assets.ByID(ctx, actor.OrgID, assetID); err != nil {
				return shared.Invalid("ticket.asset_unknown", "one of the linked assets does not exist").
					WithDetail("asset_id", assetID.String())
			}
			if err := s.assets.LinkToTicket(ctx, actor.OrgID, t.ID, assetID); err != nil {
				return err
			}
		}

		// SLA targets are computed at creation, not at triage: the clock on a
		// P1 starts when it is reported, not when someone gets round to it.
		//
		// persistedUpdatedAt is captured *before* ApplyTargets mutates the
		// aggregate: it is the value currently in the database, which is what
		// the optimistic-lock comparison has to match. Passing the post-mutation
		// t.UpdatedAt would compare the new value against the stored old one and
		// never match.
		persistedUpdatedAt := t.UpdatedAt
		if err := s.slaEngine.ApplyTargets(ctx, t, now); err != nil {
			return err
		}
		if err := s.tickets.Update(ctx, t, persistedUpdatedAt); err != nil {
			return err
		}

		if err := s.audit.Append(ctx, ticket.NewAuditEntry(
			actor.OrgID, t.ID, actor.UserID, actorLabel(ctx, s.users, actor),
			ticket.AuditCreated, nil, ticket.StrPtr(string(t.Status)), now,
		)); err != nil {
			return err
		}

		view, err = s.buildView(ctx, actor, t)
		return err
	})
	if err != nil {
		return nil, err
	}

	s.publish(ctx, view.Ticket, EventTicketCreated, actor, map[string]any{
		"reference": view.Ticket.Reference,
		"subject":   view.Ticket.Subject,
		"priority":  string(view.Ticket.Priority),
	}, false)

	return view, nil
}

// ---------------------------------------------------------------------------
// Read
// ---------------------------------------------------------------------------

// TicketView is the read model: the aggregate plus everything a screen needs,
// assembled once. Returning this rather than a bare ticket is what keeps the
// frontend from making six follow-up calls to render one page.
type TicketView struct {
	Ticket     *ticket.Ticket
	Messages   []*ticket.Message
	Assets     []*asset.Asset
	Approvals  []*approval.Approval
	Requester  *identity.User
	Assignee   *identity.User
	// AllowedTransitions is computed from the same state machine the server
	// enforces, so the UI's buttons and the server's rules cannot drift apart.
	AllowedTransitions []ticket.Status
	// CanReadInternal tells the client whether it is seeing the full thread,
	// so it can label the view honestly rather than implying completeness.
	CanReadInternal bool
}

// Get loads one ticket with its thread, honouring visibility.
func (s *TicketService) Get(ctx context.Context, actor identity.Actor, ticketID shared.ID) (*TicketView, error) {
	t, err := s.loadAuthorised(ctx, actor, ticketID)
	if err != nil {
		return nil, err
	}
	return s.buildView(ctx, actor, t)
}

// GetByReference resolves the human-facing handle agents actually quote.
func (s *TicketService) GetByReference(ctx context.Context, actor identity.Actor, reference string) (*TicketView, error) {
	t, err := s.tickets.ByReference(ctx, actor.OrgID, reference)
	if err != nil {
		return nil, err
	}
	if err := authoriseTicketAccess(actor, t); err != nil {
		return nil, err
	}
	return s.buildView(ctx, actor, t)
}

// Search runs the ticket list. The caller supplies filters; the *scope* is
// always overwritten from the actor, so a crafted request cannot widen it.
func (s *TicketService) Search(ctx context.Context, actor identity.Actor, query TicketQuery) (shared.Page[*ticket.Ticket], error) {
	query.Scope = ticketScopeFor(actor)
	return s.tickets.Search(ctx, actor.OrgID, query)
}

// ---------------------------------------------------------------------------
// Mutations
// ---------------------------------------------------------------------------

type TransitionInput struct {
	To         ticket.Status
	Resolution *string
	// ExpectedUpdatedAt is the optimistic-lock token the client echoes back
	// from its last read.
	ExpectedUpdatedAt time.Time
}

// Transition moves a ticket through the state machine.
func (s *TicketService) Transition(ctx context.Context, actor identity.Actor, ticketID shared.ID, in TransitionInput) (*TicketView, error) {
	if err := actor.Require(identity.PermTicketTransition); err != nil {
		return nil, err
	}

	var (
		view *TicketView
		from ticket.Status
	)
	err := s.tx.WithinTx(ctx, func(ctx context.Context) error {
		t, err := s.loadAuthorised(ctx, actor, ticketID)
		if err != nil {
			return err
		}
		from = t.Status

		approvalGranted, err := s.approvalGranted(ctx, actor.OrgID, t)
		if err != nil {
			return err
		}

		if err := t.Transition(ticket.TransitionInput{
			To:              in.To,
			Resolution:      in.Resolution,
			ApprovalGranted: approvalGranted,
		}, s.clock.Now()); err != nil {
			return err
		}

		if err := s.tickets.Update(ctx, t, in.ExpectedUpdatedAt); err != nil {
			return err
		}

		if err := s.audit.Append(ctx, ticket.NewAuditEntry(
			actor.OrgID, t.ID, actor.UserID, actorLabel(ctx, s.users, actor),
			ticket.AuditStatusChanged, ticket.StrPtr(string(from)), ticket.StrPtr(string(in.To)),
			s.clock.Now(),
		)); err != nil {
			return err
		}

		view, err = s.buildView(ctx, actor, t)
		return err
	})
	if err != nil {
		return nil, err
	}

	s.publish(ctx, view.Ticket, EventTicketStatusChanged, actor, map[string]any{
		"from":      string(from),
		"to":        string(in.To),
		"reference": view.Ticket.Reference,
	}, false)

	return view, nil
}

type AssignInput struct {
	// AssigneeID nil means "return to the team queue".
	AssigneeID        *shared.ID
	ExpectedUpdatedAt time.Time
}

// Assign changes ownership, verifying the assignee is a real, active member of
// the organisation who can actually work tickets.
func (s *TicketService) Assign(ctx context.Context, actor identity.Actor, ticketID shared.ID, in AssignInput) (*TicketView, error) {
	if err := actor.Require(identity.PermTicketAssign); err != nil {
		return nil, err
	}

	var view *TicketView
	err := s.tx.WithinTx(ctx, func(ctx context.Context) error {
		t, err := s.loadAuthorised(ctx, actor, ticketID)
		if err != nil {
			return err
		}

		previous := ""
		if t.AssigneeID != nil {
			previous = t.AssigneeID.String()
		}

		if in.AssigneeID != nil {
			assignee, err := s.users.ByID(ctx, actor.OrgID, *in.AssigneeID)
			if err != nil {
				return shared.Invalid("ticket.assignee_unknown", "that user does not exist")
			}
			if !assignee.Active {
				return shared.Invalid("ticket.assignee_inactive", "that user is deactivated")
			}
			if !assignee.Actor().Can(identity.PermTicketTransition) {
				return shared.Invalid("ticket.assignee_not_agent",
					"tickets can only be assigned to agents, managers or admins")
			}
			// An agent may only assign within their own team unless they hold
			// the cross-team permission. Otherwise "assign" becomes a way to
			// push work onto a queue you are not accountable for.
			if !actor.Can(identity.PermTicketAssignCross) {
				if assignee.TeamID == nil || !actor.InTeam(*assignee.TeamID) {
					return shared.Forbidden("ticket.assign_cross_team",
						"you can only assign tickets to members of your own team")
				}
			}
		}

		if err := t.Assign(in.AssigneeID, s.clock.Now()); err != nil {
			return err
		}
		if err := s.tickets.Update(ctx, t, in.ExpectedUpdatedAt); err != nil {
			return err
		}

		next := "unassigned"
		if in.AssigneeID != nil {
			next = in.AssigneeID.String()
		}
		if err := s.audit.Append(ctx, ticket.NewAuditEntry(
			actor.OrgID, t.ID, actor.UserID, actorLabel(ctx, s.users, actor),
			ticket.AuditAssigned, ticket.StrPtr(previous), ticket.StrPtr(next), s.clock.Now(),
		)); err != nil {
			return err
		}

		view, err = s.buildView(ctx, actor, t)
		return err
	})
	if err != nil {
		return nil, err
	}

	s.publish(ctx, view.Ticket, EventTicketAssigned, actor, map[string]any{
		"assignee_id": in.AssigneeID,
		"reference":   view.Ticket.Reference,
	}, false)

	return view, nil
}

type RouteInput struct {
	TeamID            *shared.ID
	ExpectedUpdatedAt time.Time
}

// Route moves a ticket between team queues.
func (s *TicketService) Route(ctx context.Context, actor identity.Actor, ticketID shared.ID, in RouteInput) (*TicketView, error) {
	if err := actor.Require(identity.PermTicketAssign); err != nil {
		return nil, err
	}

	var view *TicketView
	err := s.tx.WithinTx(ctx, func(ctx context.Context) error {
		t, err := s.loadAuthorised(ctx, actor, ticketID)
		if err != nil {
			return err
		}
		if in.TeamID != nil {
			if _, err := s.teams.ByID(ctx, actor.OrgID, *in.TeamID); err != nil {
				return shared.Invalid("ticket.team_unknown", "that team does not exist")
			}
		}

		previous := "unrouted"
		if t.TeamID != nil {
			previous = t.TeamID.String()
		}
		if err := t.Route(in.TeamID, s.clock.Now()); err != nil {
			return err
		}
		// Routing can change which SLA policy applies (policies may be
		// team-specific), so targets are recomputed rather than left stale.
		if err := s.slaEngine.ApplyTargets(ctx, t, s.clock.Now()); err != nil {
			return err
		}
		if err := s.tickets.Update(ctx, t, in.ExpectedUpdatedAt); err != nil {
			return err
		}

		next := "unrouted"
		if in.TeamID != nil {
			next = in.TeamID.String()
		}
		if err := s.audit.Append(ctx, ticket.NewAuditEntry(
			actor.OrgID, t.ID, actor.UserID, actorLabel(ctx, s.users, actor),
			ticket.AuditRouted, ticket.StrPtr(previous), ticket.StrPtr(next), s.clock.Now(),
		)); err != nil {
			return err
		}

		view, err = s.buildView(ctx, actor, t)
		return err
	})
	if err != nil {
		return nil, err
	}

	s.publish(ctx, view.Ticket, EventTicketRouted, actor, map[string]any{
		"team_id":   in.TeamID,
		"reference": view.Ticket.Reference,
	}, false)
	return view, nil
}

type ReclassifyInput struct {
	Impact            ticket.Impact
	Urgency           ticket.Urgency
	ExpectedUpdatedAt time.Time
}

// Reclassify changes impact/urgency, which re-derives priority and therefore
// may select a different SLA policy.
func (s *TicketService) Reclassify(ctx context.Context, actor identity.Actor, ticketID shared.ID, in ReclassifyInput) (*TicketView, error) {
	if err := actor.Require(identity.PermTicketTransition); err != nil {
		return nil, err
	}

	var view *TicketView
	err := s.tx.WithinTx(ctx, func(ctx context.Context) error {
		t, err := s.loadAuthorised(ctx, actor, ticketID)
		if err != nil {
			return err
		}
		previous := string(t.Priority)
		if err := t.Reclassify(in.Impact, in.Urgency, s.clock.Now()); err != nil {
			return err
		}
		if err := s.slaEngine.ApplyTargets(ctx, t, s.clock.Now()); err != nil {
			return err
		}
		if err := s.tickets.Update(ctx, t, in.ExpectedUpdatedAt); err != nil {
			return err
		}
		if err := s.audit.Append(ctx, ticket.NewAuditEntry(
			actor.OrgID, t.ID, actor.UserID, actorLabel(ctx, s.users, actor),
			ticket.AuditReclassified, ticket.StrPtr(previous), ticket.StrPtr(string(t.Priority)),
			s.clock.Now(),
		)); err != nil {
			return err
		}
		view, err = s.buildView(ctx, actor, t)
		return err
	})
	return view, err
}

type AddMessageInput struct {
	Body       string
	Visibility ticket.Visibility
}

// AddMessage appends to the thread and, if it is the first public agent reply,
// settles the first-response SLA.
func (s *TicketService) AddMessage(ctx context.Context, actor identity.Actor, ticketID shared.ID, in AddMessageInput) (*ticket.Message, error) {
	// Internal notes require the permission. A requester posting
	// `visibility: internal` must be rejected outright rather than silently
	// downgraded — silently rewriting a caller's intent hides bugs, and a
	// requester who believes a note was private deserves to be told it is not.
	if in.Visibility == ticket.VisibilityInternal && !actor.Can(identity.PermNoteInternal) {
		return nil, shared.Forbidden("message.internal_forbidden",
			"you do not have permission to post internal notes")
	}

	var message *ticket.Message
	err := s.tx.WithinTx(ctx, func(ctx context.Context) error {
		t, err := s.loadAuthorised(ctx, actor, ticketID)
		if err != nil {
			return err
		}
		if t.Status.Terminal() {
			return shared.RuleViolation("ticket.closed",
				"this ticket is closed — reopen it before adding a message")
		}

		// Captured before any mutation below — see the note in Create.
		persistedUpdatedAt := t.UpdatedAt

		now := s.clock.Now()
		message, err = ticket.NewMessage(actor.OrgID, t.ID, &actor.UserID, in.Body, in.Visibility, now)
		if err != nil {
			return err
		}
		if err := s.messages.Create(ctx, message); err != nil {
			return err
		}

		// A public reply from staff (not the requester) is a first response.
		isAgentReply := in.Visibility == ticket.VisibilityPublic &&
			actor.UserID != t.RequesterID &&
			actor.Can(identity.PermTicketTransition)

		ticketChanged := false
		if isAgentReply && t.FirstRespAt == nil {
			t.RecordFirstResponse(now)
			ticketChanged = true
		}
		// The requester replying to a ticket that was waiting on them unblocks
		// it automatically. Making a human do this by hand is how tickets sit
		// in "pending" for a week after the answer already arrived.
		if actor.UserID == t.RequesterID && t.Status == ticket.StatusPendingRequester {
			if err := t.Transition(ticket.TransitionInput{To: ticket.StatusInProgress}, now); err != nil {
				// The auto-resume is a convenience; if the machine refuses
				// (e.g. no assignee), leave the status alone rather than
				// failing the message the requester actually wanted to send.
				_ = err
			} else {
				ticketChanged = true
			}
		}

		if ticketChanged {
			if err := s.tickets.Update(ctx, t, persistedUpdatedAt); err != nil {
				return err
			}
		}

		return s.audit.Append(ctx, ticket.NewAuditEntry(
			actor.OrgID, t.ID, actor.UserID, actorLabel(ctx, s.users, actor),
			ticket.AuditMessageAdded, nil, ticket.StrPtr(string(in.Visibility)), now,
		))
	})
	if err != nil {
		return nil, err
	}

	// Reload for the event envelope. Cheap, and it guarantees the audience is
	// computed from committed state rather than from the in-flight aggregate.
	if t, err := s.tickets.ByID(ctx, actor.OrgID, ticketID); err == nil {
		s.publish(ctx, t, EventTicketMessageAdded, actor, map[string]any{
			"message_id": message.ID.String(),
			"reference":  t.Reference,
			"preview":    preview(message.Body, 140),
		}, in.Visibility == ticket.VisibilityInternal)
	}

	return message, nil
}

// LinkAsset attaches a configuration item to a ticket.
func (s *TicketService) LinkAsset(ctx context.Context, actor identity.Actor, ticketID, assetID shared.ID) error {
	if err := actor.Require(identity.PermTicketTransition); err != nil {
		return err
	}
	return s.tx.WithinTx(ctx, func(ctx context.Context) error {
		t, err := s.loadAuthorised(ctx, actor, ticketID)
		if err != nil {
			return err
		}
		linked, err := s.assets.ByID(ctx, actor.OrgID, assetID)
		if err != nil {
			return err
		}
		if err := s.assets.LinkToTicket(ctx, actor.OrgID, t.ID, linked.ID); err != nil {
			return err
		}
		return s.audit.Append(ctx, ticket.NewAuditEntry(
			actor.OrgID, t.ID, actor.UserID, actorLabel(ctx, s.users, actor),
			ticket.AuditAssetLinked, nil, ticket.StrPtr(linked.Tag), s.clock.Now(),
		))
	})
}

// UnlinkAsset detaches a configuration item.
func (s *TicketService) UnlinkAsset(ctx context.Context, actor identity.Actor, ticketID, assetID shared.ID) error {
	if err := actor.Require(identity.PermTicketTransition); err != nil {
		return err
	}
	return s.tx.WithinTx(ctx, func(ctx context.Context) error {
		t, err := s.loadAuthorised(ctx, actor, ticketID)
		if err != nil {
			return err
		}
		if err := s.assets.UnlinkFromTicket(ctx, actor.OrgID, t.ID, assetID); err != nil {
			return err
		}
		return s.audit.Append(ctx, ticket.NewAuditEntry(
			actor.OrgID, t.ID, actor.UserID, actorLabel(ctx, s.users, actor),
			ticket.AuditAssetUnlinked, ticket.StrPtr(assetID.String()), nil, s.clock.Now(),
		))
	})
}

// AuditTrail returns the ticket's history.
func (s *TicketService) AuditTrail(ctx context.Context, actor identity.Actor, ticketID shared.ID, page shared.Pagination) (shared.Page[*ticket.AuditEntry], error) {
	if err := actor.Require(identity.PermAuditRead); err != nil {
		return shared.Page[*ticket.AuditEntry]{}, err
	}
	if _, err := s.loadAuthorised(ctx, actor, ticketID); err != nil {
		return shared.Page[*ticket.AuditEntry]{}, err
	}
	return s.audit.ListForTicket(ctx, actor.OrgID, ticketID, page)
}

// ---------------------------------------------------------------------------
// Internals
// ---------------------------------------------------------------------------

// loadAuthorised is the single choke point for "fetch a ticket this actor may
// touch". Every mutation goes through it, so there is exactly one place where
// the access rule could be got wrong, and exactly one place to audit.
func (s *TicketService) loadAuthorised(ctx context.Context, actor identity.Actor, ticketID shared.ID) (*ticket.Ticket, error) {
	if actor.IsZero() {
		return nil, shared.Unauthorized("auth.required", "authentication required")
	}
	t, err := s.tickets.ByID(ctx, actor.OrgID, ticketID)
	if err != nil {
		return nil, err
	}
	if err := authoriseTicketAccess(actor, t); err != nil {
		return nil, err
	}
	return t, nil
}

func (s *TicketService) buildView(ctx context.Context, actor identity.Actor, t *ticket.Ticket) (*TicketView, error) {
	includeInternal := canReadInternalNotes(actor)

	messages, err := s.messages.ListForTicket(ctx, actor.OrgID, t.ID, includeInternal)
	if err != nil {
		return nil, err
	}
	linkedAssets, err := s.assets.ListForTicket(ctx, actor.OrgID, t.ID)
	if err != nil {
		return nil, err
	}

	view := &TicketView{
		Ticket:          t,
		Messages:        messages,
		Assets:          linkedAssets,
		CanReadInternal: includeInternal,
	}

	// Approvals are only relevant to changes, and only staff should see who
	// was asked to approve what.
	granted := false
	if t.Kind.RequiresApproval() && includeInternal {
		approvals, err := s.approvals.ListForTicket(ctx, actor.OrgID, t.ID)
		if err != nil {
			return nil, err
		}
		view.Approvals = approvals
		granted = approval.Summarise(approvals).Granted()
	} else if t.Kind.RequiresApproval() {
		granted, err = s.approvalGranted(ctx, actor.OrgID, t)
		if err != nil {
			return nil, err
		}
	}

	if requester, err := s.users.ByID(ctx, actor.OrgID, t.RequesterID); err == nil {
		view.Requester = requester
	}
	if t.AssigneeID != nil {
		if assignee, err := s.users.ByID(ctx, actor.OrgID, *t.AssigneeID); err == nil {
			view.Assignee = assignee
		}
	}

	// Only offer transitions to actors who could actually perform one.
	if actor.Can(identity.PermTicketTransition) {
		view.AllowedTransitions = ticket.AllowedFrom(t.Kind, t.Status, t.TransitionContext(granted))
	}

	return view, nil
}

func (s *TicketService) approvalGranted(ctx context.Context, orgID shared.ID, t *ticket.Ticket) (bool, error) {
	if !t.Kind.RequiresApproval() {
		// Non-change tickets have no gate, so the guard is trivially satisfied.
		return true, nil
	}
	approvals, err := s.approvals.ListForTicket(ctx, orgID, t.ID)
	if err != nil {
		return false, err
	}
	return approval.Summarise(approvals).Granted(), nil
}

// publish emits a realtime/notification event. Failures here must never fail
// the request: the ticket change is already committed and durable, and a
// dropped notification is a lesser harm than a 500 on a write that succeeded.
func (s *TicketService) publish(ctx context.Context, t *ticket.Ticket, eventType EventType, actor identity.Actor, payload map[string]any, internalOnly bool) {
	if s.events == nil || t == nil {
		return
	}
	actorID := actor.UserID
	s.events.Publish(ctx, Event{
		Type:     eventType,
		OrgID:    t.OrgID,
		TicketID: t.ID,
		ActorID:  &actorID,
		Audience: EventAudience{
			RequesterID:  t.RequesterID,
			AssigneeID:   t.AssigneeID,
			TeamID:       t.TeamID,
			InternalOnly: internalOnly,
		},
		Payload: payload,
		At:      s.clock.Now(),
	})
}

// actorLabel resolves a human-readable name for the audit trail. It falls back
// to the ID rather than erroring: an audit entry must be written even if the
// name lookup fails, because the entry is the point.
func actorLabel(ctx context.Context, users UserRepository, actor identity.Actor) string {
	if user, err := users.ByID(ctx, actor.OrgID, actor.UserID); err == nil && user != nil {
		return fmt.Sprintf("%s <%s>", user.FullName, user.Email)
	}
	return actor.UserID.String()
}

func preview(body string, limit int) string {
	runes := []rune(body)
	if len(runes) <= limit {
		return body
	}
	return string(runes[:limit]) + "…"
}
