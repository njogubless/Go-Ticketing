// Package ticket contains the system's central aggregate. Everything that can
// invalidate a ticket is enforced by a method on this type — there are no
// exported setters, because a struct with public mutable fields is a struct
// with no invariants.
package ticket

import (
	"strings"
	"time"

	"github.com/blessnduta/ticketing-system/internal/domain/shared"
)

type ID = shared.ID

const (
	MaxSubjectLength     = 200
	MaxDescriptionLength = 20000
	MaxResolutionLength  = 10000
	MaxCategoryLength    = 60
	MaxTagLength         = 40
	MaxTags              = 12
)

// Ticket is the aggregate root. Messages, attachments and approvals are
// separate entities referencing it by ID rather than embedded slices: a ticket
// with four years of correspondence must not require loading four years of
// correspondence to change its status.
type Ticket struct {
	ID    ID
	OrgID ID
	// Reference is the human-facing handle, e.g. "ACME-1042". Unique per
	// organisation, monotonic, and assigned by the database sequence — see the
	// migration. Agents and requesters quote this; nobody quotes a UUID.
	Reference string

	Kind        Kind
	Subject     string
	Description string
	Category    string
	Tags        []string

	Status   Status
	Impact   Impact
	Urgency  Urgency
	Priority Priority // derived; never set independently

	RequesterID ID
	AssigneeID  *ID
	TeamID      *ID

	// Resolution is the closing note. Required to enter StatusResolved.
	Resolution   *string
	ResolvedAt   *time.Time
	ClosedAt     *time.Time
	FirstRespAt  *time.Time
	ReopenCount  int

	// --- SLA accounting ---------------------------------------------------
	// Targets are absolute wall-clock deadlines, precomputed at triage from
	// the policy's business-minutes budget. Storing an absolute instant rather
	// than recomputing on read is what allows "show me everything breaching in
	// the next hour" to be an indexed query instead of a full scan.
	SLAPolicyID       *ID
	FirstResponseDue  *time.Time
	ResolutionDue     *time.Time
	// PausedSince is set while the ticket sits in a status that stops the
	// clock. On resume, the elapsed span is added to the deadlines.
	PausedSince       *time.Time
	FirstResponseMet  *bool
	ResolutionMet     *bool
	// BreachNotifiedAt makes the breach worker idempotent: a ticket already
	// notified is skipped, so a worker restart mid-batch cannot double-alert.
	BreachNotifiedAt *time.Time

	CreatedAt time.Time
	UpdatedAt time.Time
}

// NewInput is the validated construction payload. A parameter struct rather
// than eleven positional arguments — the compiler cannot catch two swapped
// adjacent ID arguments, but a named field cannot be swapped at all.
type NewInput struct {
	OrgID       ID
	Reference   string
	Kind        Kind
	Subject     string
	Description string
	Category    string
	Tags        []string
	Impact      Impact
	Urgency     Urgency
	RequesterID ID
	TeamID      *ID
}

// New creates a ticket in StatusNew. Priority is derived, never accepted.
func New(in NewInput, now time.Time) (*Ticket, error) {
	subject := strings.TrimSpace(in.Subject)
	if subject == "" {
		return nil, shared.Invalid("ticket.subject_required", "a subject is required")
	}
	if len(subject) > MaxSubjectLength {
		return nil, shared.Invalid("ticket.subject_too_long", "subject is too long").
			WithDetail("max_length", MaxSubjectLength)
	}

	description := strings.TrimSpace(in.Description)
	if description == "" {
		return nil, shared.Invalid("ticket.description_required", "a description is required")
	}
	if len(description) > MaxDescriptionLength {
		return nil, shared.Invalid("ticket.description_too_long", "description is too long").
			WithDetail("max_length", MaxDescriptionLength)
	}

	category := strings.TrimSpace(in.Category)
	if len(category) > MaxCategoryLength {
		return nil, shared.Invalid("ticket.category_too_long", "category is too long")
	}

	tags, err := normaliseTags(in.Tags)
	if err != nil {
		return nil, err
	}
	if in.RequesterID == shared.NilID {
		return nil, shared.Internal("ticket.requester_missing", "requester is required")
	}

	return &Ticket{
		ID:          shared.NewID(),
		OrgID:       in.OrgID,
		Reference:   in.Reference,
		Kind:        in.Kind,
		Subject:     subject,
		Description: description,
		Category:    category,
		Tags:        tags,
		Status:      StatusNew,
		Impact:      in.Impact,
		Urgency:     in.Urgency,
		Priority:    DerivePriority(in.Impact, in.Urgency),
		RequesterID: in.RequesterID,
		TeamID:      in.TeamID,
		CreatedAt:   now,
		UpdatedAt:   now,
	}, nil
}

