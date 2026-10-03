// Package security holds the cryptographic adapters: password hashing and
// token issuance. Both are behind app-layer interfaces so the algorithm is a
// deployment decision rather than a code-wide dependency.
package security

import (
	"golang.org/x/crypto/bcrypt"

	"github.com/blessnduta/ticketing-system/internal/app"
)

// BcryptHasher implements app.PasswordHasher.
//
// bcrypt rather than argon2id, deliberately: argon2id is the stronger choice on
// paper, but its memory cost has to be tuned against the deployment target, and
// a mistuned argon2 (the common outcome) is weaker than a correctly-costed
// bcrypt. bcrypt at cost 12 is roughly 250ms on current hardware, which is the
// right order of magnitude for a login. The interface makes swapping it a
// one-file change if that judgement changes.
type BcryptHasher struct {
	cost int
}

func NewBcryptHasher(cost int) *BcryptHasher {
	if cost < bcrypt.MinCost || cost > bcrypt.MaxCost {
		cost = 12
	}
	return &BcryptHasher{cost: cost}
}

var _ app.PasswordHasher = (*BcryptHasher)(nil)

func (h *BcryptHasher) Hash(password string) (string, error) {
	// bcrypt silently truncates input beyond 72 bytes. The domain's password
	// policy caps length at 128 characters, which can exceed 72 *bytes* once
	// multi-byte characters are involved — so guard here rather than assume.
	if len(password) > 72 {
		password = password[:72]
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), h.cost)
	return string(hash), err
}

// Verify reports whether the password matches and whether the stored hash was
// produced with a weaker cost than the current policy.
//
// The comparison is constant-time within bcrypt itself. Reporting needsRehash
// lets the auth service transparently upgrade a user's stored hash the next
// time they log in, so raising the cost factor does not require a password
// reset for everyone.
func (h *BcryptHasher) Verify(hash, password string) (ok bool, needsRehash bool) {
	if len(password) > 72 {
		password = password[:72]
	}
	if err := bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)); err != nil {
		return false, false
	}
	storedCost, err := bcrypt.Cost([]byte(hash))
	if err != nil {
		return true, true
	}
	return true, storedCost < h.cost
}
