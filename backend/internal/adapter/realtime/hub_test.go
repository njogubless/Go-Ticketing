package realtime

import (
	"testing"

	"github.com/blessnduta/ticketing-system/internal/app"
	"github.com/blessnduta/ticketing-system/internal/domain/identity"
	"github.com/blessnduta/ticketing-system/internal/domain/shared"
)

// The realtime fan-out filter is the one authorisation check in the system
// that leaves no trace when it is wrong. A REST leak shows up in an access
// log; a WebSocket leak is a message pushed to a socket nobody audited.
//
// visibleTo therefore mirrors app.authoriseTicketAccess, and these tests pin
// that correspondence case by case.

func event(orgID, requesterID shared.ID, teamID, assigneeID *shared.ID, internalOnly bool) app.Event {
	return app.Event{
		Type:     app.EventTicketMessageAdded,
		OrgID:    orgID,
		TicketID: shared.NewID(),
		Audience: app.EventAudience{
			RequesterID:  requesterID,
			TeamID:       teamID,
			AssigneeID:   assigneeID,
			InternalOnly: internalOnly,
		},
	}
}

func TestVisibleTo_CrossTenantIsAlwaysDenied(t *testing.T) {
	orgA, orgB := shared.NewID(), shared.NewID()
	foreignAdmin := identity.Actor{UserID: shared.NewID(), OrgID: orgB, Role: identity.RoleAdmin}

	if visibleTo(foreignAdmin, event(orgA, shared.NewID(), nil, nil, false)) {
		t.Fatal("an event must never reach a connection from another organisation")
	}
}

func TestVisibleTo_InternalEventsNeverReachRequesters(t *testing.T) {
	orgID := shared.NewID()
	requesterID := shared.NewID()
	requester := identity.Actor{UserID: requesterID, OrgID: orgID, Role: identity.RoleRequester}

	// The requester owns the ticket — and still must not receive the internal
	// note. Ownership is not a reason to see the desk's private commentary.
	internal := event(orgID, requesterID, nil, nil, true)
	if visibleTo(requester, internal) {
		t.Fatal("an internal-only event must never reach a requester, even on their own ticket")
	}

	public := event(orgID, requesterID, nil, nil, false)
	if !visibleTo(requester, public) {
		t.Fatal("a requester must receive public activity on their own ticket")
	}
}

func TestVisibleTo_SLAEventsAreDeskInternal(t *testing.T) {
	orgID := shared.NewID()
	requesterID := shared.NewID()
	requester := identity.Actor{UserID: requesterID, OrgID: orgID, Role: identity.RoleRequester}

	breach := app.Event{
		Type:     app.EventTicketSLABreached,
		OrgID:    orgID,
		TicketID: shared.NewID(),
		Audience: app.EventAudience{RequesterID: requesterID, InternalOnly: true},
	}
	if visibleTo(requester, breach) {
		t.Fatal("a requester must not be pushed 'we missed our target' notifications")
	}
}

func TestVisibleTo_Matrix(t *testing.T) {
	orgID := shared.NewID()
	teamA, teamB := shared.NewID(), shared.NewID()
	requesterID := shared.NewID()
	agentAID := shared.NewID()

	requester := identity.Actor{UserID: requesterID, OrgID: orgID, Role: identity.RoleRequester}
	stranger := identity.Actor{UserID: shared.NewID(), OrgID: orgID, Role: identity.RoleRequester}
	agentA := identity.Actor{UserID: agentAID, OrgID: orgID, Role: identity.RoleAgent, TeamID: &teamA}
	agentB := identity.Actor{UserID: shared.NewID(), OrgID: orgID, Role: identity.RoleAgent, TeamID: &teamB}
	manager := identity.Actor{UserID: shared.NewID(), OrgID: orgID, Role: identity.RoleManager}

	cases := []struct {
		name  string
		actor identity.Actor
		event app.Event
		want  bool
	}{
		{"requester sees own public activity", requester,
			event(orgID, requesterID, &teamA, nil, false), true},
		{"unrelated requester sees nothing", stranger,
			event(orgID, requesterID, &teamA, nil, false), false},
		{"agent sees their team's tickets", agentA,
			event(orgID, requesterID, &teamA, nil, true), true},
		{"agent does not see another team's tickets", agentA,
			event(orgID, requesterID, &teamB, nil, true), false},
		{"agent sees unrouted tickets", agentA,
			event(orgID, requesterID, nil, nil, true), true},
		{"agent sees a ticket assigned to them on another team", agentA,
			event(orgID, requesterID, &teamB, &agentAID, true), true},
		{"other-team agent sees their own queue", agentB,
			event(orgID, requesterID, &teamB, nil, true), true},
		{"manager sees everything in the organisation", manager,
			event(orgID, requesterID, &teamB, nil, true), true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := visibleTo(tc.actor, tc.event); got != tc.want {
				t.Fatalf("expected visibility %v, got %v", tc.want, got)
			}
		})
	}
}

func TestHubRegistersAndCountsPerOrganisation(t *testing.T) {
	hub := NewHub(discardLogger())
	orgA, orgB := shared.NewID(), shared.NewID()

	clientA := &Client{actor: identity.Actor{UserID: shared.NewID(), OrgID: orgA, Role: identity.RoleAgent}, send: make(chan []byte, 1), hub: hub}
	clientB := &Client{actor: identity.Actor{UserID: shared.NewID(), OrgID: orgB, Role: identity.RoleAgent}, send: make(chan []byte, 1), hub: hub}

	hub.register(clientA)
	hub.register(clientB)
	if got := hub.ConnectionCount(); got != 2 {
		t.Fatalf("expected 2 connections, got %d", got)
	}

	// Clients are indexed by organisation, so a broadcast never even iterates
	// another tenant's sockets — isolation as a data structure rather than as
	// a condition inside a loop.
	hub.mu.RLock()
	orgACount := len(hub.clients[orgA])
	hub.mu.RUnlock()
	if orgACount != 1 {
		t.Fatalf("expected org A to hold exactly its own connection, got %d", orgACount)
	}

	hub.unregister(clientA)
	if got := hub.ConnectionCount(); got != 1 {
		t.Fatalf("expected 1 connection after unregister, got %d", got)
	}

	hub.mu.RLock()
	_, stillPresent := hub.clients[orgA]
	hub.mu.RUnlock()
	if stillPresent {
		t.Fatal("an emptied organisation bucket must be removed, not left to accumulate")
	}
}

func TestBroadcastDeliversOnlyToPermittedClients(t *testing.T) {
	hub := NewHub(discardLogger())
	orgID := shared.NewID()
	requesterID := shared.NewID()

	requester := &Client{
		actor: identity.Actor{UserID: requesterID, OrgID: orgID, Role: identity.RoleRequester},
		send:  make(chan []byte, 4), hub: hub,
	}
	agent := &Client{
		actor: identity.Actor{UserID: shared.NewID(), OrgID: orgID, Role: identity.RoleAgent},
		send:  make(chan []byte, 4), hub: hub,
	}
	hub.register(requester)
	hub.register(agent)

	hub.Broadcast(event(orgID, requesterID, nil, nil, true)) // internal note

	if len(requester.send) != 0 {
		t.Fatal("the requester must not receive an internal-only event")
	}
	if len(agent.send) != 1 {
		t.Fatalf("the agent should have received the event, got %d messages", len(agent.send))
	}
}
