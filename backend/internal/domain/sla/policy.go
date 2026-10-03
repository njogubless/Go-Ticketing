package sla

import (
	"sort"
	"strings"
	"time"

	"github.com/blessnduta/ticketing-system/internal/domain/shared"
	"github.com/blessnduta/ticketing-system/internal/domain/ticket"
)

// Policy is a service-level commitment: respond within X and resolve within Y
// of working time, for tickets matching a set of conditions.
type Policy struct {
	ID    shared.ID
	OrgID shared.ID
	Name  string

	// --- matching conditions ---------------------------------------------
	// A nil condition matches everything. Specificity decides which policy
	// wins when several match — see Select.
	Priority *ticket.Priority
	Kind     *ticket.Kind
	TeamID   *shared.ID

	// FirstResponse and Resolution are budgets of *working* time, measured
	// against CalendarID.
	FirstResponse time.Duration
	Resolution    time.Duration
	CalendarID    shared.ID

	// EscalateAfter, when set, raises an escalation this far before the
	// resolution deadline rather than after the breach. Warning ahead of the
	// breach is what actually prevents breaches; alerting after one only
	// documents it.
	EscalateAfter *time.Duration

	Active    bool
	CreatedAt time.Time
	UpdatedAt time.Time
}

// NewPolicy validates and constructs a policy.
func NewPolicy(orgID shared.ID, name string, calendarID shared.ID, firstResponse, resolution time.Duration, now time.Time) (*Policy, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, shared.Invalid("sla.name_required", "a policy name is required")
	}
	if firstResponse <= 0 {
		return nil, shared.Invalid("sla.first_response_invalid", "first-response target must be positive")
	}
	if resolution <= 0 {
		return nil, shared.Invalid("sla.resolution_invalid", "resolution target must be positive")
	}
	if firstResponse > resolution {
		return nil, shared.Invalid("sla.targets_inconsistent",
			"the first-response target cannot be longer than the resolution target")
	}
	if calendarID == shared.NilID {
		return nil, shared.Invalid("sla.calendar_required", "a policy must reference a business calendar")
	}
	return &Policy{
		ID:            shared.NewID(),
		OrgID:         orgID,
		Name:          name,
		FirstResponse: firstResponse,
		Resolution:    resolution,
		CalendarID:    calendarID,
		Active:        true,
		CreatedAt:     now,
		UpdatedAt:     now,
	}, nil
}

// matches reports whether the policy applies to a ticket.
func (p *Policy) matches(t *ticket.Ticket) bool {
	if !p.Active {
		return false
	}
	if p.Priority != nil && *p.Priority != t.Priority {
		return false
	}
	if p.Kind != nil && *p.Kind != t.Kind {
		return false
	}
	if p.TeamID != nil && (t.TeamID == nil || *p.TeamID != *t.TeamID) {
		return false
	}
	return true
}

// specificity counts the constraints a policy places. More constraints means a
// closer match, so "P1 incidents for the network team" beats "P1 anything".
// Deterministic selection matters more than clever selection here: an agent
// must be able to explain why a ticket got the deadline it got.
func (p *Policy) specificity() int {
	score := 0
	if p.Priority != nil {
		score += 4 // priority is the strongest signal
	}
	if p.Kind != nil {
		score += 2
	}
	if p.TeamID != nil {
		score++
	}
	return score
}

// Select picks the policy governing a ticket: the most specific match, with
// ties broken by name so the outcome is stable across restarts and across
// replicas. Returns nil when nothing matches — an unmatched ticket has no SLA
// rather than a made-up one.
func Select(policies []*Policy, t *ticket.Ticket) *Policy {
	var candidates []*Policy
	for _, policy := range policies {
		if policy.matches(t) {
			candidates = append(candidates, policy)
		}
	}
	if len(candidates) == 0 {
		return nil
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		si, sj := candidates[i].specificity(), candidates[j].specificity()
		if si != sj {
			return si > sj
		}
		return candidates[i].Name < candidates[j].Name
	})
	return candidates[0]
}

// Targets is the computed outcome of applying a policy to a ticket.
type Targets struct {
	PolicyID         shared.ID
	FirstResponseDue time.Time
	ResolutionDue    time.Time
	EscalateAt       *time.Time
}

// ComputeTargets turns budgets of working time into absolute deadlines.
func (p *Policy) ComputeTargets(calendar *Calendar, from time.Time) (Targets, error) {
	firstResponseDue, err := calendar.Add(from, p.FirstResponse)
	if err != nil {
		return Targets{}, err
	}
	resolutionDue, err := calendar.Add(from, p.Resolution)
	if err != nil {
		return Targets{}, err
	}

	targets := Targets{
		PolicyID:         p.ID,
		FirstResponseDue: firstResponseDue,
		ResolutionDue:    resolutionDue,
	}

	if p.EscalateAfter != nil && *p.EscalateAfter > 0 && *p.EscalateAfter < p.Resolution {
		escalateAt, err := calendar.Add(from, p.Resolution-*p.EscalateAfter)
		if err != nil {
			return Targets{}, err
		}
		targets.EscalateAt = &escalateAt
	}

	return targets, nil
}

// DefaultPolicies returns a sensible starting set for a new organisation,
// following common ITSM practice. Shipping defaults matters: an organisation
// that has to author SLA policies before its first ticket will simply not use
// the feature, and then nothing is measured.
func DefaultPolicies(orgID, alwaysOnCalendarID, businessHoursCalendarID shared.ID, now time.Time) []*Policy {
	type spec struct {
		name          string
		priority      ticket.Priority
		firstResponse time.Duration
		resolution    time.Duration
		calendarID    shared.ID
		escalateAfter time.Duration
	}

	specs := []spec{
		// P1 runs on the 24/7 calendar: a critical outage does not wait for
		// Monday morning.
		{"P1 — critical", ticket.PriorityP1, 15 * time.Minute, 4 * time.Hour, alwaysOnCalendarID, 1 * time.Hour},
		{"P2 — high", ticket.PriorityP2, 1 * time.Hour, 8 * time.Hour, businessHoursCalendarID, 2 * time.Hour},
		{"P3 — normal", ticket.PriorityP3, 4 * time.Hour, 24 * time.Hour, businessHoursCalendarID, 4 * time.Hour},
		{"P4 — low", ticket.PriorityP4, 8 * time.Hour, 40 * time.Hour, businessHoursCalendarID, 0},
	}

	policies := make([]*Policy, 0, len(specs))
	for _, s := range specs {
		priority := s.priority
		policy := &Policy{
			ID:            shared.NewID(),
			OrgID:         orgID,
			Name:          s.name,
			Priority:      &priority,
			FirstResponse: s.firstResponse,
			Resolution:    s.resolution,
			CalendarID:    s.calendarID,
			Active:        true,
			CreatedAt:     now,
			UpdatedAt:     now,
		}
		if s.escalateAfter > 0 {
			escalateAfter := s.escalateAfter
			policy.EscalateAfter = &escalateAfter
		}
		policies = append(policies, policy)
	}
	return policies
}
