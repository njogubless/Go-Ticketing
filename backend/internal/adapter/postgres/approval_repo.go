package postgres

import (
	"context"

	"github.com/blessnduta/ticketing-system/internal/app"
	"github.com/blessnduta/ticketing-system/internal/domain/approval"
	"github.com/blessnduta/ticketing-system/internal/domain/shared"
	"github.com/blessnduta/ticketing-system/internal/platform/database"
)

type ApprovalRepo struct{ db *database.Pool }

func NewApprovalRepo(db *database.Pool) *ApprovalRepo { return &ApprovalRepo{db: db} }

var _ app.ApprovalRepository = (*ApprovalRepo)(nil)

const approvalColumns = `id, organization_id, ticket_id, approver_id, requested_by,
	decision, comment, decided_at, created_at`

func (r *ApprovalRepo) Create(ctx context.Context, a *approval.Approval) error {
	_, err := r.db.Querier(ctx).Exec(ctx, `
		INSERT INTO approvals (id, organization_id, ticket_id, approver_id, requested_by,
			decision, comment, decided_at, created_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)`,
		a.ID, a.OrgID, a.TicketID, a.ApproverID, a.RequestedBy,
		a.Decision, a.Comment, a.DecidedAt, a.CreatedAt)
	return translate(err, "approval")
}

// Update guards the decision transition in SQL as well as in the domain: the
// WHERE clause only matches a still-pending row, so two approvers clicking at
// the same instant cannot both record a verdict.
func (r *ApprovalRepo) Update(ctx context.Context, a *approval.Approval) error {
	tag, err := r.db.Querier(ctx).Exec(ctx, `
		UPDATE approvals SET decision = $3, comment = $4, decided_at = $5
		WHERE organization_id = $1 AND id = $2 AND decision = 'pending'`,
		a.OrgID, a.ID, a.Decision, a.Comment, a.DecidedAt)
	if err != nil {
		return translate(err, "approval")
	}
	if tag.RowsAffected() == 0 {
		return shared.Conflict("approval.already_decided", "this approval has already been decided")
	}
	return nil
}

func (r *ApprovalRepo) ByID(ctx context.Context, orgID, id shared.ID) (*approval.Approval, error) {
	row := r.db.Querier(ctx).QueryRow(ctx,
		`SELECT `+approvalColumns+` FROM approvals WHERE organization_id = $1 AND id = $2`, orgID, id)
	a, err := scanApproval(row)
	if err != nil {
		return nil, translate(err, "approval")
	}
	return a, nil
}

func scanApproval(row rowScanner) (*approval.Approval, error) {
	var a approval.Approval
	err := row.Scan(&a.ID, &a.OrgID, &a.TicketID, &a.ApproverID, &a.RequestedBy,
		&a.Decision, &a.Comment, &a.DecidedAt, &a.CreatedAt)
	if err != nil {
		return nil, err
	}
	return &a, nil
}

func (r *ApprovalRepo) ListForTicket(ctx context.Context, orgID, ticketID shared.ID) ([]*approval.Approval, error) {
	return r.list(ctx,
		`SELECT `+approvalColumns+` FROM approvals
		 WHERE organization_id = $1 AND ticket_id = $2 ORDER BY created_at`, orgID, ticketID)
}

func (r *ApprovalRepo) ListPendingForApprover(ctx context.Context, orgID, approverID shared.ID) ([]*approval.Approval, error) {
	return r.list(ctx,
		`SELECT `+approvalColumns+` FROM approvals
		 WHERE organization_id = $1 AND approver_id = $2 AND decision = 'pending'
		 ORDER BY created_at`, orgID, approverID)
}

func (r *ApprovalRepo) list(ctx context.Context, query string, args ...any) ([]*approval.Approval, error) {
	rows, err := r.db.Querier(ctx).Query(ctx, query, args...)
	if err != nil {
		return nil, translate(err, "approval")
	}
	defer rows.Close()

	var approvals []*approval.Approval
	for rows.Next() {
		a, err := scanApproval(rows)
		if err != nil {
			return nil, translate(err, "approval")
		}
		approvals = append(approvals, a)
	}
	return approvals, wrap(rows.Err(), "list approvals")
}
