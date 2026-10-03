package app

import (
	"context"
	"strings"

	"github.com/blessnduta/ticketing-system/internal/domain/identity"
	"github.com/blessnduta/ticketing-system/internal/domain/shared"
	"github.com/blessnduta/ticketing-system/internal/domain/ticket"
)

// ViewService manages saved ticket filters.
type ViewService struct {
	views SavedViewRepository
	clock shared.Clock
}

func NewViewService(views SavedViewRepository, clock shared.Clock) *ViewService {
	return &ViewService{views: views, clock: clock}
}

const maxViewNameLength = 60

// Create stores a named filter.
//
// The stored filter deliberately excludes VisibilityScope: a saved view
// captures *what* an agent wants to see, never *what they are allowed* to see.
// Scope is recomputed from the actor on every execution, so a view shared by a
// manager does not grant its recipients the manager's reach.
func (s *ViewService) Create(ctx context.Context, actor identity.Actor, name string, shareWithTeam bool, filter TicketQuery) (*SavedView, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, shared.Invalid("view.name_required", "a name is required")
	}
	if len(name) > maxViewNameLength {
		return nil, shared.Invalid("view.name_too_long", "name is too long")
	}
	if shareWithTeam && !actor.Can(identity.PermTicketReadTeam) {
		return nil, shared.Forbidden("view.share_forbidden", "only staff can share views with a team")
	}

	filter.Scope = VisibilityScope{}
	filter.Page = shared.Pagination{}

	view := &SavedView{
		ID:        shared.NewID(),
		OrgID:     actor.OrgID,
		OwnerID:   actor.UserID,
		Name:      name,
		Shared:    shareWithTeam,
		Filter:    filter,
		CreatedAt: s.clock.Now(),
	}
	if err := s.views.Create(ctx, view); err != nil {
		return nil, err
	}
	return view, nil
}

func (s *ViewService) List(ctx context.Context, actor identity.Actor) ([]*SavedView, error) {
	return s.views.List(ctx, actor.OrgID, actor.UserID)
}

func (s *ViewService) Delete(ctx context.Context, actor identity.Actor, viewID shared.ID) error {
	return s.views.Delete(ctx, actor.OrgID, actor.UserID, viewID)
}

// Resolve turns a saved view into an executable query for this actor.
func (s *ViewService) Resolve(ctx context.Context, actor identity.Actor, viewID shared.ID, page shared.Pagination) (TicketQuery, error) {
	view, err := s.views.ByID(ctx, actor.OrgID, viewID)
	if err != nil {
		return TicketQuery{}, err
	}
	if view.OwnerID != actor.UserID && !view.Shared {
		return TicketQuery{}, shared.NotFound("view.not_found", "view not found")
	}
	query := view.Filter
	query.Scope = ticketScopeFor(actor)
	query.Page = page
	return query, nil
}

// SystemViews are the built-in queues every desk needs on day one, so a new
// organisation is not staring at an empty sidebar. They are computed rather
// than stored, which means they cannot be deleted or misconfigured.
func SystemViews(actor identity.Actor) []*SavedView {
	unassigned := TicketQuery{
		Statuses:   []ticket.Status{ticket.StatusNew, ticket.StatusTriaged},
		Unassigned: true,
		Sort:       SortPriority,
	}
	mine := TicketQuery{
		Statuses:   []ticket.Status{ticket.StatusTriaged, ticket.StatusInProgress, ticket.StatusPendingRequester},
		AssigneeID: &actor.UserID,
		Sort:       SortDueSoonest,
	}
	breaching := TicketQuery{
		BreachedOnly: true,
		Sort:         SortDueSoonest,
	}
	needsApproval := TicketQuery{
		Statuses: []ticket.Status{ticket.StatusPendingApproval},
		Kinds:    []ticket.Kind{ticket.KindChange},
		Sort:     SortOldest,
	}

	return []*SavedView{
		{ID: systemViewID("unassigned"), Name: "Unassigned", Filter: unassigned},
		{ID: systemViewID("mine"), Name: "My tickets", Filter: mine},
		{ID: systemViewID("breaching"), Name: "Breaching SLA", Filter: breaching},
		{ID: systemViewID("approvals"), Name: "Awaiting approval", Filter: needsApproval},
	}
}

// systemViewID derives a stable identifier from a name so built-in views keep
// the same ID across restarts and across replicas — the frontend caches by ID.
func systemViewID(name string) shared.ID {
	var id shared.ID
	copy(id[:], "sysview-")
	copy(id[8:], name)
	return id
}
