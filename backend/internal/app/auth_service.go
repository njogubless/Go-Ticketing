package app

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"time"

	"github.com/blessnduta/ticketing-system/internal/domain/identity"
	"github.com/blessnduta/ticketing-system/internal/domain/shared"
	"github.com/blessnduta/ticketing-system/internal/domain/sla"
	"github.com/blessnduta/ticketing-system/internal/domain/tenant"
)

// AuthService owns registration, login and token lifecycle.
type AuthService struct {
	tx        TxManager
	orgs      OrganizationRepository
	users     UserRepository
	teams     TeamRepository
	slaRepo   SLARepository
	tokens    RefreshTokenRepository
	hasher    PasswordHasher
	issuer    TokenIssuer
	clock     shared.Clock
	refreshTTL time.Duration
}

func NewAuthService(
	tx TxManager,
	orgs OrganizationRepository,
	users UserRepository,
	teams TeamRepository,
	slaRepo SLARepository,
	tokens RefreshTokenRepository,
	hasher PasswordHasher,
	issuer TokenIssuer,
	clock shared.Clock,
	refreshTTL time.Duration,
) *AuthService {
	return &AuthService{
		tx: tx, orgs: orgs, users: users, teams: teams, slaRepo: slaRepo,
		tokens: tokens, hasher: hasher, issuer: issuer, clock: clock, refreshTTL: refreshTTL,
	}
}

// TokenPair is what a successful authentication returns.
type TokenPair struct {
	AccessToken  string
	RefreshToken string
	ExpiresAt    time.Time
	User         *identity.User
}

// RegisterOrganizationInput signs up a new tenant and its first administrator
// in one step.
type RegisterOrganizationInput struct {
	OrgName      string
	OrgSlug      string
	TicketPrefix string
	Timezone     string
	AdminEmail   string
	AdminName    string
	Password     string
}

// RegisterOrganization provisions a tenant, its first admin, a default team,
// business calendars and the default SLA policies — all in one transaction.
//
// Seeding the calendars and policies here rather than leaving them to a setup
// wizard is a product decision with an engineering consequence: it means every
// ticket in the system has an SLA from the very first one, so the reporting is
// never retrospectively empty.
func (s *AuthService) RegisterOrganization(ctx context.Context, in RegisterOrganizationInput) (*TokenPair, error) {
	if err := identity.ValidatePassword(in.Password); err != nil {
		return nil, err
	}
	now := s.clock.Now()

	org, err := tenant.NewOrganization(in.OrgName, in.OrgSlug, in.TicketPrefix, in.Timezone, now)
	if err != nil {
		return nil, err
	}

	passwordHash, err := s.hasher.Hash(in.Password)
	if err != nil {
		return nil, shared.Internal("auth.hash_failed", "could not process password").WithCause(err)
	}

	admin, err := identity.NewUser(org.ID, in.AdminEmail, in.AdminName, passwordHash, identity.RoleAdmin, nil, now)
	if err != nil {
		return nil, err
	}

	var pair *TokenPair
	err = s.tx.WithinTx(ctx, func(ctx context.Context) error {
		if existing, err := s.users.ByEmail(ctx, admin.Email); err == nil && existing != nil {
			return shared.Conflict("auth.email_taken", "an account with that email already exists")
		} else if err != nil && shared.KindOf(err) != shared.KindNotFound {
			return err
		}

		if err := s.orgs.Create(ctx, org); err != nil {
			return err
		}

		team, err := identity.NewTeam(org.ID, "Service Desk", "Default queue for incoming tickets", now)
		if err != nil {
			return err
		}
		if err := s.teams.Create(ctx, team); err != nil {
			return err
		}
		admin.TeamID = &team.ID

		if err := s.users.Create(ctx, admin); err != nil {
			return err
		}

		if err := s.seedSLADefaults(ctx, org, now); err != nil {
			return err
		}

		pair, err = s.issueTokenPair(ctx, admin, shared.NewID(), now)
		return err
	})
	if err != nil {
		return nil, err
	}
	return pair, nil
}

// seedSLADefaults creates the 24/7 and business-hours calendars plus the four
// default priority policies.
func (s *AuthService) seedSLADefaults(ctx context.Context, org *tenant.Organization, now time.Time) error {
	alwaysOn, err := sla.AlwaysOn(org.ID, org.Timezone)
	if err != nil {
		return err
	}
	businessHours, err := sla.StandardBusinessHours(org.ID, org.Timezone)
	if err != nil {
		return err
	}
	if err := s.slaRepo.CreateCalendar(ctx, alwaysOn); err != nil {
		return err
	}
	if err := s.slaRepo.CreateCalendar(ctx, businessHours); err != nil {
		return err
	}
	for _, policy := range sla.DefaultPolicies(org.ID, alwaysOn.ID, businessHours.ID, now) {
		if err := s.slaRepo.CreatePolicy(ctx, policy); err != nil {
			return err
		}
	}
	return nil
}

