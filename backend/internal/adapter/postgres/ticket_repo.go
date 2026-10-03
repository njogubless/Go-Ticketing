package postgres

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/blessnduta/ticketing-system/internal/app"
	"github.com/blessnduta/ticketing-system/internal/domain/shared"
	"github.com/blessnduta/ticketing-system/internal/domain/ticket"
	"github.com/blessnduta/ticketing-system/internal/platform/database"
)

// breachingPredicate is the SQL form of ticket.Ticket.BreachedAt: past its
// resolution deadline, not finished, and not paused.
//
// The paused clause is the one that is easy to leave out and expensive to get
// wrong. A ticket waiting on its requester, or on an approver, has a stopped
// SLA clock — the domain says so, the aggregate enforces it, and the DTO's
// `breached` flag is computed from it. Omitting it here made the dashboard
// report eighteen breaches while the queue displayed one, and the "Breaching
// SLA" view list rows that render without the breach marker.
//
// It is a constant rather than three hand-written copies precisely because the
// three copies had already drifted.
const breachingPredicate = `resolution_due IS NOT NULL
	AND resolution_due < now()
	AND status NOT IN ('resolved','closed','cancelled','pending_requester','pending_approval')`

// prefixPredicate qualifies the bare column names in a predicate with a table
// alias, so the one definition above can also be used inside a join.
func prefixPredicate(predicate, alias string) string {
	for _, column := range []string{"resolution_due", "status"} {
		predicate = strings.ReplaceAll(predicate, column, alias+"."+column)
	}
	return predicate
}

type TicketRepo struct{ db *database.Pool }

func NewTicketRepo(db *database.Pool) *TicketRepo { return &TicketRepo{db: db} }

var _ app.TicketRepository = (*TicketRepo)(nil)

const ticketColumns = `id, organization_id, reference, kind, subject, description, category, tags,
	status, impact, urgency, priority, requester_id, assignee_id, team_id,
	resolution, resolved_at, closed_at, first_response_at, reopen_count,
	sla_policy_id, first_response_due, resolution_due, paused_since,
	first_response_met, resolution_met, breach_notified_at, created_at, updated_at`

func (r *TicketRepo) Create(ctx context.Context, t *ticket.Ticket) error {
	_, err := r.db.Querier(ctx).Exec(ctx, `
		INSERT INTO tickets (id, organization_id, reference, kind, subject, description, category, tags,
			status, impact, urgency, priority, requester_id, assignee_id, team_id,
			resolution, resolved_at, closed_at, first_response_at, reopen_count,
			sla_policy_id, first_response_due, resolution_due, paused_since,
			first_response_met, resolution_met, breach_notified_at, created_at, updated_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,
		        $21,$22,$23,$24,$25,$26,$27,$28,$29)`,
		t.ID, t.OrgID, t.Reference, t.Kind, t.Subject, t.Description, t.Category, t.Tags,
		t.Status, t.Impact, t.Urgency, t.Priority, t.RequesterID, t.AssigneeID, t.TeamID,
		t.Resolution, t.ResolvedAt, t.ClosedAt, t.FirstRespAt, t.ReopenCount,
		t.SLAPolicyID, t.FirstResponseDue, t.ResolutionDue, t.PausedSince,
		t.FirstResponseMet, t.ResolutionMet, t.BreachNotifiedAt, t.CreatedAt, t.UpdatedAt)
	return translate(err, "ticket")
}

