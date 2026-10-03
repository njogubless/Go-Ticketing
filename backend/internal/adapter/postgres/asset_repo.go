package postgres

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/blessnduta/ticketing-system/internal/app"
	"github.com/blessnduta/ticketing-system/internal/domain/asset"
	"github.com/blessnduta/ticketing-system/internal/domain/shared"
	"github.com/blessnduta/ticketing-system/internal/platform/database"
)

type AssetRepo struct{ db *database.Pool }

func NewAssetRepo(db *database.Pool) *AssetRepo { return &AssetRepo{db: db} }

var _ app.AssetRepository = (*AssetRepo)(nil)

const assetColumns = `id, organization_id, tag, name, kind, status, criticality,
	manufacturer, model, serial_number, location, owner_id, parent_id,
	purchased_at, warranty_until, created_at, updated_at`

func (r *AssetRepo) Create(ctx context.Context, a *asset.Asset) error {
	_, err := r.db.Querier(ctx).Exec(ctx, `
		INSERT INTO assets (id, organization_id, tag, name, kind, status, criticality,
			manufacturer, model, serial_number, location, owner_id, parent_id,
			purchased_at, warranty_until, created_at, updated_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17)`,
		a.ID, a.OrgID, a.Tag, a.Name, a.Kind, a.Status, a.Criticality,
		a.Manufacturer, a.Model, a.SerialNumber, a.Location, a.OwnerID, a.ParentID,
		a.PurchasedAt, a.WarrantyUntil, a.CreatedAt, a.UpdatedAt)
	return translate(err, "asset")
}

func (r *AssetRepo) Update(ctx context.Context, a *asset.Asset) error {
	tag, err := r.db.Querier(ctx).Exec(ctx, `
		UPDATE assets SET tag = $3, name = $4, kind = $5, status = $6, criticality = $7,
			manufacturer = $8, model = $9, serial_number = $10, location = $11,
			owner_id = $12, parent_id = $13, purchased_at = $14, warranty_until = $15, updated_at = $16
		WHERE organization_id = $1 AND id = $2`,
		a.OrgID, a.ID, a.Tag, a.Name, a.Kind, a.Status, a.Criticality,
		a.Manufacturer, a.Model, a.SerialNumber, a.Location, a.OwnerID, a.ParentID,
		a.PurchasedAt, a.WarrantyUntil, a.UpdatedAt)
	if err != nil {
		return translate(err, "asset")
	}
	if tag.RowsAffected() == 0 {
		return notFound("asset")
	}
	return nil
}

func (r *AssetRepo) ByID(ctx context.Context, orgID, id shared.ID) (*asset.Asset, error) {
	row := r.db.Querier(ctx).QueryRow(ctx,
		`SELECT `+assetColumns+` FROM assets WHERE organization_id = $1 AND id = $2`, orgID, id)
	a, err := scanAsset(row)
	if err != nil {
		return nil, translate(err, "asset")
	}
	return a, nil
}

