package sla

import (
	"testing"
	"time"

	"github.com/blessnduta/ticketing-system/internal/domain/shared"
	"github.com/blessnduta/ticketing-system/internal/domain/ticket"
)

func testTicket(t *testing.T, priority ticket.Priority, kind ticket.Kind, teamID *shared.ID) *ticket.Ticket {
	t.Helper()
	impact, urgency := ticket.ImpactHigh, ticket.UrgencyHigh
	switch priority {
	case ticket.PriorityP2:
		impact, urgency = ticket.ImpactMedium, ticket.UrgencyHigh
	case ticket.PriorityP3:
		impact, urgency = ticket.ImpactMedium, ticket.UrgencyMedium
	case ticket.PriorityP4:
		impact, urgency = ticket.ImpactLow, ticket.UrgencyLow
	}

	created, err := ticket.New(ticket.NewInput{
		OrgID: shared.NewID(), Reference: "TST-1", Kind: kind,
		Subject: "s", Description: "d", Impact: impact, Urgency: urgency,
		RequesterID: shared.NewID(), TeamID: teamID,
	}, time.Now().UTC())
	if err != nil {
		t.Fatalf("building ticket: %v", err)
	}
	return created
}

func policy(name string, priority *ticket.Priority, kind *ticket.Kind, teamID *shared.ID) *Policy {
	return &Policy{
		ID: shared.NewID(), Name: name, Priority: priority, Kind: kind, TeamID: teamID,
		FirstResponse: time.Hour, Resolution: 4 * time.Hour,
		CalendarID: shared.NewID(), Active: true,
	}
}

func TestSelect_PrefersTheMostSpecificPolicy(t *testing.T) {
	teamID := shared.NewID()
	p1 := ticket.PriorityP1
	incident := ticket.KindIncident

	catchAll := policy("catch-all", nil, nil, nil)
	byPriority := policy("by priority", &p1, nil, nil)
	byPriorityAndKind := policy("by priority and kind", &p1, &incident, nil)
	fullyQualified := policy("fully qualified", &p1, &incident, &teamID)

	target := testTicket(t, ticket.PriorityP1, ticket.KindIncident, &teamID)

	// Deliberately unsorted input: selection must not depend on input order,
	// or two replicas could give the same ticket different deadlines.
	selected := Select([]*Policy{catchAll, fullyQualified, byPriority, byPriorityAndKind}, target)
	if selected == nil || selected.Name != "fully qualified" {
		t.Fatalf("expected the most specific policy, got %v", selected)
	}
}

func TestSelect_FallsBackToLessSpecific(t *testing.T) {
	otherTeam := shared.NewID()
	p1 := ticket.PriorityP1

	catchAll := policy("catch-all", nil, nil, nil)
	otherTeamPolicy := policy("other team", &p1, nil, &otherTeam)

	target := testTicket(t, ticket.PriorityP1, ticket.KindIncident, nil)
	selected := Select([]*Policy{otherTeamPolicy, catchAll}, target)
	if selected == nil || selected.Name != "catch-all" {
		t.Fatalf("expected the catch-all, got %v", selected)
	}
}

func TestSelect_ReturnsNilWhenNothingMatches(t *testing.T) {
	p1 := ticket.PriorityP1
	target := testTicket(t, ticket.PriorityP4, ticket.KindIncident, nil)

	// A ticket with no matching policy gets no SLA rather than an invented
	// one — a fabricated deadline would make every attainment figure fiction.
	if selected := Select([]*Policy{policy("p1 only", &p1, nil, nil)}, target); selected != nil {
		t.Fatalf("expected no policy to match, got %q", selected.Name)
	}
}

func TestSelect_IgnoresInactivePolicies(t *testing.T) {
	p1 := ticket.PriorityP1
	inactive := policy("retired", &p1, nil, nil)
	inactive.Active = false

	target := testTicket(t, ticket.PriorityP1, ticket.KindIncident, nil)
	if selected := Select([]*Policy{inactive}, target); selected != nil {
		t.Fatal("an inactive policy must never be selected")
	}
}

