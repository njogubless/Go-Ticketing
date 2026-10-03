// Package sla implements service-level targets, including the business-hours
// arithmetic that makes them meaningful.
//
// The guide flags business time as "surprisingly fiddly" and that is right. A
// four-hour resolution target raised at 16:00 on a Friday is not due at 20:00
// Friday; against a 09:00–17:00 Mon–Fri calendar it is due at 11:00 on Monday,
// and if Monday is a public holiday, 11:00 on Tuesday. Getting this wrong
// means every SLA report is quietly false, so the calculation lives in its own
// package with exhaustive tests rather than inline in a service.
package sla

import (
	"fmt"
	"sort"
	"time"

	"github.com/blessnduta/ticketing-system/internal/domain/shared"
)

// WorkWindow is a contiguous working span on a given weekday, expressed in the
// calendar's local timezone as minutes past midnight. Minutes rather than a
// time.Time avoids carrying a meaningless date around, and avoids the whole
// class of DST bugs that come from adding durations to wall-clock times.
type WorkWindow struct {
	Weekday   time.Weekday
	StartMins int // inclusive, 0..1440
	EndMins   int // exclusive, 0..1440, must exceed StartMins
}

const minutesPerDay = 24 * 60

func (w WorkWindow) validate() error {
	if w.StartMins < 0 || w.StartMins >= minutesPerDay {
		return shared.Invalid("sla.window_start_invalid", "window start must be within the day")
	}
	if w.EndMins <= w.StartMins || w.EndMins > minutesPerDay {
		return shared.Invalid("sla.window_end_invalid", "window end must be after its start and within the day")
	}
	return nil
}

func (w WorkWindow) duration() time.Duration {
	return time.Duration(w.EndMins-w.StartMins) * time.Minute
}

// Calendar defines an organisation's or team's working hours.
type Calendar struct {
	ID       shared.ID
	OrgID    shared.ID
	Name     string
	Timezone string
	Windows  []WorkWindow
	// Holidays are full non-working days, keyed by local calendar date. A set
	// of dates rather than instants because "Christmas Day" is a date, and its
	// UTC span depends on the timezone.
	Holidays map[string]struct{}

	location *time.Location // resolved once in NewCalendar; never nil after
}

// AlwaysOn is the 24/7 calendar, used for P1 incidents and as the fallback
// when an organisation has not configured business hours. Real incident
// response for a critical outage does not stop at 17:00.
func AlwaysOn(orgID shared.ID, timezone string) (*Calendar, error) {
	windows := make([]WorkWindow, 0, 7)
	for day := time.Sunday; day <= time.Saturday; day++ {
		windows = append(windows, WorkWindow{Weekday: day, StartMins: 0, EndMins: minutesPerDay})
	}
	return NewCalendar(orgID, "24/7", timezone, windows, nil)
}

// StandardBusinessHours is the common 09:00–17:00, Monday to Friday default.
func StandardBusinessHours(orgID shared.ID, timezone string) (*Calendar, error) {
	windows := make([]WorkWindow, 0, 5)
	for day := time.Monday; day <= time.Friday; day++ {
		windows = append(windows, WorkWindow{Weekday: day, StartMins: 9 * 60, EndMins: 17 * 60})
	}
	return NewCalendar(orgID, "Business hours", timezone, windows, nil)
}

// NewCalendar validates the windows, resolves the timezone once, and sorts the
// windows so lookups can rely on ordering.
func NewCalendar(orgID shared.ID, name, timezone string, windows []WorkWindow, holidays []time.Time) (*Calendar, error) {
	location, err := time.LoadLocation(timezone)
	if err != nil {
		return nil, shared.Invalid("sla.timezone_invalid", "unknown IANA timezone").
			WithDetail("timezone", timezone)
	}
	if len(windows) == 0 {
		return nil, shared.Invalid("sla.calendar_empty", "a calendar needs at least one working window")
	}
	for _, window := range windows {
		if err := window.validate(); err != nil {
			return nil, err
		}
	}

	sorted := append([]WorkWindow(nil), windows...)
	sort.Slice(sorted, func(i, j int) bool {
		if sorted[i].Weekday != sorted[j].Weekday {
			return sorted[i].Weekday < sorted[j].Weekday
		}
		return sorted[i].StartMins < sorted[j].StartMins
	})
	// Overlapping windows on the same day would double-count elapsed time.
	for i := 1; i < len(sorted); i++ {
		if sorted[i].Weekday == sorted[i-1].Weekday && sorted[i].StartMins < sorted[i-1].EndMins {
			return nil, shared.Invalid("sla.windows_overlap", "working windows on the same day must not overlap").
				WithDetail("weekday", sorted[i].Weekday.String())
		}
	}

	holidaySet := make(map[string]struct{}, len(holidays))
	for _, holiday := range holidays {
		holidaySet[holiday.In(location).Format("2006-01-02")] = struct{}{}
	}

	return &Calendar{
		ID:       shared.NewID(),
		OrgID:    orgID,
		Name:     name,
		Timezone: timezone,
		Windows:  sorted,
		Holidays: holidaySet,
		location: location,
	}, nil
}