// TransitionContext snapshots the guard inputs from the ticket's own state.
// approvalGranted must be supplied by the caller because approvals live in
// their own aggregate — the ticket does not get to decide whether it was
// approved.
func (t *Ticket) TransitionContext(approvalGranted bool) TransitionContext {
	return TransitionContext{
		HasAssignee:     t.AssigneeID != nil,
		HasResolution:   t.Resolution != nil && strings.TrimSpace(*t.Resolution) != "",
		ApprovalGranted: approvalGranted,
	}
}

// TransitionInput carries everything a status change may need to also set,
// so that "resolve with this note" is one atomic, validated operation rather
// than a write-then-transition pair that can half-fail.
type TransitionInput struct {
	To              Status
	Resolution      *string
	ApprovalGranted bool
}

// Transition moves the ticket, enforcing the state machine and applying every
// side effect the new status implies (timestamps, SLA pause/resume, outcome
// flags). Nothing outside this method may write Status.
func (t *Ticket) Transition(in TransitionInput, now time.Time) error {
	if in.Resolution != nil {
		resolution := strings.TrimSpace(*in.Resolution)
		if resolution == "" {
			return shared.Invalid("ticket.resolution_blank", "resolution cannot be blank")
		}
		if len(resolution) > MaxResolutionLength {
			return shared.Invalid("ticket.resolution_too_long", "resolution is too long").
				WithDetail("max_length", MaxResolutionLength)
		}
		t.Resolution = &resolution
	}

	if err := CanTransition(t.Kind, t.Status, in.To, t.TransitionContext(in.ApprovalGranted)); err != nil {
		return err
	}

	from := t.Status
	t.Status = in.To
	t.UpdatedAt = now

	// The SLA clock reacts to *changes* in pause state, not to the status
	// itself. Deriving it from the pair (from, to) rather than from `to` alone
	// is what keeps pending → pending-adjacent moves from double-counting.
	switch {
	case !from.PausesSLA() && in.To.PausesSLA():
		t.pauseSLA(now)
	case from.PausesSLA() && !in.To.PausesSLA():
		t.resumeSLA(now)
	}

	switch in.To {
	case StatusResolved:
		t.ResolvedAt = &now
		met := t.ResolutionDue == nil || !now.After(*t.ResolutionDue)
		t.ResolutionMet = &met
	case StatusClosed:
		t.ClosedAt = &now
		if t.ResolvedAt == nil {
			t.ResolvedAt = &now
		}
	case StatusCancelled:
		t.ClosedAt = &now
	case StatusTriaged:
		// Coming back from a terminal or resolved status is a reopen. Counting
		// reopens is the cheapest available proxy for "we closed it too early",
		// which is the metric that actually predicts customer dissatisfaction.
		if from == StatusResolved || from == StatusClosed {
			t.ReopenCount++
			t.ResolvedAt = nil
			t.ClosedAt = nil
			t.ResolutionMet = nil
		}
	}

	return nil
}

func (t *Ticket) pauseSLA(now time.Time) {
	if t.PausedSince == nil {
		t.PausedSince = &now
	}
}

// resumeSLA pushes both deadlines forward by the time spent paused, so the
// desk is measured on time it could actually act.
func (t *Ticket) resumeSLA(now time.Time) {
	if t.PausedSince == nil {
		return
	}
	paused := now.Sub(*t.PausedSince)
	if paused > 0 {
		if t.FirstResponseDue != nil && t.FirstResponseMet == nil {
			shifted := t.FirstResponseDue.Add(paused)
			t.FirstResponseDue = &shifted
		}
		if t.ResolutionDue != nil {
			shifted := t.ResolutionDue.Add(paused)
			t.ResolutionDue = &shifted
		}
		// A ticket that resumes has, by definition, moved again — clear any
		// prior breach notification so a subsequent breach alerts afresh.
		t.BreachNotifiedAt = nil
	}
	t.PausedSince = nil
}

// Assign sets the owner. Passing nil unassigns, which returns the ticket to
// the team queue.
func (t *Ticket) Assign(assigneeID *ID, now time.Time) error {
	if t.Status.Terminal() {
		return shared.RuleViolation("ticket.terminal", "a closed or cancelled ticket cannot be reassigned")
	}
	t.AssigneeID = assigneeID
	t.UpdatedAt = now
	return nil
}

