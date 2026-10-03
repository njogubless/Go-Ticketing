package ticket

import "github.com/blessnduta/ticketing-system/internal/domain/shared"

// Kind is the ITSM work type. It is not cosmetic: it changes which state
// transitions are legal, whether an approval gate applies, and which SLA
// policy is selected.
//
// Named Kind rather than Type because `ticket.Type` reads badly at call sites
// and collides with Go's own vocabulary.
type Kind string

const (
	// KindIncident is an unplanned interruption to a service. Goal: restore
	// service fast, even with a workaround.
	KindIncident Kind = "incident"
	// KindServiceRequest is a routine, pre-approved ask — new laptop, access
	// to a system, a password reset.
	KindServiceRequest Kind = "service_request"
	// KindChange is a modification to the environment. Gated behind approval;
	// this is the distinction that makes a system ITSM rather than a helpdesk.
	KindChange Kind = "change"
	// KindProblem is the underlying cause behind one or more incidents.
	// Longer-lived, usually no customer waiting on it.
	KindProblem Kind = "problem"
)

// RequiresApproval reports whether tickets of this kind must pass through an
// approval gate before work may begin.
func (k Kind) RequiresApproval() bool { return k == KindChange }

func ParseKind(raw string) (Kind, error) {
	switch Kind(raw) {
	case KindIncident, KindServiceRequest, KindChange, KindProblem:
		return Kind(raw), nil
	default:
		return "", shared.Invalid("ticket.kind_invalid",
			"kind must be incident, service_request, change or problem").WithDetail("kind", raw)
	}
}
