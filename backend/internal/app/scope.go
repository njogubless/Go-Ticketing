package app

import (
	"github.com/blessnduta/ticketing-system/internal/domain/identity"
	"github.com/blessnduta/ticketing-system/internal/domain/shared"
	"github.com/blessnduta/ticketing-system/internal/domain/ticket"
)

// This file is the whole of the row-level authorisation policy. It is one
// small file on purpose: "who can see which ticket" is the most security-
// critical question in the system, and the answer should be readable in one
// sitting rather than reconstructed from a dozen handlers.
//
// Two functions, used everywhere:
//
//	ticketScopeFor  — turns an actor into a query filter (for list/search)
//	authoriseTicketAccess — checks one already-loaded ticket (for reads/writes)
//
// They must agree. A ticket that would not appear in a list must not be
// reachable by guessing its ID, which is exactly the class of bug that leaks
// one customer's data to another.

// ticketScopeFor derives the query-level visibility filter from the actor.
func ticketScopeFor(actor identity.Actor) VisibilityScope {
	scope := VisibilityScope{
		OrgID:           actor.OrgID,
		CanReadInternal: actor.Can(identity.PermNoteInternal),
	}

	switch {
	case actor.Can(identity.PermTicketReadAll):
		scope.All = true

	case actor.Can(identity.PermTicketReadTeam):
		// Agents work their team's queue, plus anything not yet routed —
		// otherwise a brand-new ticket is invisible to everyone until a
		// manager routes it, which is precisely the "falls through the
		// cracks" failure the system exists to prevent.
		scope.TeamID = actor.TeamID
		scope.IncludeUnrouted = true
		// An agent also sees tickets they raised themselves, even if another
		// team owns them.
		userID := actor.UserID
		scope.RequesterID = &userID

	default:
		// Requesters see their own and nothing else.
		userID := actor.UserID
		scope.RequesterID = &userID
	}

	return scope
}

// authoriseTicketAccess decides whether an actor may read a specific ticket.
//
// It returns NotFound rather than Forbidden for tickets the actor may not see.
// That is deliberate: replying "forbidden" confirms the ticket exists, which
// turns the ID space into an oracle an attacker can enumerate. Only when the
// actor can already see the ticket, but lacks the right to the *action*, does
// a Forbidden come back — from the permission check, not from here.
func authoriseTicketAccess(actor identity.Actor, t *ticket.Ticket) error {
	notFound := shared.NotFound("ticket.not_found", "ticket not found")

	// Cross-tenant access is impossible by construction (the repository scopes
	// by org), but assert it anyway: this is the invariant whose failure is
	// most expensive, and an assertion costs nothing.
	if t.OrgID != actor.OrgID {
		return notFound
	}

	if actor.Can(identity.PermTicketReadAll) {
		return nil
	}
	if t.RequesterID == actor.UserID {
		return nil
	}
	if actor.Can(identity.PermTicketReadTeam) {
		// Same team, or not yet routed to any team.
		if t.TeamID == nil {
			return nil
		}
		if actor.InTeam(*t.TeamID) {
			return nil
		}
		// An agent explicitly assigned a ticket keeps access to it even after
		// the ticket is routed elsewhere — otherwise handing a ticket over
		// erases the previous owner's ability to answer questions about it.
		if t.AssigneeID != nil && *t.AssigneeID == actor.UserID {
			return nil
		}
	}

	return notFound
}

// canReadInternalNotes reports whether internal notes may be included in a
// ticket's thread for this actor. Kept as a named function rather than an
// inline permission check so that every call site reads identically and a
// grep for it finds all of them.
func canReadInternalNotes(actor identity.Actor) bool {
	return actor.Can(identity.PermNoteInternal)
}

// requireSameOrg guards non-ticket entities. Assets, users and teams are
// fetched org-scoped by their repositories, so this is a second assertion
// rather than the primary control.
func requireSameOrg(actor identity.Actor, orgID shared.ID) error {
	if actor.OrgID != orgID {
		return shared.NotFound("resource.not_found", "not found")
	}
	return nil
}
