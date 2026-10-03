package ticket

import "github.com/blessnduta/ticketing-system/internal/domain/shared"

// Impact answers "how much of the organisation is affected?" and Urgency
// answers "how fast does it degrade?". Both are set by people close to the
// problem; Priority is *derived* from them.
//
// This is the ITIL model and it is deliberately not a free-choice priority
// field. When agents pick priority directly, every ticket becomes High within
// a quarter and the queue ordering stops meaning anything. Forcing the two
// inputs and computing the output keeps priority comparable across teams.
type Impact string

const (
	ImpactLow    Impact = "low"    // one person
	ImpactMedium Impact = "medium" // a team or department
	ImpactHigh   Impact = "high"   // the whole organisation, or a customer-facing service
)

type Urgency string

const (
	UrgencyLow    Urgency = "low"    // a workaround exists
	UrgencyMedium Urgency = "medium" // degraded, work continues
	UrgencyHigh   Urgency = "high"   // work is stopped
)

// Priority is the derived queue-ordering value. P1 is most severe.
type Priority string

const (
	PriorityP1 Priority = "P1"
	PriorityP2 Priority = "P2"
	PriorityP3 Priority = "P3"
	PriorityP4 Priority = "P4"
)

// priorityMatrix is the derivation table, written out in full rather than
// computed from a score so that the business can read and amend it directly.
var priorityMatrix = map[Impact]map[Urgency]Priority{
	ImpactHigh: {
		UrgencyHigh:   PriorityP1,
		UrgencyMedium: PriorityP2,
		UrgencyLow:    PriorityP3,
	},
	ImpactMedium: {
		UrgencyHigh:   PriorityP2,
		UrgencyMedium: PriorityP3,
		UrgencyLow:    PriorityP4,
	},
	ImpactLow: {
		UrgencyHigh:   PriorityP3,
		UrgencyMedium: PriorityP4,
		UrgencyLow:    PriorityP4,
	},
}

// DerivePriority computes priority from impact and urgency. Unknown inputs are
// a programming error by the time they reach here — Impact and Urgency are
// validated at the parse boundary — so this fails closed to the least urgent
// value rather than panicking inside a request.
func DerivePriority(impact Impact, urgency Urgency) Priority {
	if byUrgency, ok := priorityMatrix[impact]; ok {
		if priority, ok := byUrgency[urgency]; ok {
			return priority
		}
	}
	return PriorityP4
}

// Rank returns a sortable weight, lowest first, so "most severe first" is a
// plain ORDER BY in the database and a plain numeric sort in the UI.
func (p Priority) Rank() int {
	switch p {
	case PriorityP1:
		return 1
	case PriorityP2:
		return 2
	case PriorityP3:
		return 3
	default:
		return 4
	}
}

func ParseImpact(raw string) (Impact, error) {
	switch Impact(raw) {
	case ImpactLow:
		return ImpactLow, nil
	case ImpactMedium:
		return ImpactMedium, nil
	case ImpactHigh:
		return ImpactHigh, nil
	default:
		return "", shared.Invalid("ticket.impact_invalid", "impact must be low, medium or high").
			WithDetail("impact", raw)
	}
}

func ParseUrgency(raw string) (Urgency, error) {
	switch Urgency(raw) {
	case UrgencyLow:
		return UrgencyLow, nil
	case UrgencyMedium:
		return UrgencyMedium, nil
	case UrgencyHigh:
		return UrgencyHigh, nil
	default:
		return "", shared.Invalid("ticket.urgency_invalid", "urgency must be low, medium or high").
			WithDetail("urgency", raw)
	}
}

func ParsePriority(raw string) (Priority, error) {
	switch Priority(raw) {
	case PriorityP1, PriorityP2, PriorityP3, PriorityP4:
		return Priority(raw), nil
	default:
		return "", shared.Invalid("ticket.priority_invalid", "priority must be P1, P2, P3 or P4").
			WithDetail("priority", raw)
	}
}