func scanAsset(row rowScanner) (*asset.Asset, error) {
	var a asset.Asset
	err := row.Scan(&a.ID, &a.OrgID, &a.Tag, &a.Name, &a.Kind, &a.Status, &a.Criticality,
		&a.Manufacturer, &a.Model, &a.SerialNumber, &a.Location, &a.OwnerID, &a.ParentID,
		&a.PurchasedAt, &a.WarrantyUntil, &a.CreatedAt, &a.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return &a, nil
}

func (r *AssetRepo) List(ctx context.Context, orgID shared.ID, filter app.AssetFilter) (shared.Page[*asset.Asset], error) {
	var sql strings.Builder
	args := []any{orgID}
	empty := shared.Page[*asset.Asset]{}

	sql.WriteString(`SELECT ` + assetColumns + ` FROM assets WHERE organization_id = $1`)

	if filter.Kind != nil {
		args = append(args, *filter.Kind)
		fmt.Fprintf(&sql, " AND kind = $%d", len(args))
	}
	if filter.Status != nil {
		args = append(args, *filter.Status)
		fmt.Fprintf(&sql, " AND status = $%d", len(args))
	}
	if filter.Criticality != nil {
		args = append(args, *filter.Criticality)
		fmt.Fprintf(&sql, " AND criticality = $%d", len(args))
	}
	if filter.OwnerID != nil {
		args = append(args, *filter.OwnerID)
		fmt.Fprintf(&sql, " AND owner_id = $%d", len(args))
	}
	if search := strings.TrimSpace(filter.Search); search != "" {
		args = append(args, "%"+search+"%")
		fmt.Fprintf(&sql, " AND (tag ILIKE $%d OR name ILIKE $%d OR serial_number ILIKE $%d)",
			len(args), len(args), len(args))
	}
	if filter.Page.Cursor != nil {
		args = append(args, *filter.Page.Cursor)
		fmt.Fprintf(&sql, " AND id > $%d", len(args))
	}

	limit := filter.Page.NormalisedLimit()
	args = append(args, limit+1)
	fmt.Fprintf(&sql, " ORDER BY id ASC LIMIT $%d", len(args))

	rows, err := r.db.Querier(ctx).Query(ctx, sql.String(), args...)
	if err != nil {
		return empty, translate(err, "asset")
	}
	defer rows.Close()

	var assets []*asset.Asset
	for rows.Next() {
		a, err := scanAsset(rows)
		if err != nil {
			return empty, translate(err, "asset")
		}
		assets = append(assets, a)
	}
	if err := rows.Err(); err != nil {
		return empty, translate(err, "asset")
	}
	return shared.NewPage(assets, limit, func(a *asset.Asset) shared.ID { return a.ID }), nil
}

// LinkToTicket is idempotent: linking an already-linked asset is a no-op
// rather than an error, because the caller's intent ("these are related") is
// already satisfied and failing would make retries unsafe.
func (r *AssetRepo) LinkToTicket(ctx context.Context, orgID, ticketID, assetID shared.ID) error {
	_, err := r.db.Querier(ctx).Exec(ctx, `
		INSERT INTO ticket_assets (organization_id, ticket_id, asset_id)
		VALUES ($1,$2,$3)
		ON CONFLICT (ticket_id, asset_id) DO NOTHING`, orgID, ticketID, assetID)
	return translate(err, "asset_link")
}

func (r *AssetRepo) UnlinkFromTicket(ctx context.Context, orgID, ticketID, assetID shared.ID) error {
	_, err := r.db.Querier(ctx).Exec(ctx, `
		DELETE FROM ticket_assets
		WHERE organization_id = $1 AND ticket_id = $2 AND asset_id = $3`, orgID, ticketID, assetID)
	return translate(err, "asset_link")
}

func (r *AssetRepo) ListForTicket(ctx context.Context, orgID, ticketID shared.ID) ([]*asset.Asset, error) {
	rows, err := r.db.Querier(ctx).Query(ctx, `
		SELECT `+prefixColumns(assetColumns, "a")+`
		FROM assets a
		JOIN ticket_assets ta ON ta.asset_id = a.id AND ta.organization_id = a.organization_id
		WHERE a.organization_id = $1 AND ta.ticket_id = $2
		ORDER BY a.tag`, orgID, ticketID)
	if err != nil {
		return nil, translate(err, "asset")
	}
	defer rows.Close()

	var assets []*asset.Asset
	for rows.Next() {
		a, err := scanAsset(rows)
		if err != nil {
			return nil, translate(err, "asset")
		}
		assets = append(assets, a)
	}
	return assets, wrap(rows.Err(), "list ticket assets")
}

// IncidentCounts ranks assets by how many incidents referenced them.
//
// Restricted to kind = 'incident': a service request against a laptop ("please
// install this") is not evidence the laptop is failing, and counting it would
// put the most-requested assets at the top instead of the most-broken ones.
func (r *AssetRepo) IncidentCounts(ctx context.Context, orgID shared.ID, since time.Time, limit int) ([]app.AssetIncidentCount, error) {
	rows, err := r.db.Querier(ctx).Query(ctx, `
		SELECT `+prefixColumns(assetColumns, "a")+`, count(*) AS incident_count
		FROM assets a
		JOIN ticket_assets ta ON ta.asset_id = a.id AND ta.organization_id = a.organization_id
		JOIN tickets t        ON t.id = ta.ticket_id AND t.organization_id = a.organization_id
		WHERE a.organization_id = $1
		  AND t.kind = 'incident'
		  AND t.created_at >= $2
		GROUP BY a.id
		ORDER BY incident_count DESC, a.tag
		LIMIT $3`, orgID, since, limit)
	if err != nil {
		return nil, translate(err, "asset")
	}
	defer rows.Close()

	var results []app.AssetIncidentCount
	for rows.Next() {
		var a asset.Asset
		var count int
		if err := rows.Scan(&a.ID, &a.OrgID, &a.Tag, &a.Name, &a.Kind, &a.Status, &a.Criticality,
			&a.Manufacturer, &a.Model, &a.SerialNumber, &a.Location, &a.OwnerID, &a.ParentID,
			&a.PurchasedAt, &a.WarrantyUntil, &a.CreatedAt, &a.UpdatedAt, &count); err != nil {
			return nil, translate(err, "asset")
		}
		results = append(results, app.AssetIncidentCount{Asset: &a, Count: count})
	}
	return results, wrap(rows.Err(), "asset incident counts")
}

// prefixColumns qualifies a column list with a table alias, so the shared
// column constant stays usable in joins without being written out twice.
func prefixColumns(columns, alias string) string {
	parts := strings.Split(columns, ",")
	for i, part := range parts {
		parts[i] = alias + "." + strings.TrimSpace(part)
	}
	return strings.Join(parts, ", ")
}