// InviteUserInput adds a member to an existing organisation.
type InviteUserInput struct {
	Email    string
	FullName string
	Password string
	Role     identity.Role
	TeamID   *shared.ID
}

// InviteUser creates a colleague's account. Roles are assigned here by an
// admin — never self-selected at signup, which is the single most common
// privilege-escalation hole in systems of this shape.
func (s *AuthService) InviteUser(ctx context.Context, actor identity.Actor, in InviteUserInput) (*identity.User, error) {
	if err := actor.Require(identity.PermUserManage); err != nil {
		return nil, err
	}
	if err := identity.ValidatePassword(in.Password); err != nil {
		return nil, err
	}
	now := s.clock.Now()

	passwordHash, err := s.hasher.Hash(in.Password)
	if err != nil {
		return nil, shared.Internal("auth.hash_failed", "could not process password").WithCause(err)
	}

	user, err := identity.NewUser(actor.OrgID, in.Email, in.FullName, passwordHash, in.Role, in.TeamID, now)
	if err != nil {
		return nil, err
	}

	if in.TeamID != nil {
		if _, err := s.teams.ByID(ctx, actor.OrgID, *in.TeamID); err != nil {
			return nil, err
		}
	}

	if existing, err := s.users.ByEmail(ctx, user.Email); err == nil && existing != nil {
		return nil, shared.Conflict("auth.email_taken", "an account with that email already exists")
	} else if err != nil && shared.KindOf(err) != shared.KindNotFound {
		return nil, err
	}

	if err := s.users.Create(ctx, user); err != nil {
		return nil, err
	}
	return user, nil
}

// Login authenticates by email and password.
//
// The failure path is uniform on purpose: a missing account, a wrong password
// and a deactivated account all produce the same error and, critically, all
// perform a password verification. Skipping the hash for an unknown email
// makes the response measurably faster and turns login into a user-enumeration
// oracle.
func (s *AuthService) Login(ctx context.Context, email, password string) (*TokenPair, error) {
	now := s.clock.Now()
	invalid := shared.Unauthorized("auth.invalid_credentials", "invalid email or password")

	normalisedEmail, err := identity.NormaliseEmail(email)
	if err != nil {
		// Still burn a hash comparison so a malformed address is not
		// distinguishable by timing from a well-formed unknown one.
		s.hasher.Verify(dummyHash, password)
		return nil, invalid
	}

	user, err := s.users.ByEmail(ctx, normalisedEmail)
	if err != nil {
		if shared.KindOf(err) == shared.KindNotFound {
			s.hasher.Verify(dummyHash, password)
			return nil, invalid
		}
		return nil, err
	}

	ok, needsRehash := s.hasher.Verify(user.PasswordHash, password)
	if !ok || !user.Active {
		return nil, invalid
	}

	if needsRehash {
		// Transparent upgrade: the user proved the password, so we can store a
		// stronger hash without ever asking them to reset it.
		if rehashed, hashErr := s.hasher.Hash(password); hashErr == nil {
			user.PasswordHash = rehashed
		}
	}
	user.LastLoginAt = &now
	user.UpdatedAt = now
	if err := s.users.Update(ctx, user); err != nil {
		return nil, err
	}

	return s.issueTokenPair(ctx, user, shared.NewID(), now)
}

