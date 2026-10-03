package ticket

import (
	"sort"

	"github.com/blessnduta/ticketing-system/internal/domain/shared"
)

// The state machine is the conceptual heart of the system, so it gets an
// explicit table rather than scattered `if status == ...` checks in handlers.
// Two properties matter:
//
//  1. It is data, so it can be read, tested exhaustively, and shipped to the
//     frontend (see AllowedFrom) — the UI greys out impossible buttons using
//     the same table the server enforces, instead of duplicating the rules.
//  2. It is keyed by (kind, from), so a change request and an incident can
//     have genuinely different lifecycles without a second machine.

// transition is an edge with the guard conditions attached to it. Keeping
// preconditions on the edge — rather than in the caller — means every path
// into a status is subject to the same rules.
type transition struct {
	to Status
	// requiresResolution demands a resolution note before entering the state.
	// A "resolved" ticket with no explanation is the single most common way a
	// ticket queue loses its institutional memory.
	requiresResolution bool
	// requiresAssignee demands somebody own the ticket first. You cannot be
	// "in progress" if nobody is doing it.
	requiresAssignee bool
	// requiresApprovalGranted demands the approval gate be satisfied.
	requiresApprovalGranted bool
}

// baseTransitions applies to every ticket kind.
var baseTransitions = map[Status][]transition{
	StatusNew: {
		{to: StatusTriaged},
		{to: StatusCancelled},
	},
	StatusTriaged: {
		{to: StatusInProgress, requiresAssignee: true},
		{to: StatusPendingRequester},
		{to: StatusCancelled},
	},
	StatusInProgress: {
		{to: StatusPendingRequester},
		{to: StatusResolved, requiresResolution: true},
		{to: StatusTriaged}, // handed back to the queue
		{to: StatusCancelled},
	},
	StatusPendingRequester: {
		{to: StatusInProgress, requiresAssignee: true},
		{to: StatusResolved, requiresResolution: true},
		{to: StatusCancelled},
	},
	StatusResolved: {
		{to: StatusClosed},
		// Reopening: the requester said it is not actually fixed. Goes back to
		// triaged rather than in_progress because the original assignee may no
		// longer be the right owner.
		{to: StatusTriaged},
	},
	StatusClosed: {
		// A closed ticket may be reopened, which restarts triage. Whether this
		// is allowed at all is a policy question; the reopen *window* is
		// enforced by the application layer, not here, because it depends on
		// the clock.
		{to: StatusTriaged},
	},
	StatusCancelled: {}, // terminal, no way back — raise a new ticket
	StatusPendingApproval: {
		{to: StatusInProgress, requiresAssignee: true, requiresApprovalGranted: true},
		{to: StatusCancelled}, // approval rejected
	},
}

// changeOverrides replace the base edges for change requests. A change cannot
// go from triaged straight to in_progress: it must pass the approval gate.
// This one override is the difference between "a helpdesk" and "change
// management".
var changeOverrides = map[Status][]transition{
	StatusTriaged: {
		{to: StatusPendingApproval},
		{to: StatusCancelled},
	},
}

// TransitionContext carries the facts the guards need. It is passed in rather
// than read off the ticket so the machine can be evaluated speculatively —
// which is exactly what AllowedFrom does to build the UI's button list.
type TransitionContext struct {
	HasAssignee      bool
	HasResolution    bool
	ApprovalGranted  bool
}

func edgesFor(kind Kind, from Status) []transition {
	if kind == KindChange {
		if overridden, ok := changeOverrides[from]; ok {
			return overridden
		}
	}
	return baseTransitions[from]
}

// CanTransition reports whether the move is legal, and if not, why. Returning
// the reason rather than a bare bool is what lets the API tell an agent
// "resolve requires a resolution note" instead of a generic 422.
func CanTransition(kind Kind, from, to Status, ctx TransitionContext) error {
	if from == to {
		return shared.RuleViolation("ticket.transition_noop", "the ticket is already in that status").
			WithDetail("status", string(to))
	}

	for _, edge := range edgesFor(kind, from) {
		if edge.to != to {
			continue
		}
		switch {
		case edge.requiresAssignee && !ctx.HasAssignee:
			return shared.RuleViolation("ticket.assignee_required",
				"assign the ticket to someone before moving it to this status").
				WithDetail("from", string(from)).WithDetail("to", string(to))
		case edge.requiresResolution && !ctx.HasResolution:
			return shared.RuleViolation("ticket.resolution_required",
				"a resolution note is required to resolve a ticket").
				WithDetail("from", string(from)).WithDetail("to", string(to))
		case edge.requiresApprovalGranted && !ctx.ApprovalGranted:
			return shared.RuleViolation("ticket.approval_required",
				"all required approvals must be granted before work can start").
				WithDetail("from", string(from)).WithDetail("to", string(to))
		}
		return nil
	}

	return shared.RuleViolation("ticket.illegal_transition",
		"that status change is not allowed from the ticket's current status").
		WithDetail("from", string(from)).
		WithDetail("to", string(to)).
		WithDetail("allowed", statusStrings(reachableFrom(kind, from)))
}

// AllowedFrom returns the statuses reachable right now given the context.
// Shipped to the frontend on every ticket read so the UI renders exactly the
// buttons the server would accept — one source of truth, two consumers.
func AllowedFrom(kind Kind, from Status, ctx TransitionContext) []Status {
	var allowed []Status
	for _, edge := range edgesFor(kind, from) {
		if edge.requiresAssignee && !ctx.HasAssignee {
			continue
		}
		if edge.requiresResolution && !ctx.HasResolution {
			continue
		}
		if edge.requiresApprovalGranted && !ctx.ApprovalGranted {
			continue
		}
		allowed = append(allowed, edge.to)
	}
	sortStatuses(allowed)
	return allowed
}

// reachableFrom lists every edge target ignoring guards — used in error
// details so the client can see the shape of the machine, not just the subset
// currently unlocked.
func reachableFrom(kind Kind, from Status) []Status {
	edges := edgesFor(kind, from)
	out := make([]Status, 0, len(edges))
	for _, edge := range edges {
		out = append(out, edge.to)
	}
	sortStatuses(out)
	return out
}

func sortStatuses(statuses []Status) {
	order := make(map[Status]int, len(AllStatuses))
	for i, s := range AllStatuses {
		order[s] = i
	}
	sort.Slice(statuses, func(i, j int) bool { return order[statuses[i]] < order[statuses[j]] })
}

func statusStrings(statuses []Status) []string {
	out := make([]string, len(statuses))
	for i, s := range statuses {
		out[i] = string(s)
	}
	return out
}
