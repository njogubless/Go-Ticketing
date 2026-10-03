package postgres

import (
	"context"
	"encoding/json"
	"time"

	"github.com/blessnduta/ticketing-system/internal/app"
	"github.com/blessnduta/ticketing-system/internal/domain/shared"
	"github.com/blessnduta/ticketing-system/internal/platform/database"
)

type RefreshTokenRepo struct{ db *database.Pool }

func NewRefreshTokenRepo(db *database.Pool) *RefreshTokenRepo { return &RefreshTokenRepo{db: db} }

var _ app.RefreshTokenRepository = (*RefreshTokenRepo)(nil)

func (r *RefreshTokenRepo) Store(ctx context.Context, token app.StoredRefreshToken) error {
	_, err := r.db.Querier(ctx).Exec(ctx, `
		INSERT INTO refresh_tokens (token_hash, user_id, organization_id, family_id, expires_at, created_at)
		VALUES ($1,$2,$3,$4,$5,$6)`,
		token.TokenHash, token.UserID, token.OrgID, token.FamilyID, token.ExpiresAt, token.CreatedAt)
	return translate(err, "refresh_token")
}

// Consume marks a token used and returns its prior state atomically.
//
// The RETURNING clause reports consumed_at as it was *before* this statement,
// which is what makes replay detection work: the caller can tell "I just
// consumed a fresh token" from "this token had already been consumed", and
// those two cases demand opposite responses.
func (r *RefreshTokenRepo) Consume(ctx context.Context, tokenHash string, now time.Time) (app.StoredRefreshToken, error) {
	var token app.StoredRefreshToken
	err := r.db.Querier(ctx).QueryRow(ctx, `
		WITH prior AS (
			SELECT token_hash, consumed_at FROM refresh_tokens WHERE token_hash = $1 FOR UPDATE
		)
		UPDATE refresh_tokens rt
		SET consumed_at = COALESCE(rt.consumed_at, $2)
		FROM prior
		WHERE rt.token_hash = prior.token_hash
		RETURNING rt.token_hash, rt.user_id, rt.organization_id, rt.family_id,
		          rt.expires_at, prior.consumed_at, rt.revoked_at, rt.created_at`,
		tokenHash, now).
		Scan(&token.TokenHash, &token.UserID, &token.OrgID, &token.FamilyID,
			&token.ExpiresAt, &token.ConsumedAt, &token.RevokedAt, &token.CreatedAt)
	if err != nil {
		return app.StoredRefreshToken{}, translate(err, "refresh_token")
	}
	return token, nil
}

func (r *RefreshTokenRepo) RevokeFamily(ctx context.Context, familyID shared.ID, now time.Time) error {
	_, err := r.db.Querier(ctx).Exec(ctx,
		`UPDATE refresh_tokens SET revoked_at = $2 WHERE family_id = $1 AND revoked_at IS NULL`,
		familyID, now)
	return translate(err, "refresh_token")
}

func (r *RefreshTokenRepo) RevokeAllForUser(ctx context.Context, userID shared.ID, now time.Time) error {
	_, err := r.db.Querier(ctx).Exec(ctx,
		`UPDATE refresh_tokens SET revoked_at = $2 WHERE user_id = $1 AND revoked_at IS NULL`,
		userID, now)
	return translate(err, "refresh_token")
}

// DeleteExpired is called by a janitor goroutine. Without it the table grows
// without bound — one row per login per device, forever.
func (r *RefreshTokenRepo) DeleteExpired(ctx context.Context, before time.Time) (int64, error) {
	tag, err := r.db.Querier(ctx).Exec(ctx,
		`DELETE FROM refresh_tokens WHERE expires_at < $1`, before)
	if err != nil {
		return 0, translate(err, "refresh_token")
	}
	return tag.RowsAffected(), nil
}

// ---------------------------------------------------------------------------
// Saved views
// ---------------------------------------------------------------------------

type SavedViewRepo struct{ db *database.Pool }

func NewSavedViewRepo(db *database.Pool) *SavedViewRepo { return &SavedViewRepo{db: db} }

var _ app.SavedViewRepository = (*SavedViewRepo)(nil)

func (r *SavedViewRepo) Create(ctx context.Context, view *app.SavedView) error {
	filterJSON, err := json.Marshal(view.Filter)
	if err != nil {
		return shared.Internal("view.encode_failed", "could not encode filter").WithCause(err)
	}
	_, err = r.db.Querier(ctx).Exec(ctx, `
		INSERT INTO saved_views (id, organization_id, owner_id, name, shared, filter, created_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7)`,
		view.ID, view.OrgID, view.OwnerID, view.Name, view.Shared, filterJSON, view.CreatedAt)
	return translate(err, "view")
}

// Delete is scoped by owner as well as by organisation: a shared view is
// readable by the team but removable only by the person who made it.
func (r *SavedViewRepo) Delete(ctx context.Context, orgID, ownerID, id shared.ID) error {
	tag, err := r.db.Querier(ctx).Exec(ctx,
		`DELETE FROM saved_views WHERE organization_id = $1 AND owner_id = $2 AND id = $3`,
		orgID, ownerID, id)
	if err != nil {
		return translate(err, "view")
	}
	if tag.RowsAffected() == 0 {
		return notFound("view")
	}
	return nil
}

// List returns the caller's own views plus any shared by colleagues.
func (r *SavedViewRepo) List(ctx context.Context, orgID, ownerID shared.ID) ([]*app.SavedView, error) {
	rows, err := r.db.Querier(ctx).Query(ctx, `
		SELECT id, organization_id, owner_id, name, shared, filter, created_at
		FROM saved_views
		WHERE organization_id = $1 AND (owner_id = $2 OR shared = TRUE)
		ORDER BY name`, orgID, ownerID)
	if err != nil {
		return nil, translate(err, "view")
	}
	defer rows.Close()

	var views []*app.SavedView
	for rows.Next() {
		view, err := scanSavedView(rows)
		if err != nil {
			return nil, err
		}
		views = append(views, view)
	}
	return views, wrap(rows.Err(), "list views")
}

func (r *SavedViewRepo) ByID(ctx context.Context, orgID, id shared.ID) (*app.SavedView, error) {
	row := r.db.Querier(ctx).QueryRow(ctx, `
		SELECT id, organization_id, owner_id, name, shared, filter, created_at
		FROM saved_views WHERE organization_id = $1 AND id = $2`, orgID, id)
	return scanSavedView(row)
}

func scanSavedView(row rowScanner) (*app.SavedView, error) {
	var (
		view       app.SavedView
		filterJSON []byte
	)
	if err := row.Scan(&view.ID, &view.OrgID, &view.OwnerID, &view.Name,
		&view.Shared, &filterJSON, &view.CreatedAt); err != nil {
		return nil, translate(err, "view")
	}
	if err := json.Unmarshal(filterJSON, &view.Filter); err != nil {
		return nil, shared.Internal("view.decode_failed", "stored view is corrupt").WithCause(err)
	}
	// Belt and braces: a stored filter must never carry an authorisation
	// scope. Even if one were somehow persisted, it is discarded here and
	// recomputed from the viewing actor.
	view.Filter.Scope = app.VisibilityScope{}
	return &view, nil
}
