package security

import (
	"time"

	"github.com/golang-jwt/jwt/v5"

	"github.com/blessnduta/ticketing-system/internal/app"
	"github.com/blessnduta/ticketing-system/internal/domain/identity"
	"github.com/blessnduta/ticketing-system/internal/domain/shared"
)

// JWTIssuer implements app.TokenIssuer.
//
// Access tokens are short-lived (15 minutes by default) and stateless. That
// pairing is the point: a stateless token cannot be revoked, so it must expire
// fast enough that the window of a stolen token is small, with the long-lived,
// revocable refresh token carrying the actual session. A 24-hour stateless
// access token — the common shortcut — means a leaked token is valid for a day
// and there is nothing anyone can do about it.
type JWTIssuer struct {
	secret       []byte
	accessTTL    time.Duration
	realtimeTTL  time.Duration
	issuer       string
}

func NewJWTIssuer(secret string, accessTTL, realtimeTTL time.Duration) *JWTIssuer {
	return &JWTIssuer{
		secret:      []byte(secret),
		accessTTL:   accessTTL,
		realtimeTTL: realtimeTTL,
		issuer:      "ticketing-system",
	}
}

var _ app.TokenIssuer = (*JWTIssuer)(nil)

// claims carries the actor. OrgID is in the token rather than the request
// because that is what makes tenant isolation unforgeable — a caller cannot
// choose which organisation they act in.
type claims struct {
	OrgID  string `json:"org"`
	Role   string `json:"role"`
	TeamID string `json:"team,omitempty"`
	// Scope separates an access token from a realtime handshake ticket. They
	// are signed with the same key, so without this an attacker could present
	// a 30-second WebSocket ticket as a full API credential.
	Scope string `json:"scope"`
	jwt.RegisteredClaims
}

const (
	scopeAccess   = "api"
	scopeRealtime = "realtime"
)

func (i *JWTIssuer) Issue(actor identity.Actor, now time.Time) (string, time.Time, error) {
	expiresAt := now.Add(i.accessTTL)
	token, err := i.sign(actor, scopeAccess, now, expiresAt)
	return token, expiresAt, err
}

// IssueRealtimeTicket mints the credential used for the WebSocket handshake.
//
// Browsers cannot attach an Authorization header to a WebSocket upgrade, so
// something has to travel in the query string, where it will be recorded by
// every proxy, load balancer and access log in the path. Putting the access
// token there would leak a 15-minute API credential into those logs. A
// 30-second ticket, scoped so it grants nothing but a socket subscription, is
// the smaller exposure — by the time the log is read it is already useless.
func (i *JWTIssuer) IssueRealtimeTicket(actor identity.Actor, now time.Time) (string, error) {
	return i.sign(actor, scopeRealtime, now, now.Add(i.realtimeTTL))
}

func (i *JWTIssuer) sign(actor identity.Actor, scope string, now, expiresAt time.Time) (string, error) {
	payload := claims{
		OrgID: actor.OrgID.String(),
		Role:  string(actor.Role),
		Scope: scope,
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   actor.UserID.String(),
			Issuer:    i.issuer,
			IssuedAt:  jwt.NewNumericDate(now),
			NotBefore: jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(expiresAt),
		},
	}
	if actor.TeamID != nil {
		payload.TeamID = actor.TeamID.String()
	}
	return jwt.NewWithClaims(jwt.SigningMethodHS256, payload).SignedString(i.secret)
}

func (i *JWTIssuer) Parse(token string) (identity.Actor, error) {
	return i.parse(token, scopeAccess)
}

func (i *JWTIssuer) ParseRealtimeTicket(token string) (identity.Actor, error) {
	return i.parse(token, scopeRealtime)
}

func (i *JWTIssuer) parse(token, expectedScope string) (identity.Actor, error) {
	invalid := shared.Unauthorized("auth.invalid_token", "your session is not valid — please sign in again")

	parsed, err := jwt.ParseWithClaims(token, &claims{},
		func(t *jwt.Token) (any, error) { return i.secret, nil },
		// Pinning the algorithm is not optional. Without it, a token signed
		// with alg:none — or an RS256 token whose "signature" is an HMAC of the
		// public key — parses successfully. This one option closes the most
		// widely exploited JWT vulnerability class there is.
		jwt.WithValidMethods([]string{jwt.SigningMethodHS256.Alg()}),
		jwt.WithIssuer(i.issuer),
		jwt.WithExpirationRequired(),
	)
	if err != nil || !parsed.Valid {
		return identity.Actor{}, invalid
	}

	payload, ok := parsed.Claims.(*claims)
	if !ok || payload.Scope != expectedScope {
		return identity.Actor{}, invalid
	}

	userID, err := shared.ParseID(payload.Subject)
	if err != nil {
		return identity.Actor{}, invalid
	}
	orgID, err := shared.ParseID(payload.OrgID)
	if err != nil {
		return identity.Actor{}, invalid
	}
	role, err := identity.ParseRole(payload.Role)
	if err != nil {
		// An unrecognised role must fail closed. Falling back to a default
		// role here would turn a token from a future or downgraded version of
		// the service into an authorisation decision nobody intended.
		return identity.Actor{}, invalid
	}

	actor := identity.Actor{UserID: userID, OrgID: orgID, Role: role}
	if payload.TeamID != "" {
		if teamID, err := shared.ParseID(payload.TeamID); err == nil {
			actor.TeamID = &teamID
		}
	}
	return actor, nil
}
