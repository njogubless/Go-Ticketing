package ticket

import (
	"testing"

	"github.com/blessnduta/ticketing-system/internal/domain/shared"
)

// The state machine and the permission boundaries are the two highest-risk
// areas in the system, so they get the densest tests. A bug here either loses
// work (a ticket stuck in an unreachable state) or lets a change bypass its
// approval gate.

func satisfied() TransitionContext {
	return TransitionContext{HasAssignee: true, HasResolution: true, ApprovalGranted: true}
}

func TestCanTransition_LegalPaths(t *testing.T) {
	cases := []struct {
		name string
		kind Kind
		from Status
		to   Status
	}{
		{"new to triaged", KindIncident, StatusNew, StatusTriaged},
		{"triaged to in progress", KindIncident, StatusTriaged, StatusInProgress},
		{"in progress to resolved", KindIncident, StatusInProgress, StatusResolved},
		{"resolved to closed", KindIncident, StatusResolved, StatusClosed},
		{"resolved reopens to triaged", KindIncident, StatusResolved, StatusTriaged},
		{"closed reopens to triaged", KindIncident, StatusClosed, StatusTriaged},
		{"in progress to pending requester", KindIncident, StatusInProgress, StatusPendingRequester},
		{"pending requester back to in progress", KindIncident, StatusPendingRequester, StatusInProgress},
		{"in progress hands back to queue", KindIncident, StatusInProgress, StatusTriaged},
		{"change triaged to pending approval", KindChange, StatusTriaged, StatusPendingApproval},
		{"approved change starts work", KindChange, StatusPendingApproval, StatusInProgress},
		{"rejected change is cancelled", KindChange, StatusPendingApproval, StatusCancelled},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := CanTransition(tc.kind, tc.from, tc.to, satisfied()); err != nil {
				t.Fatalf("expected %s -> %s to be legal for %s, got: %v", tc.from, tc.to, tc.kind, err)
			}
		})
	}
}

func TestCanTransition_IllegalPaths(t *testing.T) {
	cases := []struct {
		name string
		kind Kind
		from Status
		to   Status
	}{
		// The canonical illegal move: a ticket cannot be resolved before
		// anybody has looked at it.
		{"new straight to resolved", KindIncident, StatusNew, StatusResolved},
		{"new straight to closed", KindIncident, StatusNew, StatusClosed},
		{"triaged straight to resolved", KindIncident, StatusTriaged, StatusResolved},
		// Cancelled is a dead end by design — reopening a cancelled ticket
		// would blur the distinction between "we did nothing" and "we did
		// something and it is now done".
		{"cancelled cannot reopen", KindIncident, StatusCancelled, StatusTriaged},
		{"cancelled cannot go in progress", KindIncident, StatusCancelled, StatusInProgress},
		// The approval gate: this single case is what makes the system do
		// change management rather than just ticketing.
		{"change cannot skip approval", KindChange, StatusTriaged, StatusInProgress},
		// Only changes may enter the approval gate.
		{"incident cannot enter approval", KindIncident, StatusTriaged, StatusPendingApproval},
		{"closed cannot go straight to resolved", KindIncident, StatusClosed, StatusResolved},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := CanTransition(tc.kind, tc.from, tc.to, satisfied())
			if err == nil {
				t.Fatalf("expected %s -> %s to be illegal for %s", tc.from, tc.to, tc.kind)
			}
			if shared.KindOf(err) != shared.KindRuleViolation {
				t.Fatalf("expected a rule violation, got kind %q", shared.KindOf(err))
			}
		})
	}
}

func TestCanTransition_GuardsAreEnforced(t *testing.T) {
	t.Run("in progress requires an assignee", func(t *testing.T) {
		ctx := satisfied()
		ctx.HasAssignee = false
		err := CanTransition(KindIncident, StatusTriaged, StatusInProgress, ctx)
		assertCode(t, err, "ticket.assignee_required")
	})

	t.Run("resolve requires a resolution note", func(t *testing.T) {
		ctx := satisfied()
		ctx.HasResolution = false
		err := CanTransition(KindIncident, StatusInProgress, StatusResolved, ctx)
		assertCode(t, err, "ticket.resolution_required")
	})

	t.Run("change work requires granted approval", func(t *testing.T) {
		ctx := satisfied()
		ctx.ApprovalGranted = false
		err := CanTransition(KindChange, StatusPendingApproval, StatusInProgress, ctx)
		assertCode(t, err, "ticket.approval_required")
	})
}