// Route moves the ticket to a different team queue.
func (t *Ticket) Route(teamID *ID, now time.Time) error {
	if t.Status.Terminal() {
		return shared.RuleViolation("ticket.terminal", "a closed or cancelled ticket cannot be rerouted")
	}
	t.TeamID = teamID
	// Ownership does not survive a team change: the previous assignee is, by
	// construction, on the wrong team now.
	t.AssigneeID = nil
	t.UpdatedAt = now
	return nil
}

// Reclassify changes impact/urgency and re-derives priority. Priority is never
// settable on its own — that is the invariant the whole matrix exists to hold.
func (t *Ticket) Reclassify(impact Impact, urgency Urgency, now time.Time) error {
	if t.Status.Terminal() {
		return shared.RuleViolation("ticket.terminal", "a closed or cancelled ticket cannot be reclassified")
	}
	t.Impact = impact
	t.Urgency = urgency
	t.Priority = DerivePriority(impact, urgency)
	t.UpdatedAt = now
	return nil
}

// ApplySLATargets records the deadlines computed by the SLA engine. The ticket
// stores them; it does not calculate them — business-hours arithmetic belongs
// to the sla package, and keeping it there stops the aggregate from growing a
// calendar dependency.
func (t *Ticket) ApplySLATargets(policyID ID, firstResponseDue, resolutionDue time.Time, now time.Time) {
	t.SLAPolicyID = &policyID
	t.FirstResponseDue = &firstResponseDue
	t.ResolutionDue = &resolutionDue
	t.UpdatedAt = now
}

// RecordFirstResponse stamps the first agent reply and settles the
// first-response SLA outcome. Idempotent: only the first call counts, because
// "first" response means first.
func (t *Ticket) RecordFirstResponse(at time.Time) {
	if t.FirstRespAt != nil {
		return
	}
	t.FirstRespAt = &at
	met := t.FirstResponseDue == nil || !at.After(*t.FirstResponseDue)
	t.FirstResponseMet = &met
	t.UpdatedAt = at
}

// MarkBreachNotified records that an alert has gone out, so the worker does
// not re-alert on its next pass.
func (t *Ticket) MarkBreachNotified(at time.Time) {
	t.BreachNotifiedAt = &at
	t.UpdatedAt = at
}

// BreachedAt reports whether the ticket is late *right now*: still being
// worked, past its resolution target, with the clock running.
//
// This is a live-queue question, distinct from "did this ticket meet its SLA?",
// which is answered by ResolutionMet and settled once at resolution time. The
// distinction matters — see Status.SLAClockRunning.
func (t *Ticket) BreachedAt(at time.Time) bool {
	if !t.Status.SLAClockRunning() || t.ResolutionDue == nil {
		return false
	}
	return at.After(*t.ResolutionDue)
}

// AddTag and RemoveTag keep the tag set normalised and bounded.
func (t *Ticket) AddTag(tag string, now time.Time) error {
	normalised, err := normaliseTag(tag)
	if err != nil {
		return err
	}
	for _, existing := range t.Tags {
		if existing == normalised {
			return nil
		}
	}
	if len(t.Tags) >= MaxTags {
		return shared.Invalid("ticket.too_many_tags", "a ticket may not have more tags").
			WithDetail("max_tags", MaxTags)
	}
	t.Tags = append(t.Tags, normalised)
	t.UpdatedAt = now
	return nil
}

func (t *Ticket) RemoveTag(tag string, now time.Time) {
	normalised := strings.ToLower(strings.TrimSpace(tag))
	kept := t.Tags[:0]
	for _, existing := range t.Tags {
		if existing != normalised {
			kept = append(kept, existing)
		}
	}
	t.Tags = kept
	t.UpdatedAt = now
}

func normaliseTags(raw []string) ([]string, error) {
	if len(raw) > MaxTags {
		return nil, shared.Invalid("ticket.too_many_tags", "too many tags").WithDetail("max_tags", MaxTags)
	}
	seen := map[string]struct{}{}
	out := make([]string, 0, len(raw))
	for _, tag := range raw {
		normalised, err := normaliseTag(tag)
		if err != nil {
			return nil, err
		}
		if _, duplicate := seen[normalised]; duplicate {
			continue
		}
		seen[normalised] = struct{}{}
		out = append(out, normalised)
	}
	return out, nil
}

func normaliseTag(raw string) (string, error) {
	tag := strings.ToLower(strings.TrimSpace(raw))
	if tag == "" {
		return "", shared.Invalid("ticket.tag_blank", "a tag cannot be blank")
	}
	if len(tag) > MaxTagLength {
		return "", shared.Invalid("ticket.tag_too_long", "tag is too long").
			WithDetail("max_length", MaxTagLength)
	}
	return tag, nil
}
