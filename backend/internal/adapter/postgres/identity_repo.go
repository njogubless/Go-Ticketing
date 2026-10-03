package postgres

import (
	"context"
	"fmt"
	"strings"

	"github.com/blessnduta/ticketing-system/internal/app"
	"github.com/blessnduta/ticketing-system/internal/domain/identity"
	"github.com/blessnduta/ticketing-system/internal/domain/shared"
	"github.com/blessnduta/ticketing-system/internal/domain/tenant"
	"github.com/blessnduta/ticketing-system/internal/platform/database"
)

// ---------------------------------------------------------------------------
// Organizations
// ---------------------------------------------------------------------------

type OrganizationRepo struct{ db *database.Pool }

func NewOrganizationRepo(db *database.Pool) *OrganizationRepo { return &OrganizationRepo{db: db} }

var _ app.OrganizationRepository = (*OrganizationRepo)(nil)

func (r *OrganizationRepo) Create(ctx context.Context, org *tenant.Organization) error {
	_, err := r.db.Querier(ctx).Exec(ctx, `
		INSERT INTO organizations (id, name, slug, timezone, ticket_prefix, active, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
		org.ID, org.Name, org.Slug, org.Timezone, org.TicketPrefix, org.Active, org.CreatedAt, org.UpdatedAt)
	return translate(err, "organization")
}

const orgColumns = `id, name, slug, timezone, ticket_prefix, active, created_at, updated_at`

func (r *OrganizationRepo) ByID(ctx context.Context, id shared.ID) (*tenant.Organization, error) {
	return r.scanOne(ctx, `SELECT `+orgColumns+` FROM organizations WHERE id = $1`, id)
}

func (r *OrganizationRepo) BySlug(ctx context.Context, slug string) (*tenant.Organization, error) {
	return r.scanOne(ctx, `SELECT `+orgColumns+` FROM organizations WHERE slug = $1`, strings.ToLower(slug))
}

func (r *OrganizationRepo) scanOne(ctx context.Context, query string, args ...any) (*tenant.Organization, error) {
	var org tenant.Organization
	err := r.db.Querier(ctx).QueryRow(ctx, query, args...).Scan(
		&org.ID, &org.Name, &org.Slug, &org.Timezone, &org.TicketPrefix,
		&org.Active, &org.CreatedAt, &org.UpdatedAt)
	if err != nil {
		return nil, translate(err, "organization")
	}
	return &org, nil
}

// NextTicketReference allocates the next human-facing reference.
//
// UPDATE ... RETURNING takes a row-level lock for the duration of the
// transaction, so concurrent creates serialise on that one row and the counter
// is gap-free. A Postgres SEQUENCE would be faster but leaks gaps on rollback,
// and a customer whose ticket numbers jump from 41 to 58 will ask why.
func (r *OrganizationRepo) NextTicketReference(ctx context.Context, orgID shared.ID) (string, error) {
	var prefix string
	var seq int64
	err := r.db.Querier(ctx).QueryRow(ctx, `
		UPDATE organizations
		SET ticket_seq = ticket_seq + 1, updated_at = now()
		WHERE id = $1
		RETURNING ticket_prefix, ticket_seq`, orgID).Scan(&prefix, &seq)
	if err != nil {
		return "", translate(err, "organization")
	}
	return fmt.Sprintf("%s-%d", prefix, seq), nil
}

// ---------------------------------------------------------------------------
// Users
// ---------------------------------------------------------------------------

type UserRepo struct{ db *database.Pool }

func NewUserRepo(db *database.Pool) *UserRepo { return &UserRepo{db: db} }

var _ app.UserRepository = (*UserRepo)(nil)

const userColumns = `id, organization_id, email, password_hash, full_name, role, team_id,
	active, last_login_at, created_at, updated_at`

func (r *UserRepo) Create(ctx context.Context, user *identity.User) error {
	_, err := r.db.Querier(ctx).Exec(ctx, `
		INSERT INTO users (id, organization_id, email, password_hash, full_name, role, team_id,
			active, last_login_at, created_at, updated_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)`,
		user.ID, user.OrgID, user.Email, user.PasswordHash, user.FullName, user.Role,
		user.TeamID, user.Active, user.LastLoginAt, user.CreatedAt, user.UpdatedAt)
	return translate(err, "user")
}

// Update is org-scoped even though the ID is unique, so a bug that passed a
// foreign user's ID cannot write across the tenant boundary.
func (r *UserRepo) Update(ctx context.Context, user *identity.User) error {
	tag, err := r.db.Querier(ctx).Exec(ctx, `
		UPDATE users
		SET email = $3, password_hash = $4, full_name = $5, role = $6, team_id = $7,
			active = $8, last_login_at = $9, updated_at = $10
		WHERE organization_id = $1 AND id = $2`,
		user.OrgID, user.ID, user.Email, user.PasswordHash, user.FullName, user.Role,
		user.TeamID, user.Active, user.LastLoginAt, user.UpdatedAt)
	if err != nil {
		return translate(err, "user")
	}
	if tag.RowsAffected() == 0 {
		return notFound("user")
	}
	return nil
}

func (r *UserRepo) ByID(ctx context.Context, orgID, id shared.ID) (*identity.User, error) {
	return r.scanOne(ctx, `SELECT `+userColumns+` FROM users WHERE organization_id = $1 AND id = $2`, orgID, id)
}

// ByEmail is the one deliberately cross-tenant read in the system: at login we
// do not yet know which organisation the caller belongs to. Email is globally
// unique precisely to make this unambiguous.
func (r *UserRepo) ByEmail(ctx context.Context, email string) (*identity.User, error) {
	return r.scanOne(ctx, `SELECT `+userColumns+` FROM users WHERE email = $1`, strings.ToLower(email))
}

func (r *UserRepo) scanOne(ctx context.Context, query string, args ...any) (*identity.User, error) {
	var user identity.User
	err := r.db.Querier(ctx).QueryRow(ctx, query, args...).Scan(
		&user.ID, &user.OrgID, &user.Email, &user.PasswordHash, &user.FullName,
		&user.Role, &user.TeamID, &user.Active, &user.LastLoginAt,
		&user.CreatedAt, &user.UpdatedAt)
	if err != nil {
		return nil, translate(err, "user")
	}
	return &user, nil
}

func (r *UserRepo) List(ctx context.Context, orgID shared.ID, filter app.UserFilter) ([]*identity.User, error) {
	query := strings.Builder{}
	query.WriteString(`SELECT ` + userColumns + ` FROM users WHERE organization_id = $1`)
	args := []any{orgID}

	if filter.Role != nil {
		args = append(args, *filter.Role)
		fmt.Fprintf(&query, " AND role = $%d", len(args))
	}
	if filter.TeamID != nil {
		args = append(args, *filter.TeamID)
		fmt.Fprintf(&query, " AND team_id = $%d", len(args))
	}
	if filter.Active != nil {
		args = append(args, *filter.Active)
		fmt.Fprintf(&query, " AND active = $%d", len(args))
	}
	if search := strings.TrimSpace(filter.Search); search != "" {
		// ILIKE with a parameterised pattern. The wildcards are added to the
		// *argument*, never concatenated into the SQL, so a search for "%" is
		// a literal percent sign rather than a full-table match.
		args = append(args, "%"+search+"%")
		fmt.Fprintf(&query, " AND (full_name ILIKE $%d OR email ILIKE $%d)", len(args), len(args))
	}
	query.WriteString(" ORDER BY full_name ASC LIMIT 500")

	rows, err := r.db.Querier(ctx).Query(ctx, query.String(), args...)
	if err != nil {
		return nil, translate(err, "user")
	}
	defer rows.Close()

	var users []*identity.User
	for rows.Next() {
		var user identity.User
		if err := rows.Scan(&user.ID, &user.OrgID, &user.Email, &user.PasswordHash,
			&user.FullName, &user.Role, &user.TeamID, &user.Active,
			&user.LastLoginAt, &user.CreatedAt, &user.UpdatedAt); err != nil {
			return nil, translate(err, "user")
		}
		users = append(users, &user)
	}
	return users, wrap(rows.Err(), "list users")
}

func (r *UserRepo) CountInOrg(ctx context.Context, orgID shared.ID) (int, error) {
	var count int
	err := r.db.Querier(ctx).QueryRow(ctx,
		`SELECT count(*) FROM users WHERE organization_id = $1`, orgID).Scan(&count)
	return count, translate(err, "user")
}

// ---------------------------------------------------------------------------
// Teams
// ---------------------------------------------------------------------------

type TeamRepo struct{ db *database.Pool }

func NewTeamRepo(db *database.Pool) *TeamRepo { return &TeamRepo{db: db} }

var _ app.TeamRepository = (*TeamRepo)(nil)

const teamColumns = `id, organization_id, name, description, escalates_to, created_at, updated_at`

func (r *TeamRepo) Create(ctx context.Context, team *identity.Team) error {
	_, err := r.db.Querier(ctx).Exec(ctx, `
		INSERT INTO teams (id, organization_id, name, description, escalates_to, created_at, updated_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7)`,
		team.ID, team.OrgID, team.Name, team.Description, team.EscalatesTo, team.CreatedAt, team.UpdatedAt)
	return translate(err, "team")
}

func (r *TeamRepo) Update(ctx context.Context, team *identity.Team) error {
	tag, err := r.db.Querier(ctx).Exec(ctx, `
		UPDATE teams SET name = $3, description = $4, escalates_to = $5, updated_at = $6
		WHERE organization_id = $1 AND id = $2`,
		team.OrgID, team.ID, team.Name, team.Description, team.EscalatesTo, team.UpdatedAt)
	if err != nil {
		return translate(err, "team")
	}
	if tag.RowsAffected() == 0 {
		return notFound("team")
	}
	return nil
}

func (r *TeamRepo) ByID(ctx context.Context, orgID, id shared.ID) (*identity.Team, error) {
	var team identity.Team
	err := r.db.Querier(ctx).QueryRow(ctx,
		`SELECT `+teamColumns+` FROM teams WHERE organization_id = $1 AND id = $2`, orgID, id).
		Scan(&team.ID, &team.OrgID, &team.Name, &team.Description, &team.EscalatesTo,
			&team.CreatedAt, &team.UpdatedAt)
	if err != nil {
		return nil, translate(err, "team")
	}
	return &team, nil
}

func (r *TeamRepo) List(ctx context.Context, orgID shared.ID) ([]*identity.Team, error) {
	rows, err := r.db.Querier(ctx).Query(ctx,
		`SELECT `+teamColumns+` FROM teams WHERE organization_id = $1 ORDER BY name ASC`, orgID)
	if err != nil {
		return nil, translate(err, "team")
	}
	defer rows.Close()

	var teams []*identity.Team
	for rows.Next() {
		var team identity.Team
		if err := rows.Scan(&team.ID, &team.OrgID, &team.Name, &team.Description,
			&team.EscalatesTo, &team.CreatedAt, &team.UpdatedAt); err != nil {
			return nil, translate(err, "team")
		}
		teams = append(teams, &team)
	}
	return teams, wrap(rows.Err(), "list teams")
}
