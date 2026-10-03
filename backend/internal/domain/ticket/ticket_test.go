package ticket

import (
	"testing"
	"time"

	"github.com/blessnduta/ticketing-system/internal/domain/shared"
)

var baseTime = time.Date(2026, 3, 10, 9, 0, 0, 0, time.UTC)

func newTestTicket(t *testing.T, kind Kind, impact Impact, urgency Urgency) *Ticket {
	t.Helper()
	ticket, err := New(NewInput{
		OrgID:       shared.NewID(),
		Reference:   "TST-1",
		Kind:        kind,
		Subject:     "Something is broken",
		Description: "It stopped working this morning.",
		Impact:      impact,
		Urgency:     urgency,
		RequesterID: shared.NewID(),
	}, baseTime)
	if err != nil {
		t.Fatalf("constructing ticket: %v", err)
	}
	return ticket
}

func TestNew_DerivesPriorityAndDoesNotAcceptIt(t *testing.T) {
	// The whole point of the impact x urgency matrix is that priority is an
	// output. If it ever becomes an input, queue ordering stops being
	// comparable across teams.
	ticket := newTestTicket(t, KindIncident, ImpactHigh, UrgencyHigh)
	if ticket.Priority != PriorityP1 {
		t.Fatalf("high impact x high urgency must be P1, got %s", ticket.Priority)
	}
	if ticket.Status != StatusNew {
		t.Fatalf("a new ticket must start in 'new', got %s", ticket.Status)
	}
}

func TestNew_Validation(t *testing.T) {
	cases := []struct {
		name  string
		mutate func(*NewInput)
		code  string
	}{
		{"blank subject", func(in *NewInput) { in.Subject = "   " }, "ticket.subject_required"},
		{"blank description", func(in *NewInput) { in.Description = "" }, "ticket.description_required"},
		{"oversized subject", func(in *NewInput) { in.Subject = string(make([]byte, MaxSubjectLength+1)) }, "ticket.subject_too_long"},
		{"too many tags", func(in *NewInput) {
			for i := 0; i <= MaxTags; i++ {
				in.Tags = append(in.Tags, "tag")
			}
		}, "ticket.too_many_tags"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			input := NewInput{
				OrgID: shared.NewID(), Reference: "TST-1", Kind: KindIncident,
				Subject: "ok", Description: "ok", Impact: ImpactLow, Urgency: UrgencyLow,
				RequesterID: shared.NewID(),
			}
			tc.mutate(&input)
			_, err := New(input, baseTime)
			assertCode(t, err, tc.code)
		})
	}
}

func TestNew_NormalisesTags(t *testing.T) {
	ticket, err := New(NewInput{
		OrgID: shared.NewID(), Reference: "TST-1", Kind: KindIncident,
		Subject: "ok", Description: "ok", Impact: ImpactLow, Urgency: UrgencyLow,
		RequesterID: shared.NewID(),
		Tags:        []string{"  VPN ", "vpn", "Outage"},
	}, baseTime)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Lowercased, trimmed and de-duplicated — otherwise "VPN" and "vpn"
	// become two different tags and every tag filter under-reports.
	if len(ticket.Tags) != 2 || ticket.Tags[0] != "vpn" || ticket.Tags[1] != "outage" {
		t.Fatalf("expected [vpn outage], got %v", ticket.Tags)
	}
}

