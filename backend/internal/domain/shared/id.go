package shared

import "github.com/google/uuid"

// ID is the identifier type for every entity in the system.
//
// UUIDv7 rather than v4: v7 is time-ordered, so it indexes like a sequential
// key (no B-tree page splits on insert, good locality for "recent tickets"
// scans) while staying unguessable in customer-facing URLs. This is the single
// highest-leverage database decision in a system whose hottest query is
// "tickets ordered by creation time".
type ID = uuid.UUID

// NilID is the zero identifier.
var NilID = uuid.Nil

// NewID returns a fresh time-ordered identifier.
func NewID() ID {
	id, err := uuid.NewV7()
	if err != nil {
		// NewV7 only fails if the system entropy source fails, in which case
		// v4 is the correct fallback — we lose ordering, not correctness.
		return uuid.New()
	}
	return id
}

// ParseID converts an external string into an ID, returning a domain error so
// callers never have to translate uuid's error type themselves.
func ParseID(raw string) (ID, error) {
	id, err := uuid.Parse(raw)
	if err != nil {
		return NilID, Invalid("id.malformed", "not a valid identifier").WithCause(err)
	}
	return id, nil
}
