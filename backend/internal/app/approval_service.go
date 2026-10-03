package app

import (
	"context"

	"github.com/blessnduta/ticketing-system/internal/domain/approval"
	"github.com/blessnduta/ticketing-system/internal/domain/identity"
	"github.com/blessnduta/ticketing-system/internal/domain/shared"
	"github.com/blessnduta/ticketing-system/internal/domain/ticket"
)

// ApprovalService drives the change-approval gate.
type ApprovalService struct {
	tx        TxManager
	approvals ApprovalRepository
	tickets   TicketRepository
	users     UserRepository
	audit     AuditRepository
	events    EventPublisher
	clock     shared.Clock
}

func NewApprovalService(tx TxManager, approvals ApprovalRepository, tickets TicketRepository,
	users UserRepository, audit AuditRepository, events EventPublisher, clock shared.Clock) *ApprovalService {
	return &ApprovalService{
		tx: tx, approvals: approvals, tickets: tickets, users: users,
		audit: audit, events: events, clock: clock,
	}
}

// Request nominates approvers for a change and moves it into the approval gate.
func (s *ApprovalService) Request(ctx context.Context, actor identity.Actor, ticketID shared.ID, approverIDs []shared.ID) ([]*approval.Approval, error) {
	if err := actor.Require(identity.PermTicketTransition); err != nil {
		return nil, err
	}
	if len(approverIDs) == 0 {
		return nil, shared.Invalid("approval.approvers_required", "at least one approver is required")
	}

	var created []*approval.Approval
	err := s.tx.WithinTx(ctx, func(ctx context.Context) error {
		t, err := s.tickets.ByID(ctx, actor.OrgID, ticketID)
		if err != nil {
			return err
		}
		if err := authoriseTicketAccess(actor, t); err != nil {
			return err
		}
		if !t.Kind.RequiresApproval() {
			return shared.RuleViolation("approval.not_applicable",
				"only change requests go through approval")
		}

		// Captured before the transition below mutates the aggregate: the
		// optimistic-lock comparison must be against the value in the database,
		// not the value we are about to write.
		persistedUpdatedAt := t.UpdatedAt

		now := s.clock.Now()
		existing, err := s.approvals.ListForTicket(ctx, actor.OrgID, t.ID)
		if err != nil {
			return err
		}
		alreadyAsked := map[shared.ID]bool{}
		for _, a := range existing {
			alreadyAsked[a.ApproverID] = true
		}

		for _, approverID := range approverIDs {
			if alreadyAsked[approverID] {
				continue
			}
			approver, err := s.users.ByID(ctx, actor.OrgID, approverID)
			if err != nil {
				return shared.Invalid("approval.approver_unknown", "that approver does not exist")
			}
			if !approver.Active {
				return shared.Invalid("approval.approver_inactive", "that approver is deactivated")
			}
			// Only people who could evaluate a change may be asked to approve
			// one — otherwise the gate can be routed around by nominating a
			// requester who will click yes.
			if !approver.Actor().Can(identity.PermApprovalDecide) {
				return shared.Invalid("approval.approver_not_authorised",
					"approvers must be managers or admins").
					WithDetail("approver_id", approverID.String())
			}

			request, err := approval.Request(actor.OrgID, t.ID, approverID, actor.UserID, now)
			if err != nil {
				return err
			}
			if err := s.approvals.Create(ctx, request); err != nil {
				return err
			}
			created = append(created, request)
		}

		if len(created) == 0 {
			return shared.Conflict("approval.already_requested",
				"those approvers have already been asked")
		}

		// Moving into the gate is a normal state transition, so it goes
		// through the state machine like everything else.
		if t.Status == ticket.StatusTriaged {
			if err := t.Transition(ticket.TransitionInput{To: ticket.StatusPendingApproval}, now); err != nil {
				return err
			}
			if err := s.tickets.Update(ctx, t, persistedUpdatedAt); err != nil {
				return err
			}
		}

		return s.audit.Append(ctx, ticket.NewAuditEntry(
			actor.OrgID, t.ID, actor.UserID, actorLabel(ctx, s.users, actor),
			ticket.AuditApprovalAdded, nil, ticket.StrPtr(string(t.Status)), now,
		))
	})
	if err != nil {
		return nil, err
	}

	if t, err := s.tickets.ByID(ctx, actor.OrgID, ticketID); err == nil {
		s.publishApproval(ctx, t, EventApprovalRequested, map[string]any{
			"reference":      t.Reference,
			"approver_count": len(created),
		})
	}
	return created, nil
}