// TestSLAClockPausesAndResumes covers the accounting that makes SLA reporting
// honest: time spent waiting on the requester is not time the desk can be held
// to, so the deadline moves by exactly the paused span.
func TestSLAClockPausesAndResumes(t *testing.T) {
	ticket := newTestTicket(t, KindIncident, ImpactHigh, UrgencyHigh)
	originalDue := baseTime.Add(4 * time.Hour)
	ticket.ApplySLATargets(shared.NewID(), baseTime.Add(time.Hour), originalDue, baseTime)

	assigneeID := shared.NewID()
	mustTransition(t, ticket, StatusTriaged, baseTime)
	if err := ticket.Assign(&assigneeID, baseTime); err != nil {
		t.Fatalf("assign: %v", err)
	}
	mustTransition(t, ticket, StatusInProgress, baseTime.Add(10*time.Minute))

	pausedAt := baseTime.Add(30 * time.Minute)
	mustTransition(t, ticket, StatusPendingRequester, pausedAt)
	if ticket.PausedSince == nil {
		t.Fatal("moving to pending_requester must start the pause")
	}
	if !ticket.ResolutionDue.Equal(originalDue) {
		t.Fatal("pausing must not move the deadline — only resuming does")
	}

	// The requester takes 90 minutes to reply.
	resumedAt := pausedAt.Add(90 * time.Minute)
	mustTransition(t, ticket, StatusInProgress, resumedAt)

	if ticket.PausedSince != nil {
		t.Fatal("resuming must clear the pause marker")
	}
	wantDue := originalDue.Add(90 * time.Minute)
	if !ticket.ResolutionDue.Equal(wantDue) {
		t.Fatalf("deadline should have moved by the paused span: want %v, got %v",
			wantDue, *ticket.ResolutionDue)
	}
}

func TestBreachedAt_IgnoresPausedAndTerminalTickets(t *testing.T) {
	ticket := newTestTicket(t, KindIncident, ImpactHigh, UrgencyHigh)
	due := baseTime.Add(time.Hour)
	ticket.ApplySLATargets(shared.NewID(), baseTime, due, baseTime)
	afterDue := due.Add(time.Minute)

	assigneeID := shared.NewID()
	mustTransition(t, ticket, StatusTriaged, baseTime)
	_ = ticket.Assign(&assigneeID, baseTime)
	mustTransition(t, ticket, StatusInProgress, baseTime)

	if !ticket.BreachedAt(afterDue) {
		t.Fatal("an active ticket past its deadline is breaching")
	}

	mustTransition(t, ticket, StatusPendingRequester, baseTime.Add(time.Minute))
	if ticket.BreachedAt(afterDue) {
		t.Fatal("a paused ticket cannot be breaching — the clock is stopped")
	}

	mustTransition(t, ticket, StatusInProgress, baseTime.Add(2*time.Minute))
	mustTransitionWith(t, ticket, TransitionInput{To: StatusResolved, Resolution: strPtr("fixed")}, baseTime.Add(3*time.Minute))
	mustTransition(t, ticket, StatusClosed, baseTime.Add(4*time.Minute))
	if ticket.BreachedAt(afterDue) {
		t.Fatal("a closed ticket cannot be breaching")
	}
}

func TestRecordFirstResponse_IsIdempotent(t *testing.T) {
	ticket := newTestTicket(t, KindIncident, ImpactMedium, UrgencyMedium)
	ticket.ApplySLATargets(shared.NewID(), baseTime.Add(time.Hour), baseTime.Add(8*time.Hour), baseTime)

	first := baseTime.Add(30 * time.Minute)
	ticket.RecordFirstResponse(first)
	ticket.RecordFirstResponse(first.Add(3 * time.Hour))

	if !ticket.FirstRespAt.Equal(first) {
		t.Fatalf("first response must stay the first one: want %v, got %v", first, *ticket.FirstRespAt)
	}
	if ticket.FirstResponseMet == nil || !*ticket.FirstResponseMet {
		t.Fatal("responding within the target must record the SLA as met")
	}
}

func TestRecordFirstResponse_MarksMissedTarget(t *testing.T) {
	ticket := newTestTicket(t, KindIncident, ImpactMedium, UrgencyMedium)
	ticket.ApplySLATargets(shared.NewID(), baseTime.Add(time.Hour), baseTime.Add(8*time.Hour), baseTime)
	ticket.RecordFirstResponse(baseTime.Add(2 * time.Hour))

	if ticket.FirstResponseMet == nil || *ticket.FirstResponseMet {
		t.Fatal("responding after the target must record the SLA as missed")
	}
}

