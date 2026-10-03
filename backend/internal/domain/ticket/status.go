package ticket

import "github.com/blessnduta/ticketing-system/internal/domain/shared"

// Status is the ticket's lifecycle position. Statuses are a closed set with an
// explicit transition table — see statemachine.go. Free-text status fields are
// how ticketing systems end up with "Open", "open", "OPEN " and "Opened" in
// the same column and no usable reporting.
type Status string

const (
	// StatusNew is untriaged: it exists, nobody has classified it yet.
	StatusNew Status = "new"
	// StatusTriaged means impact/urgency confirmed and a team assigned, but
	// nobody has started. This is the queue agents pull from.
	StatusTriaged Status = "triaged"
	// StatusPendingApproval applies to changes awaiting an approval decision.
	StatusPendingApproval Status = "pending_approval"
	// StatusInProgress means someone owns it and is working now.
	StatusInProgress Status = "in_progress"
	// StatusPendingRequester means we are blocked on the requester. The SLA
	// clock pauses here — that is the whole reason this state exists
	// separately from StatusInProgress.
	StatusPendingRequester Status = "pending_requester"
	// StatusResolved means a fix is in place and the requester has been told.
	// Not yet terminal: the requester may reject it.
	StatusResolved Status = "resolved"
	// StatusClosed is terminal-by-agreement.
	StatusClosed Status = "closed"
	// StatusCancelled is terminal-without-work: withdrawn, duplicate, invalid.
	StatusCancelled Status = "cancelled"
)

// Terminal reports whether no further work is expected. Terminal tickets are
// excluded from queue counts and stop accruing SLA time.
func (s Status) Terminal() bool {
	return s == StatusClosed || s == StatusCancelled
}

// PausesSLA reports whether time spent in this status counts against the
// resolution target. Waiting on the requester, or on an approver, is not time
// the service desk can be held to.
func (s Status) PausesSLA() bool {
	return s == StatusPendingRequester || s == StatusPendingApproval
}

// Active reports whether the ticket is in an agent's working set.
func (s Status) Active() bool { return !s.Terminal() }

// SLAClockRunning reports whether the resolution deadline is still ticking.
//
// Three statuses stop it, for three different reasons:
//   - terminal — there is no more work to do;
//   - paused — we are blocked on someone outside the desk;
//   - resolved — the desk has done its part, and whether it made the target is
//     already settled in the ticket's ResolutionMet flag.
//
// That last one is easy to miss, because "resolved" is not terminal: a
// requester can still reject the fix. But a resolved ticket sitting past its
// deadline is not *breaching* — it was either met or missed at the moment it
// was resolved, and continuing to count it inflates every "how many are late
// right now?" figure on the dashboard.
func (s Status) SLAClockRunning() bool {
	return !s.Terminal() && !s.PausesSLA() && s != StatusResolved
}

func ParseStatus(raw string) (Status, error) {
	switch Status(raw) {
	case StatusNew, StatusTriaged, StatusPendingApproval, StatusInProgress,
		StatusPendingRequester, StatusResolved, StatusClosed, StatusCancelled:
		return Status(raw), nil
	default:
		return "", shared.Invalid("ticket.status_invalid", "unknown ticket status").
			WithDetail("status", raw)
	}
}

// AllStatuses is the canonical ordering used for board columns and for
// reporting groupings, so the UI never invents its own ordering.
var AllStatuses = []Status{
	StatusNew, StatusTriaged, StatusPendingApproval, StatusInProgress,
	StatusPendingRequester, StatusResolved, StatusClosed, StatusCancelled,
}