// Location returns the resolved timezone.
func (c *Calendar) Location() *time.Location {
	if c.location == nil {
		// Defensive: a Calendar rebuilt from the database by a mapper that
		// forgot to resolve the location falls back to UTC rather than
		// panicking mid-request. The repository does resolve it; this is a
		// belt-and-braces guard.
		if loc, err := time.LoadLocation(c.Timezone); err == nil {
			c.location = loc
		} else {
			c.location = time.UTC
		}
	}
	return c.location
}

// windowsOn returns the working spans for a specific local date, as absolute
// instants. Holidays yield none.
func (c *Calendar) windowsOn(localDay time.Time) []span {
	if _, isHoliday := c.Holidays[localDay.Format("2006-01-02")]; isHoliday {
		return nil
	}
	var spans []span
	for _, window := range c.Windows {
		if window.Weekday != localDay.Weekday() {
			continue
		}
		// Constructing from Y/M/D + minute offset (rather than adding a
		// duration to midnight) is what makes this DST-correct: on a spring
		// forward day, 09:00 local is still 09:00 local even though only 23
		// hours have elapsed since the previous midnight.
		year, month, day := localDay.Date()
		start := time.Date(year, month, day, window.StartMins/60, window.StartMins%60, 0, 0, c.Location())
		var end time.Time
		if window.EndMins == minutesPerDay {
			end = time.Date(year, month, day, 0, 0, 0, 0, c.Location()).AddDate(0, 0, 1)
		} else {
			end = time.Date(year, month, day, window.EndMins/60, window.EndMins%60, 0, 0, c.Location())
		}
		spans = append(spans, span{start: start, end: end})
	}
	return spans
}

type span struct{ start, end time.Time }

// maxLookaheadDays bounds the search when adding business time. A budget
// larger than the working year is a misconfiguration, and an unbounded loop
// over a calendar with a typo in it would hang a request.
const maxLookaheadDays = 730

// Add returns the instant at which `budget` of working time will have elapsed,
// starting from `from`. If `from` falls outside working hours, the clock starts
// at the next window — a ticket raised at 22:00 does not burn its overnight.
func (c *Calendar) Add(from time.Time, budget time.Duration) (time.Time, error) {
	if budget <= 0 {
		return from, nil
	}

	cursor := from.In(c.Location())
	remaining := budget

	for dayOffset := 0; dayOffset <= maxLookaheadDays; dayOffset++ {
		day := time.Date(cursor.Year(), cursor.Month(), cursor.Day(), 0, 0, 0, 0, c.Location()).
			AddDate(0, 0, dayOffset)

		for _, window := range c.windowsOn(day) {
			// Skip windows entirely in the past relative to the cursor.
			if !window.end.After(cursor) {
				continue
			}
			start := window.start
			if cursor.After(start) {
				start = cursor
			}
			available := window.end.Sub(start)
			if available >= remaining {
				return start.Add(remaining).UTC(), nil
			}
			remaining -= available
		}
	}

	return time.Time{}, shared.Internal("sla.budget_unreachable",
		"the SLA target could not be met within the calendar's horizon").
		WithCause(fmt.Errorf("budget %s exceeds %d days of working time", budget, maxLookaheadDays))
}

// Elapsed returns how much working time lies between two instants. Used to
// measure actual response and resolution times against their targets — a
// ticket open over a weekend has not consumed 48 hours of anyone's SLA.
func (c *Calendar) Elapsed(from, to time.Time) time.Duration {
	if !to.After(from) {
		return 0
	}
	fromLocal := from.In(c.Location())
	toLocal := to.In(c.Location())

	var total time.Duration
	for dayOffset := 0; dayOffset <= maxLookaheadDays; dayOffset++ {
		day := time.Date(fromLocal.Year(), fromLocal.Month(), fromLocal.Day(), 0, 0, 0, 0, c.Location()).
			AddDate(0, 0, dayOffset)
		if day.After(toLocal) {
			break
		}
		for _, window := range c.windowsOn(day) {
			start, end := window.start, window.end
			if start.Before(fromLocal) {
				start = fromLocal
			}
			if end.After(toLocal) {
				end = toLocal
			}
			if end.After(start) {
				total += end.Sub(start)
			}
		}
	}
	return total
}

// WeeklyCapacity is the total working time in one week — used by the reporting
// layer to express queue load as a fraction of available desk time.
func (c *Calendar) WeeklyCapacity() time.Duration {
	var total time.Duration
	for _, window := range c.Windows {
		total += window.duration()
	}
	return total
}