// Update writes the aggregate, guarded by optimistic locking.
//
// The WHERE clause includes updated_at = expectedUpdatedAt, so a write based on
// a stale read affects zero rows and is reported as a conflict. On a service
// desk two agents opening the same ticket is routine, and last-write-wins would
// silently discard one of them — including, potentially, a resolution note.
//
// A zero expectedUpdatedAt means "the caller loaded this inside the current
// transaction and no concurrent write is possible", which is the case for
// service methods that read and write within one WithinTx.
func (r *TicketRepo) Update(ctx context.Context, t *ticket.Ticket, expectedUpdatedAt time.Time) error {
	query := `
		UPDATE tickets SET
			kind = $3, subject = $4, description = $5, category = $6, tags = $7,
			status = $8, impact = $9, urgency = $10, priority = $11,
			requester_id = $12, assignee_id = $13, team_id = $14,
			resolution = $15, resolved_at = $16, closed_at = $17, first_response_at = $18,
			reopen_count = $19, sla_policy_id = $20, first_response_due = $21,
			resolution_due = $22, paused_since = $23, first_response_met = $24,
			resolution_met = $25, breach_notified_at = $26, updated_at = $27
		WHERE organization_id = $1 AND id = $2`

	args := []any{
		t.OrgID, t.ID, t.Kind, t.Subject, t.Description, t.Category, t.Tags,
		t.Status, t.Impact, t.Urgency, t.Priority, t.RequesterID, t.AssigneeID, t.TeamID,
		t.Resolution, t.ResolvedAt, t.ClosedAt, t.FirstRespAt, t.ReopenCount,
		t.SLAPolicyID, t.FirstResponseDue, t.ResolutionDue, t.PausedSince,
		t.FirstResponseMet, t.ResolutionMet, t.BreachNotifiedAt, t.UpdatedAt,
	}

	if !expectedUpdatedAt.IsZero() {
		args = append(args, expectedUpdatedAt)
		query += fmt.Sprintf(" AND updated_at = $%d", len(args))
	}

	tag, err := r.db.Querier(ctx).Exec(ctx, query, args...)
	if err != nil {
		return translate(err, "ticket")
	}
	if tag.RowsAffected() == 0 {
		if expectedUpdatedAt.IsZero() {
			return notFound("ticket")
		}
		// Distinguish "gone" from "changed": both match zero rows, but they
		// mean different things to the agent staring at the screen.
		var exists bool
		if err := r.db.Querier(ctx).QueryRow(ctx,
			`SELECT EXISTS (SELECT 1 FROM tickets WHERE organization_id = $1 AND id = $2)`,
			t.OrgID, t.ID).Scan(&exists); err == nil && !exists {
			return notFound("ticket")
		}
		return conflict("ticket")
	}
	return nil
}

func (r *TicketRepo) ByID(ctx context.Context, orgID, id shared.ID) (*ticket.Ticket, error) {
	return r.scanOne(ctx,
		`SELECT `+ticketColumns+` FROM tickets WHERE organization_id = $1 AND id = $2`, orgID, id)
}

func (r *TicketRepo) ByReference(ctx context.Context, orgID shared.ID, reference string) (*ticket.Ticket, error) {
	return r.scanOne(ctx,
		`SELECT `+ticketColumns+` FROM tickets WHERE organization_id = $1 AND reference = $2`,
		orgID, strings.ToUpper(strings.TrimSpace(reference)))
}

func (r *TicketRepo) scanOne(ctx context.Context, query string, args ...any) (*ticket.Ticket, error) {
	row := r.db.Querier(ctx).QueryRow(ctx, query, args...)
	t, err := scanTicket(row)
	if err != nil {
		return nil, translate(err, "ticket")
	}
	return t, nil
}

// rowScanner unifies pgx.Row and pgx.Rows so one scan function serves both the
// single-row and multi-row paths. Duplicating a 29-column scan is how a new
// column ends up populated in one path and silently zero in the other.
type rowScanner interface{ Scan(dest ...any) error }

