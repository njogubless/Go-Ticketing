package identity

import (
	"net/mail"
	"strings"
	"time"
	"unicode"

	"github.com/blessnduta/ticketing-system/internal/domain/shared"
)

// User is a member of exactly one organisation. Cross-organisation membership
// is deliberately not modelled: it would mean the actor's OrgID could no
// longer be derived from the token alone, and every tenant-isolation guarantee
// in the system rests on that.
type User struct {
	ID           ID
	OrgID        ID
	Email        string
	PasswordHash string
	FullName     string
	Role         Role
	TeamID       *ID
	// Active gates login. Deactivating rather than deleting preserves the
	// audit trail's referential integrity — an audit entry pointing at a
	// deleted actor is worthless in a compliance review.
	Active      bool
	LastLoginAt *time.Time
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

const (
	minPasswordLength = 12
	maxPasswordLength = 128 // bcrypt silently truncates at 72 bytes; reject long
	// inputs outright rather than accept a password whose tail is ignored.
	maxEmailLength    = 254
	maxFullNameLength = 120
)

// NewUser constructs a valid user or explains why it cannot. The password is
// taken as an already-computed hash: the domain defines *what* must be true of
// a user, while *how* a password is hashed is an infrastructure concern the
// application layer injects.
func NewUser(orgID ID, email, fullName, passwordHash string, role Role, teamID *ID, now time.Time) (*User, error) {
	normalisedEmail, err := NormaliseEmail(email)
	if err != nil {
		return nil, err
	}

	fullName = strings.TrimSpace(fullName)
	if fullName == "" {
		return nil, shared.Invalid("user.name_required", "full name is required")
	}
	if len(fullName) > maxFullNameLength {
		return nil, shared.Invalid("user.name_too_long", "full name is too long")
	}
	if !role.Valid() {
		return nil, shared.Invalid("user.role_invalid", "unknown role").WithDetail("role", string(role))
	}
	if passwordHash == "" {
		return nil, shared.Internal("user.hash_missing", "password hash was not supplied")
	}

	return &User{
		ID:           shared.NewID(),
		OrgID:        orgID,
		Email:        normalisedEmail,
		PasswordHash: passwordHash,
		FullName:     fullName,
		Role:         role,
		TeamID:       teamID,
		Active:       true,
		CreatedAt:    now,
		UpdatedAt:    now,
	}, nil
}

// Actor projects a user into the identity used for authorisation decisions.
func (u *User) Actor() Actor {
	return Actor{UserID: u.ID, OrgID: u.OrgID, Role: u.Role, TeamID: u.TeamID}
}

// NormaliseEmail lowercases and validates an address. Normalising on the way
// in — rather than comparing case-insensitively on the way out — is what makes
// the unique index on email actually prevent duplicate accounts.
func NormaliseEmail(raw string) (string, error) {
	trimmed := strings.ToLower(strings.TrimSpace(raw))
	if trimmed == "" {
		return "", shared.Invalid("user.email_required", "email is required")
	}
	if len(trimmed) > maxEmailLength {
		return "", shared.Invalid("user.email_too_long", "email is too long")
	}
	if _, err := mail.ParseAddress(trimmed); err != nil {
		return "", shared.Invalid("user.email_invalid", "email is not a valid address")
	}
	return trimmed, nil
}

// ValidatePassword enforces the password policy before hashing.
//
// Length is weighted far more heavily than character-class rules, per current
// NIST guidance: a 12-character passphrase beats "P@ss1!" comfortably, and
// mandatory symbol rules mostly produce predictable substitutions. The one
// composition rule kept is "not a single repeated character", which catches
// the genuinely degenerate cases.
func ValidatePassword(password string) error {
	if len(password) < minPasswordLength {
		return shared.Invalid("password.too_short", "password must be at least 12 characters").
			WithDetail("min_length", minPasswordLength)
	}
	if len(password) > maxPasswordLength {
		return shared.Invalid("password.too_long", "password must be at most 128 characters").
			WithDetail("max_length", maxPasswordLength)
	}

	distinct := map[rune]struct{}{}
	hasNonSpace := false
	for _, r := range password {
		distinct[r] = struct{}{}
		if !unicode.IsSpace(r) {
			hasNonSpace = true
		}
	}
	if !hasNonSpace {
		return shared.Invalid("password.blank", "password cannot be only whitespace")
	}
	if len(distinct) < 5 {
		return shared.Invalid("password.too_repetitive", "password is too repetitive")
	}
	return nil
}

// Deactivate disables login without destroying history.
func (u *User) Deactivate(now time.Time) {
	u.Active = false
	u.UpdatedAt = now
}

// ChangeRole moves a user between roles.
func (u *User) ChangeRole(role Role, now time.Time) error {
	if !role.Valid() {
		return shared.Invalid("user.role_invalid", "unknown role").WithDetail("role", string(role))
	}
	u.Role = role
	u.UpdatedAt = now
	return nil
}