// Refresh rotates a refresh token.
//
// Rotation with reuse detection: each refresh token is single-use and belongs
// to a family. Presenting an already-consumed token means either a race or a
// stolen token, and we cannot tell which — so the entire family is revoked and
// the user is forced to log in again. Losing a session is a small cost against
// leaving a thief with a valid rotation chain.
func (s *AuthService) Refresh(ctx context.Context, refreshToken string) (*TokenPair, error) {
	now := s.clock.Now()
	invalid := shared.Unauthorized("auth.invalid_refresh_token", "session expired, please sign in again")

	tokenHash := hashToken(refreshToken)

	var (
		pair *TokenPair
		// compromisedFamily is carried out of the transaction rather than acted
		// on inside it. Revoking within the transaction and then returning an
		// error would roll the revocation back with everything else — the token
		// family would survive the very detection that was meant to kill it.
		compromisedFamily *shared.ID
	)

	err := s.tx.WithinTx(ctx, func(ctx context.Context) error {
		stored, err := s.tokens.Consume(ctx, tokenHash, now)
		if err != nil {
			if shared.KindOf(err) == shared.KindNotFound {
				return invalid
			}
			return err
		}

		if stored.RevokedAt != nil || now.After(stored.ExpiresAt) {
			return invalid
		}
		if stored.ConsumedAt != nil {
			familyID := stored.FamilyID
			compromisedFamily = &familyID
			return shared.Unauthorized("auth.token_reuse_detected",
				"this session has been ended for security reasons, please sign in again")
		}

		user, err := s.users.ByID(ctx, stored.OrgID, stored.UserID)
		if err != nil {
			return invalid
		}
		if !user.Active {
			return invalid
		}

		pair, err = s.issueTokenPair(ctx, user, stored.FamilyID, now)
		return err
	})

	if compromisedFamily != nil {
		// Burn the whole chain in its own transaction, so it commits even
		// though the caller's request is being rejected.
		if revokeErr := s.tokens.RevokeFamily(ctx, *compromisedFamily, now); revokeErr != nil {
			return nil, shared.Internal("auth.revoke_failed",
				"could not end the compromised session").WithCause(revokeErr)
		}
	}

	if err != nil {
		return nil, err
	}
	return pair, nil
}

// Logout revokes the presented refresh token's family.
func (s *AuthService) Logout(ctx context.Context, refreshToken string) error {
	now := s.clock.Now()
	stored, err := s.tokens.Consume(ctx, hashToken(refreshToken), now)
	if err != nil {
		if shared.KindOf(err) == shared.KindNotFound {
			// Logging out with an unknown token is not an error worth
			// surfacing — the desired end state (no valid session) holds.
			return nil
		}
		return err
	}
	return s.tokens.RevokeFamily(ctx, stored.FamilyID, now)
}

// LogoutEverywhere revokes every session for a user. The control an admin
// needs when a laptop is stolen.
func (s *AuthService) LogoutEverywhere(ctx context.Context, actor identity.Actor) error {
	return s.tokens.RevokeAllForUser(ctx, actor.UserID, s.clock.Now())
}

// IssueRealtimeTicket mints the short-lived credential the WebSocket
// handshake uses in place of the access token.
func (s *AuthService) IssueRealtimeTicket(ctx context.Context, actor identity.Actor) (string, error) {
	if actor.IsZero() {
		return "", shared.Unauthorized("auth.required", "authentication required")
	}
	return s.issuer.IssueRealtimeTicket(actor, s.clock.Now())
}

func (s *AuthService) issueTokenPair(ctx context.Context, user *identity.User, familyID shared.ID, now time.Time) (*TokenPair, error) {
	accessToken, expiresAt, err := s.issuer.Issue(user.Actor(), now)
	if err != nil {
		return nil, shared.Internal("auth.token_failed", "could not issue token").WithCause(err)
	}

	refreshToken, err := generateRefreshToken()
	if err != nil {
		return nil, shared.Internal("auth.token_failed", "could not issue token").WithCause(err)
	}

	if err := s.tokens.Store(ctx, StoredRefreshToken{
		TokenHash: hashToken(refreshToken),
		UserID:    user.ID,
		OrgID:     user.OrgID,
		FamilyID:  familyID,
		ExpiresAt: now.Add(s.refreshTTL),
		CreatedAt: now,
	}); err != nil {
		return nil, err
	}

	return &TokenPair{
		AccessToken:  accessToken,
		RefreshToken: refreshToken,
		ExpiresAt:    expiresAt,
		User:         user,
	}, nil
}

// generateRefreshToken produces 32 bytes of cryptographic randomness. Refresh
// tokens are opaque, not JWTs: there is nothing to read in them, and opacity
// means a leaked token reveals no user or tenant identifiers.
func generateRefreshToken() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", errors.New("entropy source unavailable")
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

// hashToken stores refresh tokens as SHA-256 digests. A database dump then
// yields no usable sessions. Plain SHA-256 rather than bcrypt is correct here
// precisely because the input is 256 bits of uniform randomness — there is no
// dictionary to attack, and the lookup must stay index-friendly.
func hashToken(token string) string {
	digest := sha256.Sum256([]byte(token))
	return base64.RawURLEncoding.EncodeToString(digest[:])
}

// dummyHash is a real bcrypt hash of an unguessable value, verified against
// when no user was found so the timing profile of a failed login is flat.
const dummyHash = "$2a$12$N9qo8uLOickgx2ZMRZoMyeIjZAgcfl7p92ldGxad68LJZdL17lhWy"
