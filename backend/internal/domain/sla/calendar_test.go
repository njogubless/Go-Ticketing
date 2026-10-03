package sla

import (
	"testing"
	"time"

	"github.com/blessnduta/ticketing-system/internal/domain/shared"
)

// Business-hours arithmetic is the fiddliest calculation in the system and the
// one whose bugs are least visible: a wrong deadline does not crash anything,
// it just makes every SLA report quietly false. Hence the density here.

func businessHours(t *testing.T, timezone string) *Calendar {
	t.Helper()
	calendar, err := StandardBusinessHours(shared.NewID(), timezone)
	if err != nil {
		t.Fatalf("building calendar: %v", err)
	}
	return calendar
}

func TestAdd_WithinASingleWorkingDay(t *testing.T) {
	calendar := businessHours(t, "UTC")
	// Tuesday 10:00 + 4 working hours = Tuesday 14:00.
	start := time.Date(2026, 3, 10, 10, 0, 0, 0, time.UTC)
	got, err := calendar.Add(start, 4*time.Hour)
	if err != nil {
		t.Fatalf("add: %v", err)
	}
	want := time.Date(2026, 3, 10, 14, 0, 0, 0, time.UTC)
	assertTime(t, want, got)
}

// TestAdd_RollsOverToTheNextDay is the case the guide calls out: a four-hour
// target raised at 16:00 is not due at 20:00.
func TestAdd_RollsOverToTheNextDay(t *testing.T) {
	calendar := businessHours(t, "UTC")
	start := time.Date(2026, 3, 10, 16, 0, 0, 0, time.UTC) // Tuesday 16:00
	got, err := calendar.Add(start, 4*time.Hour)
	if err != nil {
		t.Fatalf("add: %v", err)
	}
	// One hour left on Tuesday (16:00-17:00), three more from Wednesday 09:00.
	want := time.Date(2026, 3, 11, 12, 0, 0, 0, time.UTC)
	assertTime(t, want, got)
}

func TestAdd_SkipsTheWeekend(t *testing.T) {
	calendar := businessHours(t, "UTC")
	start := time.Date(2026, 3, 13, 16, 0, 0, 0, time.UTC) // Friday 16:00
	got, err := calendar.Add(start, 4*time.Hour)
	if err != nil {
		t.Fatalf("add: %v", err)
	}
	// One hour Friday, then Monday 09:00 + 3h.
	want := time.Date(2026, 3, 16, 12, 0, 0, 0, time.UTC)
	assertTime(t, want, got)
}

// TestAdd_StartingOutsideWorkingHours: a ticket raised at 22:00 must not burn
// its budget overnight while nobody is on shift.
func TestAdd_StartingOutsideWorkingHours(t *testing.T) {
	calendar := businessHours(t, "UTC")
	start := time.Date(2026, 3, 10, 22, 0, 0, 0, time.UTC) // Tuesday night
	got, err := calendar.Add(start, 2*time.Hour)
	if err != nil {
		t.Fatalf("add: %v", err)
	}
	want := time.Date(2026, 3, 11, 11, 0, 0, 0, time.UTC) // Wednesday 09:00 + 2h
	assertTime(t, want, got)
}

func TestAdd_StartingOnAWeekend(t *testing.T) {
	calendar := businessHours(t, "UTC")
	start := time.Date(2026, 3, 14, 10, 0, 0, 0, time.UTC) // Saturday
	got, err := calendar.Add(start, time.Hour)
	if err != nil {
		t.Fatalf("add: %v", err)
	}
	want := time.Date(2026, 3, 16, 10, 0, 0, 0, time.UTC) // Monday 09:00 + 1h
	assertTime(t, want, got)
}