func TestReopenIncrementsCountAndClearsResolution(t *testing.T) {
	ticket := newTestTicket(t, KindIncident, ImpactLow, UrgencyLow)
	assigneeID := shared.NewID()
	mustTransition(t, ticket, StatusTriaged, baseTime)
	_ = ticket.Assign(&assigneeID, baseTime)
	mustTransition(t, ticket, StatusInProgress, baseTime)
	mustTransitionWith(t, ticket, TransitionInput{To: StatusResolved, Resolution: strPtr("restarted it")}, baseTime)

	if ticket.ResolvedAt == nil {
		t.Fatal("resolving must stamp resolved_at")
	}

	mustTransition(t, ticket, StatusTriaged, baseTime.Add(time.Hour))
	if ticket.ReopenCount != 1 {
		t.Fatalf("reopening must increment the counter, got %d", ticket.ReopenCount)
	}
	if ticket.ResolvedAt != nil || ticket.ResolutionMet != nil {
		t.Fatal("reopening must clear the resolution outcome — it was not actually resolved")
	}
}

func TestRouteClearsAssignee(t *testing.T) {
	ticket := newTestTicket(t, KindIncident, ImpactLow, UrgencyLow)
	assigneeID := shared.NewID()
	_ = ticket.Assign(&assigneeID, baseTime)

	newTeam := shared.NewID()
	if err := ticket.Route(&newTeam, baseTime); err != nil {
		t.Fatalf("route: %v", err)
	}
	// The previous owner is on the wrong team by construction; leaving them
	// assigned would show the ticket as owned when nobody on the new team has
	// picked it up.
	if ticket.AssigneeID != nil {
		t.Fatal("routing to another team must clear the assignee")
	}
}

func TestTerminalTicketsRejectMutation(t *testing.T) {
	ticket := newTestTicket(t, KindIncident, ImpactLow, UrgencyLow)
	mustTransition(t, ticket, StatusCancelled, baseTime)

	assigneeID := shared.NewID()
	if err := ticket.Assign(&assigneeID, baseTime); err == nil {
		t.Error("a cancelled ticket must not be assignable")
	}
	if err := ticket.Reclassify(ImpactHigh, UrgencyHigh, baseTime); err == nil {
		t.Error("a cancelled ticket must not be reclassifiable")
	}
}

func TestReclassifyRederivesPriority(t *testing.T) {
	ticket := newTestTicket(t, KindIncident, ImpactLow, UrgencyLow)
	if ticket.Priority != PriorityP4 {
		t.Fatalf("expected P4, got %s", ticket.Priority)
	}
	if err := ticket.Reclassify(ImpactHigh, UrgencyHigh, baseTime); err != nil {
		t.Fatalf("reclassify: %v", err)
	}
	if ticket.Priority != PriorityP1 {
		t.Fatalf("reclassifying must re-derive priority, got %s", ticket.Priority)
	}
}

func TestTransitionRejectsBlankResolution(t *testing.T) {
	ticket := newTestTicket(t, KindIncident, ImpactLow, UrgencyLow)
	assigneeID := shared.NewID()
	mustTransition(t, ticket, StatusTriaged, baseTime)
	_ = ticket.Assign(&assigneeID, baseTime)
	mustTransition(t, ticket, StatusInProgress, baseTime)

	err := ticket.Transition(TransitionInput{To: StatusResolved, Resolution: strPtr("   ")}, baseTime)
	assertCode(t, err, "ticket.resolution_blank")
}

// --- helpers ---------------------------------------------------------------

func mustTransition(t *testing.T, ticket *Ticket, to Status, at time.Time) {
	t.Helper()
	mustTransitionWith(t, ticket, TransitionInput{To: to, ApprovalGranted: true}, at)
}

func mustTransitionWith(t *testing.T, ticket *Ticket, in TransitionInput, at time.Time) {
	t.Helper()
	in.ApprovalGranted = true
	if err := ticket.Transition(in, at); err != nil {
		t.Fatalf("transition to %s at %v: %v", in.To, at, err)
	}
}

func strPtr(s string) *string { return &s }
