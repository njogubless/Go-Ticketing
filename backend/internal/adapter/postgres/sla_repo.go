package postgres

import (
	"context"
	"encoding/json"
	"time"

	"github.com/blessnduta/ticketing-system/internal/app"
	"github.com/blessnduta/ticketing-system/internal/domain/shared"
	"github.com/blessnduta/ticketing-system/internal/domain/sla"
	"github.com/blessnduta/ticketing-system/internal/platform/database"
)

type SLARepo struct{ db *database.Pool }

func NewSLARepo(db *database.Pool) *SLARepo { return &SLARepo{db: db} }

var _ app.SLARepository = (*SLARepo)(nil)

// windowRecord is the JSONB shape for a working window. A separate type from
// sla.WorkWindow so the storage format is versionable independently of the
// domain type — renaming a domain field must not silently orphan stored rows.
type windowRecord struct {
	Weekday   int `json:"weekday"`
	StartMins int `json:"start_mins"`
	EndMins   int `json:"end_mins"`
}

func (r *SLARepo) CreateCalendar(ctx context.Context, calendar *sla.Calendar) error {
	windows := make([]windowRecord, len(calendar.Windows))
	for i, window := range calendar.Windows {
		windows[i] = windowRecord{
			Weekday:   int(window.Weekday),
			StartMins: window.StartMins,
			EndMins:   window.EndMins,
		}
	}
	windowsJSON, err := json.Marshal(windows)
	if err != nil {
		return shared.Internal("sla.encode_failed", "could not encode calendar").WithCause(err)
	}

	holidays := make([]string, 0, len(calendar.Holidays))
	for day := range calendar.Holidays {
		holidays = append(holidays, day)
	}
	holidaysJSON, err := json.Marshal(holidays)
	if err != nil {
		return shared.Internal("sla.encode_failed", "could not encode calendar").WithCause(err)
	}

	_, err = r.db.Querier(ctx).Exec(ctx, `
		INSERT INTO sla_calendars (id, organization_id, name, timezone, windows, holidays)
		VALUES ($1,$2,$3,$4,$5,$6)`,
		calendar.ID, calendar.OrgID, calendar.Name, calendar.Timezone, windowsJSON, holidaysJSON)
	return translate(err, "sla_calendar")
}

func (r *SLARepo) CalendarByID(ctx context.Context, orgID, id shared.ID) (*sla.Calendar, error) {
	row := r.db.Querier(ctx).QueryRow(ctx, `
		SELECT id, organization_id, name, timezone, windows, holidays
		FROM sla_calendars WHERE organization_id = $1 AND id = $2`, orgID, id)
	return scanCalendar(row)
}

func (r *SLARepo) ListCalendars(ctx context.Context, orgID shared.ID) ([]*sla.Calendar, error) {
	rows, err := r.db.Querier(ctx).Query(ctx, `
		SELECT id, organization_id, name, timezone, windows, holidays
		FROM sla_calendars WHERE organization_id = $1 ORDER BY name`, orgID)
	if err != nil {
		return nil, translate(err, "sla_calendar")
	}
	defer rows.Close()

	var calendars []*sla.Calendar
	for rows.Next() {
		calendar, err := scanCalendar(rows)
		if err != nil {
			return nil, err
		}
		calendars = append(calendars, calendar)
	}
	return calendars, wrap(rows.Err(), "list calendars")
}

// scanCalendar rebuilds a Calendar through its constructor rather than by
// assigning fields. That way a row written before a validation rule existed is
// rejected on read instead of quietly producing wrong deadlines — and the
// constructor is what resolves the timezone into a *time.Location.
func scanCalendar(row rowScanner) (*sla.Calendar, error) {
	var (
		id, orgID      shared.ID
		name, timezone string
		windowsJSON    []byte
		holidaysJSON   []byte
	)
	if err := row.Scan(&id, &orgID, &name, &timezone, &windowsJSON, &holidaysJSON); err != nil {
		return nil, translate(err, "sla_calendar")
	}

	var records []windowRecord
	if err := json.Unmarshal(windowsJSON, &records); err != nil {
		return nil, shared.Internal("sla.decode_failed", "stored calendar is corrupt").WithCause(err)
	}
	windows := make([]sla.WorkWindow, len(records))
	for i, record := range records {
		windows[i] = sla.WorkWindow{
			Weekday:   time.Weekday(record.Weekday),
			StartMins: record.StartMins,
			EndMins:   record.EndMins,
		}
	}

	var holidayStrings []string
	if len(holidaysJSON) > 0 {
		_ = json.Unmarshal(holidaysJSON, &holidayStrings)
	}
	location, err := time.LoadLocation(timezone)
	if err != nil {
		location = time.UTC
	}
	holidays := make([]time.Time, 0, len(holidayStrings))
	for _, raw := range holidayStrings {
		if day, err := time.ParseInLocation("2006-01-02", raw, location); err == nil {
			holidays = append(holidays, day)
		}
	}

	calendar, err := sla.NewCalendar(orgID, name, timezone, windows, holidays)
	if err != nil {
		return nil, err
	}
	// NewCalendar mints a fresh ID; restore the stored one.
	calendar.ID = id
	return calendar, nil
}

