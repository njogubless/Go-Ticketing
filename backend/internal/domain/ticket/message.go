package ticket

import (
	"strings"
	"time"

	"github.com/blessnduta/ticketing-system/internal/domain/shared"
)

// Visibility decides who may read a message. This is the highest-risk field in
// the entire schema: an internal note leaking to a requester is a real
// incident, not a cosmetic bug. It is therefore an explicit enum, filtered in
// SQL, and re-checked before serialisation.
type Visibility string

const (
	// VisibilityPublic is part of the conversation with the requester.
	VisibilityPublic Visibility = "public"
	// VisibilityInternal is agent-only commentary. Never leaves the desk.
	VisibilityInternal Visibility = "internal"
)

func ParseVisibility(raw string) (Visibility, error) {
	switch Visibility(raw) {
	case VisibilityPublic:
		return VisibilityPublic, nil
	case VisibilityInternal:
		return VisibilityInternal, nil
	default:
		return "", shared.Invalid("message.visibility_invalid", "visibility must be public or internal").
			WithDetail("visibility", raw)
	}
}

const MaxMessageLength = 50000

// Message is one entry in a ticket's thread — a reply, an internal note, or a
// system-generated record of an automated action.
type Message struct {
	ID         ID
	OrgID      ID
	TicketID   ID
	AuthorID   *ID // nil for system-generated entries
	Body       string
	Visibility Visibility
	// System marks entries the desk did not type (auto-assignment, SLA
	// escalation). Rendered differently and excluded from response-time
	// metrics, since a robot replying is not a response.
	System      bool
	Attachments []Attachment
	CreatedAt   time.Time
}

// NewMessage validates and constructs a thread entry.
func NewMessage(orgID, ticketID ID, authorID *ID, body string, visibility Visibility, now time.Time) (*Message, error) {
	body = strings.TrimSpace(body)
	if body == "" {
		return nil, shared.Invalid("message.body_required", "a message body is required")
	}
	if len(body) > MaxMessageLength {
		return nil, shared.Invalid("message.body_too_long", "message is too long").
			WithDetail("max_length", MaxMessageLength)
	}
	return &Message{
		ID:         shared.NewID(),
		OrgID:      orgID,
		TicketID:   ticketID,
		AuthorID:   authorID,
		Body:       body,
		Visibility: visibility,
		CreatedAt:  now,
	}, nil
}

// NewSystemMessage records an automated action in the thread. Always internal:
// the requester should hear from the desk, not from its plumbing.
func NewSystemMessage(orgID, ticketID ID, body string, now time.Time) *Message {
	return &Message{
		ID:         shared.NewID(),
		OrgID:      orgID,
		TicketID:   ticketID,
		Body:       body,
		Visibility: VisibilityInternal,
		System:     true,
		CreatedAt:  now,
	}
}

// ReadableBy is the last line of defence before a message is serialised. The
// repository already filters internal notes out of requester queries; this
// re-check exists because defence in depth is cheap and a leak is not.
func (m *Message) ReadableBy(canReadInternal bool) bool {
	return m.Visibility == VisibilityPublic || canReadInternal
}

// Attachment is a file on a message. Only the object-storage key is stored —
// never a public URL — so access is mediated by a short-lived signed URL
// issued after the same permission check as the message itself.
type Attachment struct {
	ID          ID
	OrgID       ID
	MessageID   ID
	Filename    string
	ContentType string
	SizeBytes   int64
	StorageKey  string
	UploadedBy  ID
	CreatedAt   time.Time
}

const (
	MaxAttachmentBytes    = 25 << 20 // 25 MiB
	MaxAttachmentFilename = 255
)

// allowedContentTypes is an allowlist, not a denylist. A denylist on file
// uploads is a vulnerability with extra steps: there is always one more
// extension nobody thought of.
var allowedContentTypes = map[string]bool{
	"image/png": true, "image/jpeg": true, "image/gif": true, "image/webp": true,
	"application/pdf":  true,
	"text/plain":       true,
	"text/csv":         true,
	"application/zip":  true,
	"application/json": true,
	"application/msword": true,
	"application/vnd.openxmlformats-officedocument.wordprocessingml.document": true,
	"application/vnd.openxmlformats-officedocument.spreadsheetml.sheet":       true,
	"application/vnd.ms-excel": true,
}

func NewAttachment(orgID, messageID, uploaderID ID, filename, contentType, storageKey string, size int64, now time.Time) (*Attachment, error) {
	filename = strings.TrimSpace(filename)
	if filename == "" {
		return nil, shared.Invalid("attachment.filename_required", "a filename is required")
	}
	if len(filename) > MaxAttachmentFilename {
		return nil, shared.Invalid("attachment.filename_too_long", "filename is too long")
	}
	// Path traversal defence: the filename is display-only, but it ends up in
	// a Content-Disposition header and, in some clients, on a filesystem.
	if strings.ContainsAny(filename, `/\`) || strings.Contains(filename, "..") {
		return nil, shared.Invalid("attachment.filename_invalid", "filename contains illegal characters")
	}
	if size <= 0 {
		return nil, shared.Invalid("attachment.empty", "file is empty")
	}
	if size > MaxAttachmentBytes {
		return nil, shared.Invalid("attachment.too_large", "file exceeds the size limit").
			WithDetail("max_bytes", MaxAttachmentBytes)
	}
	if !allowedContentTypes[contentType] {
		return nil, shared.Invalid("attachment.type_not_allowed", "that file type is not accepted").
			WithDetail("content_type", contentType)
	}
	return &Attachment{
		ID:          shared.NewID(),
		OrgID:       orgID,
		MessageID:   messageID,
		Filename:    filename,
		ContentType: contentType,
		SizeBytes:   size,
		StorageKey:  storageKey,
		UploadedBy:  uploaderID,
		CreatedAt:   now,
	}, nil
}
