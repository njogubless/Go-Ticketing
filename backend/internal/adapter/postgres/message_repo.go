package postgres

import (
	"context"

	"github.com/blessnduta/ticketing-system/internal/app"
	"github.com/blessnduta/ticketing-system/internal/domain/shared"
	"github.com/blessnduta/ticketing-system/internal/domain/ticket"
	"github.com/blessnduta/ticketing-system/internal/platform/database"
)

type MessageRepo struct{ db *database.Pool }

func NewMessageRepo(db *database.Pool) *MessageRepo { return &MessageRepo{db: db} }

var _ app.MessageRepository = (*MessageRepo)(nil)

func (r *MessageRepo) Create(ctx context.Context, message *ticket.Message) error {
	_, err := r.db.Querier(ctx).Exec(ctx, `
		INSERT INTO ticket_messages (id, organization_id, ticket_id, author_id, body, visibility, system, created_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8)`,
		message.ID, message.OrgID, message.TicketID, message.AuthorID,
		message.Body, message.Visibility, message.System, message.CreatedAt)
	return translate(err, "message")
}

// ListForTicket returns the thread.
//
// When includeInternal is false the filter is in the WHERE clause, not applied
// in Go afterwards. Internal notes then never leave the database, so no
// subsequent refactor — a logging statement, a caching layer, a serialiser
// change — can accidentally expose them.
func (r *MessageRepo) ListForTicket(ctx context.Context, orgID, ticketID shared.ID, includeInternal bool) ([]*ticket.Message, error) {
	query := `
		SELECT id, organization_id, ticket_id, author_id, body, visibility, system, created_at
		FROM ticket_messages
		WHERE organization_id = $1 AND ticket_id = $2`
	if !includeInternal {
		query += ` AND visibility = 'public'`
	}
	query += ` ORDER BY created_at ASC, id ASC`

	rows, err := r.db.Querier(ctx).Query(ctx, query, orgID, ticketID)
	if err != nil {
		return nil, translate(err, "message")
	}
	defer rows.Close()

	var messages []*ticket.Message
	for rows.Next() {
		var message ticket.Message
		if err := rows.Scan(&message.ID, &message.OrgID, &message.TicketID, &message.AuthorID,
			&message.Body, &message.Visibility, &message.System, &message.CreatedAt); err != nil {
			return nil, translate(err, "message")
		}
		messages = append(messages, &message)
	}
	return messages, wrap(rows.Err(), "list messages")
}

func (r *MessageRepo) CountPublicAgentReplies(ctx context.Context, orgID, ticketID shared.ID) (int, error) {
	var count int
	err := r.db.Querier(ctx).QueryRow(ctx, `
		SELECT count(*)
		FROM ticket_messages m
		JOIN tickets t ON t.id = m.ticket_id AND t.organization_id = m.organization_id
		JOIN users  u ON u.id = m.author_id  AND u.organization_id = m.organization_id
		WHERE m.organization_id = $1 AND m.ticket_id = $2
		  AND m.visibility = 'public'
		  AND m.system = FALSE
		  AND m.author_id <> t.requester_id
		  AND u.role IN ('agent','manager','admin')`, orgID, ticketID).Scan(&count)
	return count, translate(err, "message")
}

// ---------------------------------------------------------------------------
// Attachments
// ---------------------------------------------------------------------------

type AttachmentRepo struct{ db *database.Pool }

func NewAttachmentRepo(db *database.Pool) *AttachmentRepo { return &AttachmentRepo{db: db} }

var _ app.AttachmentRepository = (*AttachmentRepo)(nil)

const attachmentColumns = `id, organization_id, message_id, filename, content_type,
	size_bytes, storage_key, uploaded_by, created_at`

func (r *AttachmentRepo) Create(ctx context.Context, attachment *ticket.Attachment) error {
	_, err := r.db.Querier(ctx).Exec(ctx, `
		INSERT INTO attachments (id, organization_id, message_id, filename, content_type,
			size_bytes, storage_key, uploaded_by, created_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)`,
		attachment.ID, attachment.OrgID, attachment.MessageID, attachment.Filename,
		attachment.ContentType, attachment.SizeBytes, attachment.StorageKey,
		attachment.UploadedBy, attachment.CreatedAt)
	return translate(err, "attachment")
}