// ---------------------------------------------------------------------------
// Policies
// ---------------------------------------------------------------------------

const policyColumns = `id, organization_id, name, priority, kind, team_id, calendar_id,
	first_response_seconds, resolution_seconds, escalate_after_seconds, active, created_at, updated_at`

func (r *SLARepo) CreatePolicy(ctx context.Context, policy *sla.Policy) error {
	_, err := r.db.Querier(ctx).Exec(ctx, `
		INSERT INTO sla_policies (id, organization_id, name, priority, kind, team_id, calendar_id,
			first_response_seconds, resolution_seconds, escalate_after_seconds, active, created_at, updated_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13)`,
		policy.ID, policy.OrgID, policy.Name, policy.Priority, policy.Kind, policy.TeamID,
		policy.CalendarID, int64(policy.FirstResponse.Seconds()), int64(policy.Resolution.Seconds()),
		secondsPtr(policy.EscalateAfter), policy.Active, policy.CreatedAt, policy.UpdatedAt)
	return translate(err, "sla_policy")
}

func (r *SLARepo) UpdatePolicy(ctx context.Context, policy *sla.Policy) error {
	tag, err := r.db.Querier(ctx).Exec(ctx, `
		UPDATE sla_policies SET name = $3, priority = $4, kind = $5, team_id = $6, calendar_id = $7,
			first_response_seconds = $8, resolution_seconds = $9, escalate_after_seconds = $10,
			active = $11, updated_at = $12
		WHERE organization_id = $1 AND id = $2`,
		policy.OrgID, policy.ID, policy.Name, policy.Priority, policy.Kind, policy.TeamID,
		policy.CalendarID, int64(policy.FirstResponse.Seconds()), int64(policy.Resolution.Seconds()),
		secondsPtr(policy.EscalateAfter), policy.Active, policy.UpdatedAt)
	if err != nil {
		return translate(err, "sla_policy")
	}
	if tag.RowsAffected() == 0 {
		return notFound("sla_policy")
	}
	return nil
}

func (r *SLARepo) ListPolicies(ctx context.Context, orgID shared.ID) ([]*sla.Policy, error) {
	rows, err := r.db.Querier(ctx).Query(ctx,
		`SELECT `+policyColumns+` FROM sla_policies WHERE organization_id = $1 ORDER BY name`, orgID)
	if err != nil {
		return nil, translate(err, "sla_policy")
	}
	defer rows.Close()

	var policies []*sla.Policy
	for rows.Next() {
		var (
			policy                              sla.Policy
			firstResponseSecs, resolutionSecs   int64
			escalateSecs                        *int64
		)
		if err := rows.Scan(&policy.ID, &policy.OrgID, &policy.Name, &policy.Priority,
			&policy.Kind, &policy.TeamID, &policy.CalendarID,
			&firstResponseSecs, &resolutionSecs, &escalateSecs,
			&policy.Active, &policy.CreatedAt, &policy.UpdatedAt); err != nil {
			return nil, translate(err, "sla_policy")
		}
		policy.FirstResponse = time.Duration(firstResponseSecs) * time.Second
		policy.Resolution = time.Duration(resolutionSecs) * time.Second
		if escalateSecs != nil {
			escalateAfter := time.Duration(*escalateSecs) * time.Second
			policy.EscalateAfter = &escalateAfter
		}
		policies = append(policies, &policy)
	}
	return policies, wrap(rows.Err(), "list policies")
}

func secondsPtr(d *time.Duration) *int64 {
	if d == nil {
		return nil
	}
	seconds := int64(d.Seconds())
	return &seconds
}