func TestCanTransition_SameStatusIsRejected(t *testing.T) {
	err := CanTransition(KindIncident, StatusInProgress, StatusInProgress, satisfied())
	assertCode(t, err, "ticket.transition_noop")
}

// TestAllowedFrom_MatchesCanTransition is the important one: the server ships
// AllowedFrom to the frontend so the UI can render exactly the buttons that
// will be accepted. If the two ever disagree, users get buttons that fail or
// lose buttons that would have worked — and nothing would catch it but this.
func TestAllowedFrom_MatchesCanTransition(t *testing.T) {
	contexts := map[string]TransitionContext{
		"all guards satisfied": satisfied(),
		"no assignee":          {HasResolution: true, ApprovalGranted: true},
		"no resolution":        {HasAssignee: true, ApprovalGranted: true},
		"no approval":          {HasAssignee: true, HasResolution: true},
		"nothing satisfied":    {},
	}

	for _, kind := range []Kind{KindIncident, KindServiceRequest, KindChange, KindProblem} {
		for ctxName, ctx := range contexts {
			for _, from := range AllStatuses {
				allowed := map[Status]bool{}
				for _, to := range AllowedFrom(kind, from, ctx) {
					allowed[to] = true
				}

				for _, to := range AllStatuses {
					if from == to {
						continue
					}
					canDo := CanTransition(kind, from, to, ctx) == nil
					if canDo != allowed[to] {
						t.Errorf("kind=%s ctx=%q %s -> %s: CanTransition says %v but AllowedFrom says %v",
							kind, ctxName, from, to, canDo, allowed[to])
					}
				}
			}
		}
	}
}

// TestEveryStatusIsReachable guards against a status that can be defined but
// never entered — dead code in the most consequential enum in the system.
func TestEveryStatusIsReachable(t *testing.T) {
	reachable := map[Status]bool{StatusNew: true}

	for _, kind := range []Kind{KindIncident, KindChange} {
		for _, from := range AllStatuses {
			for _, to := range AllowedFrom(kind, from, satisfied()) {
				reachable[to] = true
			}
		}
	}

	for _, status := range AllStatuses {
		if !reachable[status] {
			t.Errorf("status %q is unreachable from any other status", status)
		}
	}
}

func TestIllegalTransitionErrorListsAlternatives(t *testing.T) {
	err := CanTransition(KindIncident, StatusNew, StatusResolved, satisfied())
	domainErr, ok := shared.AsError(err)
	if !ok {
		t.Fatalf("expected a domain error, got %T", err)
	}
	// The error carries the legal alternatives so a client can tell the user
	// what they *can* do, not merely what they cannot.
	allowed, ok := domainErr.Details["allowed"].([]string)
	if !ok || len(allowed) == 0 {
		t.Fatalf("expected the error to list allowed transitions, got %#v", domainErr.Details)
	}
}

func TestStatusClassification(t *testing.T) {
	if !StatusClosed.Terminal() || !StatusCancelled.Terminal() {
		t.Error("closed and cancelled must be terminal")
	}
	if StatusResolved.Terminal() {
		t.Error("resolved must not be terminal — the requester can still reject it")
	}
	if !StatusPendingRequester.PausesSLA() || !StatusPendingApproval.PausesSLA() {
		t.Error("waiting on a requester or an approver must pause the SLA clock")
	}
	if StatusInProgress.PausesSLA() {
		t.Error("in_progress must not pause the SLA clock")
	}
}

func assertCode(t *testing.T, err error, wantCode string) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected error with code %q, got nil", wantCode)
	}
	domainErr, ok := shared.AsError(err)
	if !ok {
		t.Fatalf("expected a domain error, got %T: %v", err, err)
	}
	if domainErr.Code != wantCode {
		t.Fatalf("expected code %q, got %q", wantCode, domainErr.Code)
	}
}