// DecideInput is an approver's verdict.
type DecideInput struct {
	Decision approval.Decision
	Comment  string
}

// Decide records a verdict and, once the set resolves, moves the ticket on.
//
// A rejection cancels the change immediately: there is nothing to wait for,
// and leaving it in the gate would let a second approver's yes overwrite a
// recorded no.
func (s *ApprovalService) Decide(ctx context.Context, actor identity.Actor, approvalID shared.ID, in DecideInput) (*approval.Approval, error) {
	if err := actor.Require(identity.PermApprovalDecide); err != nil {
		return nil, err
	}

	var decided *approval.Approval
	err := s.tx.WithinTx(ctx, func(ctx context.Context) error {
		record, err := s.approvals.ByID(ctx, actor.OrgID, approvalID)
		if err != nil {
			return err
		}
		now := s.clock.Now()
		if err := record.Decide(actor.UserID, in.Decision, in.Comment, now); err != nil {
			return err
		}
		if err := s.approvals.Update(ctx, record); err != nil {
			return err
		}
		decided = record

		t, err := s.tickets.ByID(ctx, actor.OrgID, record.TicketID)
		if err != nil {
			return err
		}
		persistedUpdatedAt := t.UpdatedAt

		all, err := s.approvals.ListForTicket(ctx, actor.OrgID, t.ID)
		if err != nil {
			return err
		}
		outcome := approval.Summarise(all)

		if outcome.HasRejection() && t.Status == ticket.StatusPendingApproval {
			if err := t.Transition(ticket.TransitionInput{To: ticket.StatusCancelled}, now); err != nil {
				return err
			}
			if err := s.tickets.Update(ctx, t, persistedUpdatedAt); err != nil {
				return err
			}
		}
		// A fully-granted set does *not* auto-start the work. The gate
		// unlocking is not the same as somebody picking it up, and pretending
		// otherwise would show a change as "in progress" with nobody on it.

		return s.audit.Append(ctx, ticket.NewAuditEntry(
			actor.OrgID, t.ID, actor.UserID, actorLabel(ctx, s.users, actor),
			ticket.AuditApprovalDecided, nil, ticket.StrPtr(string(in.Decision)), now,
		))
	})
	if err != nil {
		return nil, err
	}

	if t, err := s.tickets.ByID(ctx, actor.OrgID, decided.TicketID); err == nil {
		s.publishApproval(ctx, t, EventApprovalDecided, map[string]any{
			"reference": t.Reference,
			"decision":  string(in.Decision),
		})
	}
	return decided, nil
}

// Inbox lists approvals waiting on this actor — the queue that stops changes
// stalling because nobody knew they were asked.
func (s *ApprovalService) Inbox(ctx context.Context, actor identity.Actor) ([]*approval.Approval, error) {
	if err := actor.Require(identity.PermApprovalDecide); err != nil {
		return nil, err
	}
	return s.approvals.ListPendingForApprover(ctx, actor.OrgID, actor.UserID)
}

func (s *ApprovalService) publishApproval(ctx context.Context, t *ticket.Ticket, eventType EventType, payload map[string]any) {
	if s.events == nil {
		return
	}
	s.events.Publish(ctx, Event{
		Type:     eventType,
		OrgID:    t.OrgID,
		TicketID: t.ID,
		Audience: EventAudience{
			RequesterID:  t.RequesterID,
			AssigneeID:   t.AssigneeID,
			TeamID:       t.TeamID,
			InternalOnly: true,
		},
		Payload: payload,
		At:      s.clock.Now(),
	})
}
