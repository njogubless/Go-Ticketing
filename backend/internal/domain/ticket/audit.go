package ticket

import (
	"time"

	"github.com/blessnduta/ticketing-system/internal/domain/shared"
)

// AuditAction is a closed vocabulary. Free-text actions make audit logs
// unqueryable, which defeats the point of keeping them.
type AuditAction string

const (
	AuditCreated       AuditAction = "ticket.created"
	AuditStatusChanged AuditAction = "ticket.status_changed"
	AuditAssigned      AuditAction = "ticket.assigned"
	AuditRouted        AuditAction = "ticket.routed"
	AuditReclassified  AuditAction = "ticket.reclassified"
	AuditMessageAdded  AuditAction = "ticket.message_added"
	AuditSLAApplied    AuditAction = "ticket.sla_applied"
	AuditSLABreached   AuditAction = "ticket.sla_breached"
	AuditEscalated     AuditAction = "ticket.escalated"
	AuditApprovalAdded AuditAction = "ticket.approval_requested"
	AuditApprovalDecided AuditAction = "ticket.approval_decided"
	AuditAssetLinked   AuditAction = "ticket.asset_linked"
	AuditAssetUnlinked AuditAction = "ticket.asset_unlinked"
)

// AuditEntry is an append-only record of a meaningful change.
//
// Two design points, both non-negotiable for a system that claims an audit
// trail. First, entries are never updated or deleted — the database revokes
// UPDATE and DELETE on this table from the application role (see the
// migration), so a bug cannot rewrite history. Second, ActorID is nullable
// only for genuinely system-originated actions, and those carry ActorLabel
// so the log never reads "changed by (null)".
type AuditEntry struct {
	ID         shared.ID
	OrgID      shared.ID
	TicketID   shared.ID
	ActorID    *shared.ID
	ActorLabel string
	Action     AuditAction
	// From/To hold the before and after values as strings. Typed columns per
	// action would be more precise but would need a schema change for every
	// new action; strings plus a closed action vocabulary is the trade-off
	// that keeps this table stable over years.
	From *string
	To   *string
	// Metadata carries anything else worth keeping (IP address, request ID,
	// automation rule name). Stored as JSONB.
	Metadata  map[string]any
	CreatedAt time.Time
}

// NewAuditEntry records a user-initiated change.
func NewAuditEntry(orgID, ticketID shared.ID, actorID shared.ID, actorLabel string, action AuditAction, from, to *string, now time.Time) *AuditEntry {
	return &AuditEntry{
		ID:         shared.NewID(),
		OrgID:      orgID,
		TicketID:   ticketID,
		ActorID:    &actorID,
		ActorLabel: actorLabel,
		Action:     action,
		From:       from,
		To:         to,
		CreatedAt:  now,
	}
}

// NewSystemAuditEntry records a change made by the system itself — an SLA
// escalation, an auto-assignment.
func NewSystemAuditEntry(orgID, ticketID shared.ID, action AuditAction, from, to *string, now time.Time) *AuditEntry {
	return &AuditEntry{
		ID:         shared.NewID(),
		OrgID:      orgID,
		TicketID:   ticketID,
		ActorLabel: "system",
		Action:     action,
		From:       from,
		To:         to,
		CreatedAt:  now,
	}
}

// WithMetadata attaches structured context.
func (e *AuditEntry) WithMetadata(metadata map[string]any) *AuditEntry {
	e.Metadata = metadata
	return e
}

// StrPtr is a small helper for building From/To values at call sites without
// a local variable per call.
func StrPtr(s string) *string { return &s }