func TestSelect_IsDeterministicOnTies(t *testing.T) {
	p1 := ticket.PriorityP1
	alpha := policy("alpha", &p1, nil, nil)
	beta := policy("beta", &p1, nil, nil)
	target := testTicket(t, ticket.PriorityP1, ticket.KindIncident, nil)

	// Equally specific policies are broken by name, so the outcome is stable
	// across restarts and replicas — an agent must be able to explain why a
	// ticket got the deadline it got.
	for _, input := range [][]*Policy{{alpha, beta}, {beta, alpha}} {
		if selected := Select(input, target); selected.Name != "alpha" {
			t.Fatalf("tie-break must be deterministic by name, got %q", selected.Name)
		}
	}
}

func TestNewPolicy_RejectsInconsistentTargets(t *testing.T) {
	orgID, calendarID := shared.NewID(), shared.NewID()
	now := time.Now().UTC()

	// A first-response target longer than the resolution target is
	// nonsensical and would silently produce a due date after the deadline.
	_, err := NewPolicy(orgID, "backwards", calendarID, 8*time.Hour, 4*time.Hour, now)
	if err == nil {
		t.Fatal("expected inconsistent targets to be rejected")
	}

	if _, err := NewPolicy(orgID, "", calendarID, time.Hour, 4*time.Hour, now); err == nil {
		t.Fatal("expected a blank name to be rejected")
	}
	if _, err := NewPolicy(orgID, "zero", calendarID, 0, 4*time.Hour, now); err == nil {
		t.Fatal("expected a zero first-response target to be rejected")
	}
}

func TestComputeTargets_UsesBusinessHours(t *testing.T) {
	calendar := businessHours(t, "UTC")
	p := policy("p3", nil, nil, nil)
	p.FirstResponse = 4 * time.Hour
	p.Resolution = 12 * time.Hour

	from := time.Date(2026, 3, 13, 16, 0, 0, 0, time.UTC) // Friday 16:00
	targets, err := p.ComputeTargets(calendar, from)
	if err != nil {
		t.Fatalf("compute targets: %v", err)
	}

	// 4 working hours from Friday 16:00 -> Monday 12:00.
	assertTime(t, time.Date(2026, 3, 16, 12, 0, 0, 0, time.UTC), targets.FirstResponseDue)
	// 12 working hours -> 1h Friday + 8h Monday + 3h Tuesday = Tuesday 12:00.
	assertTime(t, time.Date(2026, 3, 17, 12, 0, 0, 0, time.UTC), targets.ResolutionDue)
}

func TestDefaultPolicies_CoverEveryPriority(t *testing.T) {
	orgID := shared.NewID()
	alwaysOn, businessHoursID := shared.NewID(), shared.NewID()
	policies := DefaultPolicies(orgID, alwaysOn, businessHoursID, time.Now().UTC())

	covered := map[ticket.Priority]bool{}
	for _, p := range policies {
		if p.Priority == nil {
			t.Fatal("default policies must each target a priority")
		}
		covered[*p.Priority] = true
		if p.FirstResponse > p.Resolution {
			t.Fatalf("policy %q has a first-response target longer than its resolution target", p.Name)
		}
	}

	for _, priority := range []ticket.Priority{ticket.PriorityP1, ticket.PriorityP2, ticket.PriorityP3, ticket.PriorityP4} {
		if !covered[priority] {
			t.Errorf("no default policy covers %s — those tickets would have no SLA", priority)
		}
	}

	// P1 must be on the 24/7 calendar: a critical outage cannot wait for
	// Monday morning.
	for _, p := range policies {
		if *p.Priority == ticket.PriorityP1 && p.CalendarID != alwaysOn {
			t.Error("the P1 policy must use the 24/7 calendar")
		}
	}
}
