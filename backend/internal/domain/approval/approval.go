// Package approval implements the change-approval gate.
//
// A change request may not begin work until every required approver has said
// yes. Modelling approvals as their own aggregate — rather than a boolean on
// the ticket — is what makes the audit question "who approved this change, and
// when?" answerable, which is the entire reason change management exists.
package approval

import (
	"strings"
	"time"

	"github.com/blessnduta/ticketing-system/internal/domain/shared"
)

type ID = shared.ID

// Decision is an approver's verdict.
type Decision string

const (
	DecisionPending  Decision = "pending"
	DecisionApproved Decision = "approved"
	DecisionRejected Decision = "rejected"
)

const maxCommentLength = 2000

// Approval is one approver's slot on one ticket.
type Approval struct {
	ID         ID
	OrgID      ID
	TicketID   ID
	ApproverID ID
	// RequestedBy records who asked, which matters when the approver later
	// wants to know why this landed in their queue.
	RequestedBy ID
	Decision    Decision
	Comment     string
	DecidedAt   *time.Time
	CreatedAt   time.Time
}

func Request(orgID, ticketID, approverID, requestedBy ID, now time.Time) (*Approval, error) {
	if approverID == shared.NilID {
		return nil, shared.Invalid("approval.approver_required", "an approver is required")
	}
	// Self-approval defeats the control. This is the one rule in the package
	// that cannot be configured away.
	if approverID == requestedBy {
		return nil, shared.RuleViolation("approval.self_approval",
			"you cannot nominate yourself as the approver of your own change")
	}
	return &Approval{
		ID:          shared.NewID(),
		OrgID:       orgID,
		TicketID:    ticketID,
		ApproverID:  approverID,
		RequestedBy: requestedBy,
		Decision:    DecisionPending,
		CreatedAt:   now,
	}, nil
}

// Decide records a verdict. Decisions are final: an approver who changes their
// mind raises a new change, so the record of what was approved at the time the
// work started stays intact.
func (a *Approval) Decide(actorID ID, decision Decision, comment string, now time.Time) error {
	if actorID != a.ApproverID {
		return shared.Forbidden("approval.not_approver", "this approval is assigned to someone else")
	}
	if a.Decision != DecisionPending {
		return shared.Conflict("approval.already_decided", "this approval has already been decided").
			WithDetail("decision", string(a.Decision))
	}
	if decision != DecisionApproved && decision != DecisionRejected {
		return shared.Invalid("approval.decision_invalid", "decision must be approved or rejected")
	}
	comment = strings.TrimSpace(comment)
	if len(comment) > maxCommentLength {
		return shared.Invalid("approval.comment_too_long", "comment is too long")
	}
	// A rejection without a reason is unactionable — the requester cannot fix
	// what they were not told about.
	if decision == DecisionRejected && comment == "" {
		return shared.Invalid("approval.reason_required", "a reason is required when rejecting a change")
	}

	a.Decision = decision
	a.Comment = comment
	a.DecidedAt = &now
	return nil
}

// Outcome summarises a ticket's whole approval set.
type Outcome struct {
	Total    int
	Approved int
	Rejected int
	Pending  int
}

// Summarise folds a set of approvals into an outcome.
func Summarise(approvals []*Approval) Outcome {
	outcome := Outcome{Total: len(approvals)}
	for _, approval := range approvals {
		switch approval.Decision {
		case DecisionApproved:
			outcome.Approved++
		case DecisionRejected:
			outcome.Rejected++
		default:
			outcome.Pending++
		}
	}
	return outcome
}

// Granted reports whether work may begin: at least one approver, none
// rejecting, none outstanding. Unanimity rather than a quorum — a quorum is a
// configurable policy worth adding later, but defaulting to it would let a
// change proceed over a recorded objection, which is the wrong default for a
// control.
func (o Outcome) Granted() bool {
	return o.Total > 0 && o.Approved == o.Total
}

// HasRejection reports whether any approver refused, which cancels the change.
func (o Outcome) HasRejection() bool { return o.Rejected > 0 }
