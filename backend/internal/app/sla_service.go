package app

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/blessnduta/ticketing-system/internal/domain/identity"
	"github.com/blessnduta/ticketing-system/internal/domain/shared"
	"github.com/blessnduta/ticketing-system/internal/domain/sla"
	"github.com/blessnduta/ticketing-system/internal/domain/ticket"
)

// SLAService applies policies to tickets and runs the breach detector.
type SLAService struct {
	slaRepo SLARepository
	tickets TicketRepository
	users   UserRepository
	teams   TeamRepository
	audit   AuditRepository
	events  EventPublisher
	clock   shared.Clock
	logger  *slog.Logger

	// cache avoids reloading an organisation's policies and calendars on every
	// single ticket write. Policies change rarely (a handful of times a year)
	// and are read on every create, route and reclassify — exactly the profile
	// that justifies a cache. TTL rather than explicit invalidation keeps the
	// write path from needing to know the cache exists.
	cache   sync.Map // shared.ID -> *policyCacheEntry
	cacheTTL time.Duration
}

type policyCacheEntry struct {
	policies  []*sla.Policy
	calendars map[shared.ID]*sla.Calendar
	loadedAt  time.Time
}

func NewSLAService(
	slaRepo SLARepository,
	tickets TicketRepository,
	users UserRepository,
	teams TeamRepository,
	audit AuditRepository,
	events EventPublisher,
	clock shared.Clock,
	logger *slog.Logger,
) *SLAService {
	return &SLAService{
		slaRepo: slaRepo, tickets: tickets, users: users, teams: teams,
		audit: audit, events: events, clock: clock, logger: logger,
		cacheTTL: 5 * time.Minute,
	}
}

// ApplyTargets selects the governing policy and stamps deadlines onto the
// ticket. It mutates the ticket but does not persist it — the caller is inside
// a transaction and owns the write, so this cannot half-commit.
//
// A ticket with no matching policy keeps whatever targets it had and is
// otherwise left alone. Inventing a deadline for an unmatched ticket would
// make every SLA report subtly fictional.
func (s *SLAService) ApplyTargets(ctx context.Context, t *ticket.Ticket, now time.Time) error {
	entry, err := s.load(ctx, t.OrgID)
	if err != nil {
		return err
	}

	policy := sla.Select(entry.policies, t)
	if policy == nil {
		return nil
	}
	calendar, ok := entry.calendars[policy.CalendarID]
	if !ok {
		// A policy referencing a missing calendar is a data-integrity problem,
		// not a request problem: log it and leave the ticket untouched rather
		// than failing the agent's action.
		s.logger.ErrorContext(ctx, "sla policy references unknown calendar",
			slog.String("policy_id", policy.ID.String()),
			slog.String("calendar_id", policy.CalendarID.String()))
		return nil
	}

	// Deadlines are computed from ticket creation, not from "now": recomputing
	// after a reclassification must not silently hand the desk a fresh budget.
	targets, err := policy.ComputeTargets(calendar, t.CreatedAt)
	if err != nil {
		return err
	}
	t.ApplySLATargets(targets.PolicyID, targets.FirstResponseDue, targets.ResolutionDue, now)
	return nil
}

// PolicySnapshot is the read model for the admin SLA screen.
type PolicySnapshot struct {
	Policies  []*sla.Policy
	Calendars []*sla.Calendar
}

func (s *SLAService) List(ctx context.Context, actor identity.Actor) (*PolicySnapshot, error) {
	if err := actor.Require(identity.PermSLAManage); err != nil {
		return nil, err
	}
	policies, err := s.slaRepo.ListPolicies(ctx, actor.OrgID)
	if err != nil {
		return nil, err
	}
	calendars, err := s.slaRepo.ListCalendars(ctx, actor.OrgID)
	if err != nil {
		return nil, err
	}
	return &PolicySnapshot{Policies: policies, Calendars: calendars}, nil
}

type CreatePolicyInput struct {
	Name          string
	Priority      *ticket.Priority
	Kind          *ticket.Kind
	TeamID        *shared.ID
	CalendarID    shared.ID
	FirstResponse time.Duration
	Resolution    time.Duration
	EscalateAfter *time.Duration
}

func (s *SLAService) CreatePolicy(ctx context.Context, actor identity.Actor, in CreatePolicyInput) (*sla.Policy, error) {
	if err := actor.Require(identity.PermSLAManage); err != nil {
		return nil, err
	}
	now := s.clock.Now()

	if _, err := s.slaRepo.CalendarByID(ctx, actor.OrgID, in.CalendarID); err != nil {
		return nil, shared.Invalid("sla.calendar_unknown", "that business calendar does not exist")
	}

	policy, err := sla.NewPolicy(actor.OrgID, in.Name, in.CalendarID, in.FirstResponse, in.Resolution, now)
	if err != nil {
		return nil, err
	}
	policy.Priority = in.Priority
	policy.Kind = in.Kind
	policy.TeamID = in.TeamID
	policy.EscalateAfter = in.EscalateAfter

	if err := s.slaRepo.CreatePolicy(ctx, policy); err != nil {
		return nil, err
	}
	s.cache.Delete(actor.OrgID)
	return policy, nil
}