func (r *AttachmentRepo) ByID(ctx context.Context, orgID, id shared.ID) (*ticket.Attachment, error) {
	var attachment ticket.Attachment
	err := r.db.Querier(ctx).QueryRow(ctx,
		`SELECT `+attachmentColumns+` FROM attachments WHERE organization_id = $1 AND id = $2`,
		orgID, id).Scan(&attachment.ID, &attachment.OrgID, &attachment.MessageID,
		&attachment.Filename, &attachment.ContentType, &attachment.SizeBytes,
		&attachment.StorageKey, &attachment.UploadedBy, &attachment.CreatedAt)
	if err != nil {
		return nil, translate(err, "attachment")
	}
	return &attachment, nil
}

// ListForMessages batches the lookup for a whole thread — one query for N
// messages rather than N queries, which is the difference between a thread
// view that renders instantly and one that does not.
func (r *AttachmentRepo) ListForMessages(ctx context.Context, orgID shared.ID, messageIDs []shared.ID) ([]*ticket.Attachment, error) {
	if len(messageIDs) == 0 {
		return nil, nil
	}
	rows, err := r.db.Querier(ctx).Query(ctx,
		`SELECT `+attachmentColumns+` FROM attachments
		 WHERE organization_id = $1 AND message_id = ANY($2)
		 ORDER BY created_at ASC`, orgID, messageIDs)
	if err != nil {
		return nil, translate(err, "attachment")
	}
	defer rows.Close()

	var attachments []*ticket.Attachment
	for rows.Next() {
		var attachment ticket.Attachment
		if err := rows.Scan(&attachment.ID, &attachment.OrgID, &attachment.MessageID,
			&attachment.Filename, &attachment.ContentType, &attachment.SizeBytes,
			&attachment.StorageKey, &attachment.UploadedBy, &attachment.CreatedAt); err != nil {
			return nil, translate(err, "attachment")
		}
		attachments = append(attachments, &attachment)
	}
	return attachments, wrap(rows.Err(), "list attachments")
}

// ---------------------------------------------------------------------------
// Audit log
// ---------------------------------------------------------------------------

type AuditRepo struct{ db *database.Pool }

func NewAuditRepo(db *database.Pool) *AuditRepo { return &AuditRepo{db: db} }

var _ app.AuditRepository = (*AuditRepo)(nil)

// Append is the only write path. There is intentionally no Update or Delete:
// the interface does not offer them, and the database rejects them (see
// migration 0002).
func (r *AuditRepo) Append(ctx context.Context, entry *ticket.AuditEntry) error {
	_, err := r.db.Querier(ctx).Exec(ctx, `
		INSERT INTO audit_log (id, organization_id, ticket_id, actor_id, actor_label,
			action, from_value, to_value, metadata, created_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`,
		entry.ID, entry.OrgID, entry.TicketID, entry.ActorID, entry.ActorLabel,
		entry.Action, entry.From, entry.To, entry.Metadata, entry.CreatedAt)
	return translate(err, "audit")
}

func (r *AuditRepo) ListForTicket(ctx context.Context, orgID, ticketID shared.ID, page shared.Pagination) (shared.Page[*ticket.AuditEntry], error) {
	limit := page.NormalisedLimit()
	args := []any{orgID, ticketID}
	query := `
		SELECT id, organization_id, ticket_id, actor_id, actor_label, action,
		       from_value, to_value, metadata, created_at
		FROM audit_log
		WHERE organization_id = $1 AND ticket_id = $2`
	if page.Cursor != nil {
		args = append(args, *page.Cursor)
		query += " AND id < $3"
	}
	args = append(args, limit+1)
	query += " ORDER BY id DESC LIMIT $" + itoa(len(args))

	empty := shared.Page[*ticket.AuditEntry]{}
	rows, err := r.db.Querier(ctx).Query(ctx, query, args...)
	if err != nil {
		return empty, translate(err, "audit")
	}
	defer rows.Close()

	var entries []*ticket.AuditEntry
	for rows.Next() {
		var entry ticket.AuditEntry
		if err := rows.Scan(&entry.ID, &entry.OrgID, &entry.TicketID, &entry.ActorID,
			&entry.ActorLabel, &entry.Action, &entry.From, &entry.To,
			&entry.Metadata, &entry.CreatedAt); err != nil {
			return empty, translate(err, "audit")
		}
		entries = append(entries, &entry)
	}
	if err := rows.Err(); err != nil {
		return empty, translate(err, "audit")
	}

	return shared.NewPage(entries, limit, func(e *ticket.AuditEntry) shared.ID { return e.ID }), nil
}

// itoa avoids pulling strconv in for one call site in a hot-ish path.
func itoa(n int) string {
	if n < 10 {
		return string(rune('0' + n))
	}
	digits := make([]byte, 0, 3)
	for n > 0 {
		digits = append([]byte{byte('0' + n%10)}, digits...)
		n /= 10
	}
	return string(digits)
}
