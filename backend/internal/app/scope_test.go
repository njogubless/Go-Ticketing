package app

import (
	"testing"
	"time"

	"github.com/blessnduta/ticketing-system/internal/domain/identity"
	"github.com/blessnduta/ticketing-system/internal/domain/shared"
	"github.com/blessnduta/ticketing-system/internal/domain/ticket"
)

// Row-level visibility is the most security-critical logic in the system: a
// mistake here shows one requester another requester's tickets, or one
// organisation another organisation's.
//
// The two mechanisms — ticketScopeFor (the query filter) and
// authoriseTicketAccess (the per-ticket check) — must agree exactly. A ticket
// that would not appear in a list must not be reachable by guessing its ID.
// The final test in this file asserts that agreement directly.

type scenario struct {
	orgA, orgB     shared.ID
	teamA, teamB   shared.ID
	requester      identity.Actor
	otherRequester identity.Actor
	agentA         identity.Actor
	agentB         identity.Actor
	manager        identity.Actor
	admin          identity.Actor
	foreignAdmin   identity.Actor
}

func newScenario() scenario {
	orgA, orgB := shared.NewID(), shared.NewID()
	teamA, teamB := shared.NewID(), shared.NewID()
	return scenario{
		orgA: orgA, orgB: orgB, teamA: teamA, teamB: teamB,
		requester:      identity.Actor{UserID: shared.NewID(), OrgID: orgA, Role: identity.RoleRequester},
		otherRequester: identity.Actor{UserID: shared.NewID(), OrgID: orgA, Role: identity.RoleRequester},
		agentA:         identity.Actor{UserID: shared.NewID(), OrgID: orgA, Role: identity.RoleAgent, TeamID: &teamA},
		agentB:         identity.Actor{UserID: shared.NewID(), OrgID: orgA, Role: identity.RoleAgent, TeamID: &teamB},
		manager:        identity.Actor{UserID: shared.NewID(), OrgID: orgA, Role: identity.RoleManager},
		admin:          identity.Actor{UserID: shared.NewID(), OrgID: orgA, Role: identity.RoleAdmin},
		foreignAdmin:   identity.Actor{UserID: shared.NewID(), OrgID: orgB, Role: identity.RoleAdmin},
	}
}

func makeTicket(orgID, requesterID shared.ID, teamID, assigneeID *shared.ID) *ticket.Ticket {
	return &ticket.Ticket{
		ID: shared.NewID(), OrgID: orgID, Reference: "TST-1",
		Kind: ticket.KindIncident, Subject: "s", Description: "d",
		Status: ticket.StatusTriaged, Impact: ticket.ImpactLow, Urgency: ticket.UrgencyLow,
		Priority: ticket.PriorityP4, RequesterID: requesterID,
		TeamID: teamID, AssigneeID: assigneeID,
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}
}

func TestAuthoriseTicketAccess_CrossTenantIsAlwaysDenied(t *testing.T) {
	s := newScenario()
	// Even an admin — the most privileged role there is — cannot reach across
	// the organisation boundary.
	foreign := makeTicket(s.orgA, s.requester.UserID, &s.teamA, nil)
	if err := authoriseTicketAccess(s.foreignAdmin, foreign); err == nil {
		t.Fatal("an admin of another organisation must not access this ticket")
	}
}

func TestAuthoriseTicketAccess_DeniedLooksLikeNotFound(t *testing.T) {
	s := newScenario()
	other := makeTicket(s.orgA, s.otherRequester.UserID, &s.teamA, nil)

	err := authoriseTicketAccess(s.requester, other)
	if err == nil {
		t.Fatal("a requester must not read another requester's ticket")
	}
	// Not-found rather than forbidden: replying "forbidden" confirms the
	// ticket exists, turning the ID space into an enumeration oracle.
	if shared.KindOf(err) != shared.KindNotFound {
		t.Fatalf("denial must present as not-found, got %q", shared.KindOf(err))
	}
}

func TestAuthoriseTicketAccess_Matrix(t *testing.T) {
	s := newScenario()

	ownTicket := makeTicket(s.orgA, s.requester.UserID, &s.teamA, nil)
	otherTicket := makeTicket(s.orgA, s.otherRequester.UserID, &s.teamA, nil)
	teamBTicket := makeTicket(s.orgA, s.otherRequester.UserID, &s.teamB, nil)
	unroutedTicket := makeTicket(s.orgA, s.otherRequester.UserID, nil, nil)
	assignedAwayTicket := makeTicket(s.orgA, s.otherRequester.UserID, &s.teamB, &s.agentA.UserID)

	cases := []struct {
		name   string
		actor  identity.Actor
		ticket *ticket.Ticket
		allow  bool
	}{
		{"requester reads own", s.requester, ownTicket, true},
		{"requester cannot read another's", s.requester, otherTicket, false},
		{"requester cannot read unrouted", s.requester, unroutedTicket, false},

		{"agent reads own team's queue", s.agentA, otherTicket, true},
		{"agent cannot read another team's", s.agentA, teamBTicket, false},
		{"agent reads unrouted tickets", s.agentA, unroutedTicket, true},
		// An agent who owns a ticket keeps access after it is routed away,
		// otherwise handing over erases the previous owner's ability to answer
		// questions about their own work.
		{"agent keeps access to a ticket assigned to them", s.agentA, assignedAwayTicket, true},
		{"agent of another team reads their own queue", s.agentB, teamBTicket, true},
		{"agent reads a ticket they raised", s.agentB, makeTicket(s.orgA, s.agentB.UserID, &s.teamA, nil), true},

		{"manager reads everything in the org", s.manager, teamBTicket, true},
		{"admin reads everything in the org", s.admin, teamBTicket, true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := authoriseTicketAccess(tc.actor, tc.ticket)
			if tc.allow && err != nil {
				t.Fatalf("expected access to be allowed, got: %v", err)
			}
			if !tc.allow && err == nil {
				t.Fatal("expected access to be denied")
			}
		})
	}
}