func (s *SLAService) load(ctx context.Context, orgID shared.ID) (*policyCacheEntry, error) {
	if cached, ok := s.cache.Load(orgID); ok {
		entry := cached.(*policyCacheEntry)
		if s.clock.Now().Sub(entry.loadedAt) < s.cacheTTL {
			return entry, nil
		}
	}

	policies, err := s.slaRepo.ListPolicies(ctx, orgID)
	if err != nil {
		return nil, err
	}
	calendarList, err := s.slaRepo.ListCalendars(ctx, orgID)
	if err != nil {
		return nil, err
	}
	calendars := make(map[shared.ID]*sla.Calendar, len(calendarList))
	for _, calendar := range calendarList {
		calendars[calendar.ID] = calendar
	}

	entry := &policyCacheEntry{policies: policies, calendars: calendars, loadedAt: s.clock.Now()}
	s.cache.Store(orgID, entry)
	return entry, nil
}

// ---------------------------------------------------------------------------
// Breach worker
// ---------------------------------------------------------------------------

// BreachWorker periodically finds tickets past their resolution deadline,
// escalates them and emits alerts.
//
// Design notes that matter more than the code:
//
//   - It queries for work rather than scheduling a timer per ticket. A million
//     pending timers is a memory leak with a scheduler attached; one indexed
//     query per interval is not.
//   - It is idempotent via BreachNotifiedAt, so a crash mid-batch re-processes
//     safely and nobody gets alerted twice.
//   - It processes in bounded batches, so a backlog degrades throughput rather
//     than exhausting memory.
type BreachWorker struct {
	sla      *SLAService
	tickets  TicketRepository
	teams    TeamRepository
	audit    AuditRepository
	events   EventPublisher
	clock    shared.Clock
	logger   *slog.Logger
	interval time.Duration
	batch    int
}

func NewBreachWorker(slaService *SLAService, tickets TicketRepository, teams TeamRepository,
	audit AuditRepository, events EventPublisher, clock shared.Clock, logger *slog.Logger,
	interval time.Duration) *BreachWorker {
	return &BreachWorker{
		sla: slaService, tickets: tickets, teams: teams, audit: audit, events: events,
		clock: clock, logger: logger, interval: interval, batch: 200,
	}
}

// Run blocks until the context is cancelled, sweeping on each tick.
func (w *BreachWorker) Run(ctx context.Context) {
	ticker := time.NewTicker(w.interval)
	defer ticker.Stop()

	w.logger.InfoContext(ctx, "sla breach worker started", slog.Duration("interval", w.interval))
	for {
		select {
		case <-ctx.Done():
			w.logger.InfoContext(ctx, "sla breach worker stopped")
			return
		case <-ticker.C:
			if err := w.Sweep(ctx); err != nil {
				// A failed sweep is logged and retried next tick. Killing the
				// worker over one bad batch would silently stop all SLA
				// alerting, which is a far worse outcome than a noisy log.
				w.logger.ErrorContext(ctx, "sla sweep failed", slog.Any("error", err))
			}
		}
	}
}

// Sweep processes one batch. Exported so tests can drive it directly instead
// of waiting on a ticker.
func (w *BreachWorker) Sweep(ctx context.Context) error {
	now := w.clock.Now()
	breached, err := w.tickets.DueForBreachCheck(ctx, now, w.batch)
	if err != nil {
		return err
	}
	if len(breached) == 0 {
		return nil
	}

	w.logger.InfoContext(ctx, "processing sla breaches", slog.Int("count", len(breached)))

	for _, t := range breached {
		// Re-check against the aggregate's own rule rather than trusting the
		// query: the ticket may have been paused between the query and here.
		if !t.BreachedAt(now) {
			continue
		}

		previousUpdatedAt := t.UpdatedAt
		t.MarkBreachNotified(now)

		// Escalate to the team's escalation target, if one is configured.
		escalated := false
		if t.TeamID != nil {
			if team, err := w.teams.ByID(ctx, t.OrgID, *t.TeamID); err == nil && team.EscalatesTo != nil {
				if err := t.Route(team.EscalatesTo, now); err == nil {
					escalated = true
				}
			}
		}

		if err := w.tickets.Update(ctx, t, previousUpdatedAt); err != nil {
			// A conflict means an agent touched the ticket in the same
			// instant — their write wins, and the next sweep will pick this up
			// again if it is still breaching.
			if shared.KindOf(err) != shared.KindConflict {
				w.logger.ErrorContext(ctx, "failed to mark breach",
					slog.String("ticket_id", t.ID.String()), slog.Any("error", err))
			}
			continue
		}

		action := ticket.AuditSLABreached
		if escalated {
			action = ticket.AuditEscalated
		}
		if err := w.audit.Append(ctx, ticket.NewSystemAuditEntry(
			t.OrgID, t.ID, action, nil, ticket.StrPtr(string(t.Priority)), now,
		)); err != nil {
			w.logger.ErrorContext(ctx, "failed to audit breach",
				slog.String("ticket_id", t.ID.String()), slog.Any("error", err))
		}

		if w.events != nil {
			w.events.Publish(ctx, Event{
				Type:     EventTicketSLABreached,
				OrgID:    t.OrgID,
				TicketID: t.ID,
				Audience: EventAudience{
					RequesterID: t.RequesterID,
					AssigneeID:  t.AssigneeID,
					TeamID:      t.TeamID,
					// Breaches are a desk-internal matter. Telling a requester
					// "we missed our target" via a realtime toast is a support
					// decision, not a default.
					InternalOnly: true,
				},
				Payload: map[string]any{
					"reference": t.Reference,
					"subject":   t.Subject,
					"priority":  string(t.Priority),
					"escalated": escalated,
					"due_at":    t.ResolutionDue,
				},
				At: now,
			})
		}
	}

	return nil
}
