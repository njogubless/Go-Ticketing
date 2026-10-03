package identity

import "github.com/blessnduta/ticketing-system/internal/domain/shared"

// Actor is the authenticated caller, reconstructed from the access token on
// every request. It is the *only* source of identity the application layer
// trusts — no handler reads a user ID out of a request body or URL to decide
// who is acting.
//
// OrgID being part of the actor rather than the request is what makes tenant
// isolation structural: a caller cannot address another organisation's data
// because the organisation is not an input they control.
type Actor struct {
	UserID ID
	OrgID  ID
	Role   Role
	TeamID *ID
}

// ID aliases shared.ID so this package reads naturally at call sites.
type ID = shared.ID

// Can reports whether the actor holds a permission.
func (a Actor) Can(perm Permission) bool {
	return rolePermissions[a.Role][perm]
}

// Require returns a Forbidden domain error unless the actor holds the
// permission. Call sites read as a guard clause:
//
//	if err := actor.Require(identity.PermTicketAssign); err != nil { return err }
func (a Actor) Require(perm Permission) error {
	if a.Can(perm) {
		return nil
	}
	return shared.Forbidden("auth.forbidden", "you do not have permission to perform this action").
		WithDetail("required_permission", string(perm))
}

// InTeam reports whether the actor belongs to the given team.
func (a Actor) InTeam(teamID ID) bool {
	return a.TeamID != nil && *a.TeamID == teamID
}

// IsZero reports whether this is an unauthenticated placeholder. Used as a
// defensive assertion in the application layer: a service reaching a
// permission check with a zero actor means middleware was skipped, which is a
// wiring bug that must fail loudly rather than silently allow.
func (a Actor) IsZero() bool {
	return a.UserID == shared.NilID || a.OrgID == shared.NilID
}