func scanTicket(row rowScanner) (*ticket.Ticket, error) {
	var t ticket.Ticket
	err := row.Scan(
		&t.ID, &t.OrgID, &t.Reference, &t.Kind, &t.Subject, &t.Description, &t.Category, &t.Tags,
		&t.Status, &t.Impact, &t.Urgency, &t.Priority, &t.RequesterID, &t.AssigneeID, &t.TeamID,
		&t.Resolution, &t.ResolvedAt, &t.ClosedAt, &t.FirstRespAt, &t.ReopenCount,
		&t.SLAPolicyID, &t.FirstResponseDue, &t.ResolutionDue, &t.PausedSince,
		&t.FirstResponseMet, &t.ResolutionMet, &t.BreachNotifiedAt, &t.CreatedAt, &t.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return &t, nil
}

// ---------------------------------------------------------------------------
// Search — the hottest endpoint in the system
// ---------------------------------------------------------------------------

// Search builds a parameterised query from the filter set.
//
// Every value is bound, never interpolated. The only strings that reach the SQL
// text are from closed vocabularies this package controls (sort keys, column
// names), so there is no path from user input into the query structure.
func (r *TicketRepo) Search(ctx context.Context, orgID shared.ID, query app.TicketQuery) (shared.Page[*ticket.Ticket], error) {
	var (
		sql   strings.Builder
		args  []any
		empty = shared.Page[*ticket.Ticket]{}
	)

	args = append(args, orgID)
	sql.WriteString(`SELECT ` + ticketColumns + ` FROM tickets WHERE organization_id = $1`)

	// --- authorisation scope, applied before any user filter ----------------
	// Written first so it can never be accidentally OR-ed into a user-supplied
	// condition: everything that follows is AND-ed onto this.
	if !query.Scope.All {
		var clauses []string
		if query.Scope.RequesterID != nil {
			args = append(args, *query.Scope.RequesterID)
			clauses = append(clauses, fmt.Sprintf("requester_id = $%d", len(args)))
		}
		if query.Scope.TeamID != nil {
			args = append(args, *query.Scope.TeamID)
			clauses = append(clauses, fmt.Sprintf("team_id = $%d", len(args)))
			// An agent keeps sight of tickets assigned to them personally even
			// after the ticket is routed to another team.
			args = append(args, *query.Scope.RequesterID)
			clauses = append(clauses, fmt.Sprintf("assignee_id = $%d", len(args)))
		}
		if query.Scope.IncludeUnrouted {
			clauses = append(clauses, "team_id IS NULL")
		}
		if len(clauses) == 0 {
			// A scope with no clauses would return the whole organisation.
			// Fail closed instead: an actor we cannot scope sees nothing.
			return empty, nil
		}
		sql.WriteString(" AND (" + strings.Join(clauses, " OR ") + ")")
	}

	// --- user-supplied filters ---------------------------------------------
	if len(query.Statuses) > 0 {
		args = append(args, statusStrings(query.Statuses))
		fmt.Fprintf(&sql, " AND status = ANY($%d)", len(args))
	}
	if len(query.Priorities) > 0 {
		args = append(args, priorityStrings(query.Priorities))
		fmt.Fprintf(&sql, " AND priority = ANY($%d)", len(args))
	}
	if len(query.Kinds) > 0 {
		args = append(args, kindStrings(query.Kinds))
		fmt.Fprintf(&sql, " AND kind = ANY($%d)", len(args))
	}
	if len(query.TeamIDs) > 0 {
		args = append(args, query.TeamIDs)
		fmt.Fprintf(&sql, " AND team_id = ANY($%d)", len(args))
	}
	if query.Unassigned {
		sql.WriteString(" AND assignee_id IS NULL")
	} else if query.AssigneeID != nil {
		args = append(args, *query.AssigneeID)
		fmt.Fprintf(&sql, " AND assignee_id = $%d", len(args))
	}
	if query.RequesterID != nil {
		args = append(args, *query.RequesterID)
		fmt.Fprintf(&sql, " AND requester_id = $%d", len(args))
	}
	if len(query.Tags) > 0 {
		args = append(args, query.Tags)
		// @> is the GIN-indexed containment operator: "has all of these tags".
		fmt.Fprintf(&sql, " AND tags @> $%d", len(args))
	}
	if query.AssetID != nil {
		args = append(args, *query.AssetID)
		fmt.Fprintf(&sql, ` AND EXISTS (SELECT 1 FROM ticket_assets ta
			WHERE ta.ticket_id = tickets.id AND ta.asset_id = $%d)`, len(args))
	}
	if text := strings.TrimSpace(query.Text); text != "" {
		args = append(args, text)
		// websearch_to_tsquery parses what users actually type — quoted
		// phrases, OR, leading minus — and never errors on malformed input.
		// plainto_tsquery would discard the operators; to_tsquery would throw
		// a syntax error on a stray colon and turn a typo into a 500.
		fmt.Fprintf(&sql, " AND search_vector @@ websearch_to_tsquery('english', $%d)", len(args))
	}
	if query.BreachedOnly {
		sql.WriteString(" AND " + breachingPredicate)
	}
	if query.DueBefore != nil {
		args = append(args, *query.DueBefore)
		fmt.Fprintf(&sql, " AND resolution_due IS NOT NULL AND resolution_due < $%d", len(args))
	}
	if query.CreatedAfter != nil {
		args = append(args, *query.CreatedAfter)
		fmt.Fprintf(&sql, " AND created_at >= $%d", len(args))
	}

	// --- keyset pagination --------------------------------------------------
	// The cursor comparison must match the sort direction, so it is emitted
	// alongside the ORDER BY rather than independently.
	orderBy, cursorClause := sortClause(query.Sort)
	if query.Page.Cursor != nil {
		args = append(args, *query.Page.Cursor)
		fmt.Fprintf(&sql, cursorClause, len(args))
	}
	sql.WriteString(" ORDER BY " + orderBy)

	// Over-fetch by one to detect whether another page exists without a
	// second COUNT query — a COUNT over a filtered ticket table is the single
	// most expensive thing a list endpoint can do.
	limit := query.Page.NormalisedLimit()
	args = append(args, limit+1)
	fmt.Fprintf(&sql, " LIMIT $%d", len(args))

	rows, err := r.db.Querier(ctx).Query(ctx, sql.String(), args...)
	if err != nil {
		return empty, translate(err, "ticket")
	}
	defer rows.Close()

	var tickets []*ticket.Ticket
	for rows.Next() {
		t, err := scanTicket(rows)
		if err != nil {
			return empty, translate(err, "ticket")
		}
		tickets = append(tickets, t)
	}
	if err := rows.Err(); err != nil {
		return empty, translate(err, "ticket")
	}

	return shared.NewPage(tickets, limit, func(t *ticket.Ticket) shared.ID { return t.ID }), nil
}

// sortClause returns the ORDER BY and the matching keyset predicate.
//
// Every ordering ends in `id` so it is total: without a unique tiebreaker,
// rows with equal priority could be returned in a different order on each page
// request, which makes keyset pagination skip and repeat rows.
func sortClause(sort app.TicketSort) (orderBy, cursorFormat string) {
	switch sort {
	case app.SortOldest:
		return "id ASC", " AND id > $%d"
	case app.SortPriority:
		// NULLS LAST on resolution_due keeps SLA-less tickets from crowding
		// the top of a queue sorted by urgency.
		return "priority ASC, resolution_due ASC NULLS LAST, id DESC", " AND id < $%d"
	case app.SortDueSoonest:
		return "resolution_due ASC NULLS LAST, id DESC", " AND id < $%d"
	case app.SortUpdated:
		return "updated_at DESC, id DESC", " AND id < $%d"
	default: // SortNewest
		return "id DESC", " AND id < $%d"
	}
}

// DueForBreachCheck feeds the SLA worker. The predicate deliberately mirrors
// the partial index in migration 0001 exactly — if the two drift, the worker
// silently falls back to a sequential scan over every ticket ever filed.
func (r *TicketRepo) DueForBreachCheck(ctx context.Context, before time.Time, limit int) ([]*ticket.Ticket, error) {
	rows, err := r.db.Querier(ctx).Query(ctx, `
		SELECT `+ticketColumns+` FROM tickets
		WHERE status NOT IN ('resolved','closed','cancelled','pending_requester','pending_approval')
		  AND resolution_due IS NOT NULL
		  AND breach_notified_at IS NULL
		  AND resolution_due < $1
		ORDER BY resolution_due ASC
		LIMIT $2`, before, limit)
	if err != nil {
		return nil, translate(err, "ticket")
	}
	defer rows.Close()

	var tickets []*ticket.Ticket
	for rows.Next() {
		t, err := scanTicket(rows)
		if err != nil {
			return nil, translate(err, "ticket")
		}
		tickets = append(tickets, t)
	}
	return tickets, wrap(rows.Err(), "breach sweep")
}

// ---------------------------------------------------------------------------
// Reporting
// ---------------------------------------------------------------------------

// Stats computes the dashboard in a handful of aggregate queries rather than
// loading tickets into Go and counting them there. The database is far better
// at this, and the alternative pulls a month of tickets across the wire to
// produce eight integers.
func (r *TicketRepo) Stats(ctx context.Context, orgID shared.ID, window app.ReportWindow) (app.TicketStats, error) {
	stats := app.TicketStats{
		ByStatus:   map[ticket.Status]int{},
		ByPriority: map[ticket.Priority]int{},
		ByKind:     map[ticket.Kind]int{},
	}

	teamFilter := ""
	args := []any{orgID, window.From, window.To}
	if window.TeamID != nil {
		args = append(args, *window.TeamID)
		teamFilter = fmt.Sprintf(" AND team_id = $%d", len(args))
	}

	// percentile_cont returns double precision, which cannot be scanned
	// straight into a time.Duration. Land it in a float and convert once.
	var medianSeconds, p90Seconds float64

	// --- headline counters + SLA attainment + resolution percentiles --------
	// percentile_cont over a FILTERed set gives p50/p90 in the same pass as
	// the counters, so the whole summary is one scan of one index range.
	err := r.db.Querier(ctx).QueryRow(ctx, `
		SELECT
			count(*) FILTER (WHERE created_at BETWEEN $2 AND $3),
			count(*) FILTER (WHERE resolved_at BETWEEN $2 AND $3),
			count(*) FILTER (WHERE reopen_count > 0 AND created_at BETWEEN $2 AND $3),
			count(*) FILTER (WHERE status NOT IN ('resolved','closed','cancelled')),
			count(*) FILTER (WHERE `+breachingPredicate+`),
			count(*) FILTER (WHERE first_response_met IS NOT NULL AND created_at BETWEEN $2 AND $3),
			count(*) FILTER (WHERE first_response_met AND created_at BETWEEN $2 AND $3),
			count(*) FILTER (WHERE resolution_met IS NOT NULL AND created_at BETWEEN $2 AND $3),
			count(*) FILTER (WHERE resolution_met AND created_at BETWEEN $2 AND $3),
			coalesce(percentile_cont(0.5) WITHIN GROUP (
				ORDER BY EXTRACT(EPOCH FROM (resolved_at - created_at))
			) FILTER (WHERE resolved_at BETWEEN $2 AND $3), 0),
			coalesce(percentile_cont(0.9) WITHIN GROUP (
				ORDER BY EXTRACT(EPOCH FROM (resolved_at - created_at))
			) FILTER (WHERE resolved_at BETWEEN $2 AND $3), 0)
		FROM tickets
		WHERE organization_id = $1`+teamFilter, args...).
		Scan(&stats.Created, &stats.Resolved, &stats.Reopened, &stats.Open, &stats.Breached,
			&stats.FirstResponseTotal, &stats.FirstResponseMet,
			&stats.ResolutionTotal, &stats.ResolutionMet,
			&medianSeconds, &p90Seconds)
	if err != nil {
		return stats, translate(err, "report")
	}
	stats.MedianResolution = time.Duration(medianSeconds * float64(time.Second))
	stats.P90Resolution = time.Duration(p90Seconds * float64(time.Second))

	if err := r.loadBreakdowns(ctx, &stats, teamFilter, args); err != nil {
		return stats, err
	}
	if err := r.loadVolume(ctx, &stats, teamFilter, args); err != nil {
		return stats, err
	}
	if err := r.loadAgentLoad(ctx, &stats, orgID, window); err != nil {
		return stats, err
	}
	return stats, nil
}

func (r *TicketRepo) loadBreakdowns(ctx context.Context, stats *app.TicketStats, teamFilter string, args []any) error {
	// One query, three groupings, distinguished by a discriminator column.
	// Three round trips would be clearer but this is on a dashboard load path
	// where each round trip is a visible fraction of the render time.
	rows, err := r.db.Querier(ctx).Query(ctx, `
		SELECT 'status' AS dimension, status AS value, count(*)
		FROM tickets WHERE organization_id = $1 AND created_at BETWEEN $2 AND $3`+teamFilter+`
		GROUP BY status
		UNION ALL
		SELECT 'priority', priority, count(*)
		FROM tickets WHERE organization_id = $1 AND created_at BETWEEN $2 AND $3`+teamFilter+`
		GROUP BY priority
		UNION ALL
		SELECT 'kind', kind, count(*)
		FROM tickets WHERE organization_id = $1 AND created_at BETWEEN $2 AND $3`+teamFilter+`
		GROUP BY kind`, args...)
	if err != nil {
		return translate(err, "report")
	}
	defer rows.Close()

	for rows.Next() {
		var dimension, value string
		var count int
		if err := rows.Scan(&dimension, &value, &count); err != nil {
			return translate(err, "report")
		}
		switch dimension {
		case "status":
			stats.ByStatus[ticket.Status(value)] = count
		case "priority":
			stats.ByPriority[ticket.Priority(value)] = count
		case "kind":
			stats.ByKind[ticket.Kind(value)] = count
		}
	}
	return wrap(rows.Err(), "report breakdowns")
}

func (r *TicketRepo) loadVolume(ctx context.Context, stats *app.TicketStats, teamFilter string, args []any) error {
	// generate_series gives a row per day even when nothing happened, so the
	// trend chart shows a genuine zero rather than skipping the day and
	// implying continuity that is not there.
	//
	// Created and resolved counts are aggregated in separate CTEs and then
	// joined. Joining the tickets table twice against the day series instead
	// would multiply the rows — a day with 3 created and 4 resolved would
	// produce 12 rows and report 12 of each.
	rows, err := r.db.Querier(ctx).Query(ctx, `
		WITH days AS (
			SELECT generate_series(date_trunc('day', $2::timestamptz),
			                       date_trunc('day', $3::timestamptz),
			                       '1 day')::date AS day
		),
		created AS (
			SELECT date_trunc('day', created_at)::date AS day, count(*) AS n
			FROM tickets
			WHERE organization_id = $1 AND created_at BETWEEN $2 AND $3`+teamFilter+`
			GROUP BY 1
		),
		resolved AS (
			SELECT date_trunc('day', resolved_at)::date AS day, count(*) AS n
			FROM tickets
			WHERE organization_id = $1 AND resolved_at BETWEEN $2 AND $3`+teamFilter+`
			GROUP BY 1
		)
		SELECT days.day,
		       coalesce(created.n, 0),
		       coalesce(resolved.n, 0)
		FROM days
		LEFT JOIN created  ON created.day  = days.day
		LEFT JOIN resolved ON resolved.day = days.day
		ORDER BY days.day`, args...)
	if err != nil {
		return translate(err, "report")
	}
	defer rows.Close()

	for rows.Next() {
		var point app.DailyCount
		if err := rows.Scan(&point.Day, &point.Created, &point.Resolved); err != nil {
			return translate(err, "report")
		}
		stats.Volume = append(stats.Volume, point)
	}
	return wrap(rows.Err(), "report volume")
}

func (r *TicketRepo) loadAgentLoad(ctx context.Context, stats *app.TicketStats, orgID shared.ID, window app.ReportWindow) error {
	args := []any{orgID}
	teamFilter := ""
	if window.TeamID != nil {
		args = append(args, *window.TeamID)
		teamFilter = fmt.Sprintf(" AND t.team_id = $%d", len(args))
	}

	rows, err := r.db.Querier(ctx).Query(ctx, `
		SELECT u.id, u.full_name,
		       count(*) FILTER (WHERE t.status NOT IN ('resolved','closed','cancelled')),
		       count(*) FILTER (WHERE `+prefixPredicate(breachingPredicate, "t")+`)
		FROM tickets t
		JOIN users u ON u.id = t.assignee_id AND u.organization_id = t.organization_id
		WHERE t.organization_id = $1`+teamFilter+`
		GROUP BY u.id, u.full_name
		HAVING count(*) FILTER (WHERE t.status NOT IN ('resolved','closed','cancelled')) > 0
		ORDER BY 3 DESC
		LIMIT 50`, args...)
	if err != nil {
		return translate(err, "report")
	}
	defer rows.Close()

	for rows.Next() {
		var load app.AgentLoad
		if err := rows.Scan(&load.UserID, &load.FullName, &load.Open, &load.Breached); err != nil {
			return translate(err, "report")
		}
		stats.AgentLoad = append(stats.AgentLoad, load)
	}
	return wrap(rows.Err(), "report agent load")
}

// ---------------------------------------------------------------------------
// Enum-slice conversions
//
// pgx binds []string to a Postgres text[] cleanly; it cannot bind a slice of a
// named string type. These converters keep that driver detail out of the
// callers.
// ---------------------------------------------------------------------------

func statusStrings(values []ticket.Status) []string {
	out := make([]string, len(values))
	for i, value := range values {
		out[i] = string(value)
	}
	return out
}

func priorityStrings(values []ticket.Priority) []string {
	out := make([]string, len(values))
	for i, value := range values {
		out[i] = string(value)
	}
	return out
}

func kindStrings(values []ticket.Kind) []string {
	out := make([]string, len(values))
	for i, value := range values {
		out[i] = string(value)
	}
	return out
}
