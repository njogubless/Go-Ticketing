package app

import (
	"context"
	"time"

	"github.com/blessnduta/ticketing-system/internal/domain/identity"
	"github.com/blessnduta/ticketing-system/internal/domain/shared"
)

// ReportService serves the analytics dashboard.
type ReportService struct {
	tickets TicketRepository
	clock   shared.Clock
}

func NewReportService(tickets TicketRepository, clock shared.Clock) *ReportService {
	return &ReportService{tickets: tickets, clock: clock}
}

const (
	defaultReportDays = 30
	maxReportDays     = 366
)

// Overview returns the dashboard payload for a window.
//
// The window is clamped rather than trusted. An unbounded date range on an
// aggregate query is a straightforward way for one authenticated user to
// saturate the database, and "last five years" is never a dashboard question —
// it is an export question, which belongs on a different, queued path.
func (s *ReportService) Overview(ctx context.Context, actor identity.Actor, from, to *time.Time, teamID *shared.ID) (TicketStats, error) {
	if err := actor.Require(identity.PermReportRead); err != nil {
		return TicketStats{}, err
	}

	now := s.clock.Now()
	window := ReportWindow{
		From:   now.AddDate(0, 0, -defaultReportDays),
		To:     now,
		TeamID: teamID,
	}
	if from != nil {
		window.From = *from
	}
	if to != nil {
		window.To = *to
	}
	if !window.To.After(window.From) {
		return TicketStats{}, shared.Invalid("report.window_invalid",
			"the end of the window must be after its start")
	}
	if window.To.Sub(window.From) > maxReportDays*24*time.Hour {
		return TicketStats{}, shared.Invalid("report.window_too_wide",
			"reporting windows are limited to one year").
			WithDetail("max_days", maxReportDays)
	}

	// Managers may scope to any team; nothing narrower is enforced here
	// because PermReportRead is already restricted to managers and admins.
	return s.tickets.Stats(ctx, actor.OrgID, window)
}

// SLAAttainment reduces the stats to the two numbers a service manager is
// actually measured on, as percentages.
func SLAAttainment(stats TicketStats) (firstResponsePct, resolutionPct float64) {
	if stats.FirstResponseTotal > 0 {
		firstResponsePct = float64(stats.FirstResponseMet) / float64(stats.FirstResponseTotal) * 100
	}
	if stats.ResolutionTotal > 0 {
		resolutionPct = float64(stats.ResolutionMet) / float64(stats.ResolutionTotal) * 100
	}
	return firstResponsePct, resolutionPct
}