func TestTicketScopeFor(t *testing.T) {
	s := newScenario()

	t.Run("requester is limited to their own tickets", func(t *testing.T) {
		scope := ticketScopeFor(s.requester)
		if scope.All {
			t.Fatal("a requester must never get an unfiltered scope")
		}
		if scope.RequesterID == nil || *scope.RequesterID != s.requester.UserID {
			t.Fatal("scope must pin the requester id")
		}
		if scope.TeamID != nil || scope.IncludeUnrouted {
			t.Fatal("a requester must not see team queues or unrouted tickets")
		}
		if scope.CanReadInternal {
			t.Fatal("a requester must never be allowed to read internal notes")
		}
	})

	t.Run("agent gets their team plus unrouted plus their own", func(t *testing.T) {
		scope := ticketScopeFor(s.agentA)
		if scope.All {
			t.Fatal("an agent must not get an unfiltered scope")
		}
		if scope.TeamID == nil || *scope.TeamID != s.teamA {
			t.Fatal("scope must pin the agent's team")
		}
		if !scope.IncludeUnrouted {
			t.Fatal("agents must see unrouted tickets, or new tickets are invisible to everyone")
		}
		if !scope.CanReadInternal {
			t.Fatal("agents must be able to read internal notes")
		}
	})

	t.Run("manager and admin see the whole organisation", func(t *testing.T) {
		for _, actor := range []identity.Actor{s.manager, s.admin} {
			scope := ticketScopeFor(actor)
			if !scope.All {
				t.Fatalf("%s must see the whole organisation", actor.Role)
			}
			if scope.OrgID != actor.OrgID {
				t.Fatal("scope must still be pinned to the actor's organisation")
			}
		}
	})

	t.Run("scope is always pinned to the actor's organisation", func(t *testing.T) {
		for _, actor := range []identity.Actor{s.requester, s.agentA, s.manager, s.admin} {
			if got := ticketScopeFor(actor).OrgID; got != actor.OrgID {
				t.Fatalf("scope org %v does not match actor org %v", got, actor.OrgID)
			}
		}
	})
}

// TestScopeAndAccessCheckAgree is the test that matters most in this file.
//
// It simulates the query filter in Go and asserts that, for every combination
// of actor and ticket, "would this row come back from a list?" gives the same
// answer as "may this actor open this ticket by ID?". A divergence in either
// direction is a bug: one way leaks data, the other hides a user's own work.
func TestScopeAndAccessCheckAgree(t *testing.T) {
	s := newScenario()

	actors := []identity.Actor{s.requester, s.otherRequester, s.agentA, s.agentB, s.manager, s.admin, s.foreignAdmin}
	tickets := []*ticket.Ticket{
		makeTicket(s.orgA, s.requester.UserID, &s.teamA, nil),
		makeTicket(s.orgA, s.otherRequester.UserID, &s.teamB, nil),
		makeTicket(s.orgA, s.otherRequester.UserID, nil, nil),
		makeTicket(s.orgA, s.requester.UserID, &s.teamB, &s.agentA.UserID),
		makeTicket(s.orgA, s.agentB.UserID, &s.teamA, nil),
	}

	for _, actor := range actors {
		scope := ticketScopeFor(actor)
		for _, target := range tickets {
			inList := matchesScope(scope, target)
			byID := authoriseTicketAccess(actor, target) == nil
			if inList != byID {
				t.Errorf("disagreement for role=%s team=%v ticket(requester=%v team=%v assignee=%v): list=%v byID=%v",
					actor.Role, actor.TeamID, target.RequesterID, target.TeamID, target.AssigneeID, inList, byID)
			}
		}
	}
}

// matchesScope mirrors the SQL that TicketRepo.Search builds. Keeping a Go
// twin of the WHERE clause is duplication, but it is duplication that turns a
// silent data leak into a failing unit test — and it needs no database.
func matchesScope(scope VisibilityScope, t *ticket.Ticket) bool {
	if t.OrgID != scope.OrgID {
		return false
	}
	if scope.All {
		return true
	}

	if scope.RequesterID != nil && t.RequesterID == *scope.RequesterID {
		return true
	}
	if scope.TeamID != nil {
		if t.TeamID != nil && *t.TeamID == *scope.TeamID {
			return true
		}
		if t.AssigneeID != nil && scope.RequesterID != nil && *t.AssigneeID == *scope.RequesterID {
			return true
		}
	}
	if scope.IncludeUnrouted && t.TeamID == nil {
		return true
	}
	return false
}

func TestCanReadInternalNotes(t *testing.T) {
	s := newScenario()
	if canReadInternalNotes(s.requester) {
		t.Fatal("requesters must never read internal notes")
	}
	for _, actor := range []identity.Actor{s.agentA, s.manager, s.admin} {
		if !canReadInternalNotes(actor) {
			t.Fatalf("%s must be able to read internal notes", actor.Role)
		}
	}
}