func TestAdd_SkipsHolidays(t *testing.T) {
	holiday := time.Date(2026, 3, 11, 0, 0, 0, 0, time.UTC) // Wednesday
	calendar, err := NewCalendar(shared.NewID(), "With holiday", "UTC", []WorkWindow{
		{Weekday: time.Tuesday, StartMins: 9 * 60, EndMins: 17 * 60},
		{Weekday: time.Wednesday, StartMins: 9 * 60, EndMins: 17 * 60},
		{Weekday: time.Thursday, StartMins: 9 * 60, EndMins: 17 * 60},
	}, []time.Time{holiday})
	if err != nil {
		t.Fatalf("building calendar: %v", err)
	}

	start := time.Date(2026, 3, 10, 16, 0, 0, 0, time.UTC) // Tuesday 16:00
	got, err := calendar.Add(start, 2*time.Hour)
	if err != nil {
		t.Fatalf("add: %v", err)
	}
	// One hour Tuesday; Wednesday is a holiday; one more hour Thursday.
	want := time.Date(2026, 3, 12, 10, 0, 0, 0, time.UTC)
	assertTime(t, want, got)
}

// TestAdd_AcrossDaylightSaving is why the calendar stores an IANA name and
// builds each window from a wall-clock Y/M/D + minute offset rather than by
// adding a duration to midnight. Europe/London springs forward on 29 March
// 2026, so that day has 23 real hours — but 09:00-17:00 local is still eight
// working hours, and the resulting deadline is still 12:00 local.
func TestAdd_AcrossDaylightSaving(t *testing.T) {
	calendar := businessHours(t, "Europe/London")
	london, err := time.LoadLocation("Europe/London")
	if err != nil {
		t.Skipf("tz database unavailable: %v", err)
	}

	start := time.Date(2026, 3, 27, 16, 0, 0, 0, london) // Friday 16:00 GMT
	got, err := calendar.Add(start, 4*time.Hour)
	if err != nil {
		t.Fatalf("add: %v", err)
	}

	// One hour Friday; the weekend (containing the DST change) is skipped;
	// three hours from Monday 09:00 BST.
	gotLocal := got.In(london)
	if gotLocal.Hour() != 12 || gotLocal.Day() != 30 {
		t.Fatalf("expected Monday 30 March 12:00 local, got %v", gotLocal)
	}
}

func TestAdd_AlwaysOnCalendarDoesNotSkip(t *testing.T) {
	calendar, err := AlwaysOn(shared.NewID(), "UTC")
	if err != nil {
		t.Fatalf("building 24/7 calendar: %v", err)
	}
	// A P1 at 22:00 on a Friday is due at 02:00 Saturday — critical incidents
	// do not wait for Monday.
	start := time.Date(2026, 3, 13, 22, 0, 0, 0, time.UTC)
	got, err := calendar.Add(start, 4*time.Hour)
	if err != nil {
		t.Fatalf("add: %v", err)
	}
	want := time.Date(2026, 3, 14, 2, 0, 0, 0, time.UTC)
	assertTime(t, want, got)
}

func TestAdd_ZeroBudgetIsIdentity(t *testing.T) {
	calendar := businessHours(t, "UTC")
	start := time.Date(2026, 3, 10, 10, 0, 0, 0, time.UTC)
	got, err := calendar.Add(start, 0)
	if err != nil {
		t.Fatalf("add: %v", err)
	}
	assertTime(t, start, got)
}

func TestElapsed_CountsOnlyWorkingTime(t *testing.T) {
	calendar := businessHours(t, "UTC")
	from := time.Date(2026, 3, 13, 16, 0, 0, 0, time.UTC) // Friday 16:00
	to := time.Date(2026, 3, 16, 10, 0, 0, 0, time.UTC)   // Monday 10:00

	// Wall clock says 66 hours. Working time is one hour Friday plus one hour
	// Monday. A resolution report using wall clock would show this ticket as
	// taking nearly three days.
	got := calendar.Elapsed(from, to)
	if got != 2*time.Hour {
		t.Fatalf("expected 2h of working time, got %v", got)
	}
}

