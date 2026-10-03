package identity

import (
	"strings"
	"time"

	"github.com/blessnduta/ticketing-system/internal/domain/shared"
)

// Team is a service-desk queue: network, hardware, access management, and so
// on. Tickets are routed to a team first and an individual second, which is
// what lets a desk absorb someone being on leave.
type Team struct {
	ID          ID
	OrgID       ID
	Name        string
	Description string
	// Escalatesto is the team that receives a ticket when its SLA breaches.
	// Nil means breaches escalate to organisation managers instead of another
	// queue. Modelled as a pointer to the same type so escalation chains are
	// just a linked list the SLA worker walks.
	EscalatesTo *ID
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

const maxTeamNameLength = 80

func NewTeam(orgID ID, name, description string, now time.Time) (*Team, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, shared.Invalid("team.name_required", "team name is required")
	}
	if len(name) > maxTeamNameLength {
		return nil, shared.Invalid("team.name_too_long", "team name is too long")
	}
	return &Team{
		ID:          shared.NewID(),
		OrgID:       orgID,
		Name:        name,
		Description: strings.TrimSpace(description),
		CreatedAt:   now,
		UpdatedAt:   now,
	}, nil
}