func TestElapsed_ReversedRangeIsZero(t *testing.T) {
	calendar := businessHours(t, "UTC")
	from := time.Date(2026, 3, 16, 10, 0, 0, 0, time.UTC)
	to := time.Date(2026, 3, 13, 16, 0, 0, 0, time.UTC)
	if got := calendar.Elapsed(from, to); got != 0 {
		t.Fatalf("a reversed range must be zero, got %v", got)
	}
}

func TestNewCalendar_Validation(t *testing.T) {
	orgID := shared.NewID()

	t.Run("rejects an unknown timezone", func(t *testing.T) {
		_, err := NewCalendar(orgID, "bad", "Mars/Olympus_Mons",
			[]WorkWindow{{Weekday: time.Monday, StartMins: 540, EndMins: 1020}}, nil)
		if err == nil {
			t.Fatal("expected an error for an unknown timezone")
		}
	})

	t.Run("rejects an empty calendar", func(t *testing.T) {
		if _, err := NewCalendar(orgID, "empty", "UTC", nil, nil); err == nil {
			t.Fatal("a calendar with no working windows must be rejected")
		}
	})

	t.Run("rejects overlapping windows", func(t *testing.T) {
		// Overlapping windows would double-count elapsed time and make every
		// duration measurement wrong in a way nobody would notice.
		_, err := NewCalendar(orgID, "overlap", "UTC", []WorkWindow{
			{Weekday: time.Monday, StartMins: 9 * 60, EndMins: 13 * 60},
			{Weekday: time.Monday, StartMins: 12 * 60, EndMins: 17 * 60},
		}, nil)
		if err == nil {
			t.Fatal("overlapping windows on the same day must be rejected")
		}
	})

	t.Run("rejects an inverted window", func(t *testing.T) {
		_, err := NewCalendar(orgID, "inverted", "UTC",
			[]WorkWindow{{Weekday: time.Monday, StartMins: 17 * 60, EndMins: 9 * 60}}, nil)
		if err == nil {
			t.Fatal("a window ending before it starts must be rejected")
		}
	})

	t.Run("accepts a split shift", func(t *testing.T) {
		// Non-overlapping windows on the same day are legitimate — a desk that
		// closes for lunch.
		if _, err := NewCalendar(orgID, "split", "UTC", []WorkWindow{
			{Weekday: time.Monday, StartMins: 9 * 60, EndMins: 12 * 60},
			{Weekday: time.Monday, StartMins: 13 * 60, EndMins: 17 * 60},
		}, nil); err != nil {
			t.Fatalf("a split shift must be accepted: %v", err)
		}
	})
}

func TestAdd_SplitShiftSkipsTheLunchBreak(t *testing.T) {
	calendar, err := NewCalendar(shared.NewID(), "split", "UTC", []WorkWindow{
		{Weekday: time.Tuesday, StartMins: 9 * 60, EndMins: 12 * 60},
		{Weekday: time.Tuesday, StartMins: 13 * 60, EndMins: 17 * 60},
	}, nil)
	if err != nil {
		t.Fatalf("building calendar: %v", err)
	}

	start := time.Date(2026, 3, 10, 11, 0, 0, 0, time.UTC) // Tuesday 11:00
	got, err := calendar.Add(start, 2*time.Hour)
	if err != nil {
		t.Fatalf("add: %v", err)
	}
	// One hour before lunch, one hour after: 13:00 + 1h = 14:00.
	want := time.Date(2026, 3, 10, 14, 0, 0, 0, time.UTC)
	assertTime(t, want, got)
}

func TestWeeklyCapacity(t *testing.T) {
	if got := businessHours(t, "UTC").WeeklyCapacity(); got != 40*time.Hour {
		t.Fatalf("standard business hours is a 40-hour week, got %v", got)
	}
}

func assertTime(t *testing.T, want, got time.Time) {
	t.Helper()
	if !want.Equal(got) {
		t.Fatalf("expected %v, got %v", want.UTC(), got.UTC())
	}
}
