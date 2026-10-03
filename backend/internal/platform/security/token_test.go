package security

import (
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"github.com/blessnduta/ticketing-system/internal/domain/identity"
	"github.com/blessnduta/ticketing-system/internal/domain/shared"
)

const testSecret = "a-test-secret-long-enough-to-be-realistic-for-hs256"

func testActor() identity.Actor {
	teamID := shared.NewID()
	return identity.Actor{
		UserID: shared.NewID(),
		OrgID:  shared.NewID(),
		Role:   identity.RoleAgent,
		TeamID: &teamID,
	}
}

func TestIssueAndParseRoundTrip(t *testing.T) {
	issuer := NewJWTIssuer(testSecret, 15*time.Minute, 30*time.Second)
	actor := testActor()
	now := time.Now().UTC()

	token, expiresAt, err := issuer.Issue(actor, now)
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	if !expiresAt.After(now) {
		t.Fatal("the token must expire in the future")
	}

	parsed, err := issuer.Parse(token)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if parsed.UserID != actor.UserID || parsed.OrgID != actor.OrgID || parsed.Role != actor.Role {
		t.Fatal("the round trip must preserve the actor")
	}
	if parsed.TeamID == nil || *parsed.TeamID != *actor.TeamID {
		t.Fatal("the round trip must preserve the team")
	}
}

// TestRejectsAlgNone covers the most exploited JWT vulnerability there is: a
// token that declares alg:none and carries no signature. Without an explicit
// algorithm allowlist, some libraries accept it and hand back valid claims.
func TestRejectsAlgNone(t *testing.T) {
	issuer := NewJWTIssuer(testSecret, 15*time.Minute, 30*time.Second)
	actor := testActor()

	unsigned := jwt.NewWithClaims(jwt.SigningMethodNone, claims{
		OrgID: actor.OrgID.String(),
		Role:  string(identity.RoleAdmin), // escalate while we are at it
		Scope: scopeAccess,
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   actor.UserID.String(),
			Issuer:    "ticketing-system",
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour)),
		},
	})
	token, err := unsigned.SignedString(jwt.UnsafeAllowNoneSignatureType)
	if err != nil {
		t.Fatalf("constructing the attack token: %v", err)
	}

	if _, err := issuer.Parse(token); err == nil {
		t.Fatal("a token signed with alg:none must be rejected")
	}
}

func TestRejectsATokenSignedWithAnotherKey(t *testing.T) {
	issuer := NewJWTIssuer(testSecret, 15*time.Minute, 30*time.Second)
	attacker := NewJWTIssuer("a-completely-different-secret-value-here", 15*time.Minute, 30*time.Second)

	token, _, err := attacker.Issue(testActor(), time.Now().UTC())
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	if _, err := issuer.Parse(token); err == nil {
		t.Fatal("a token signed with a different key must be rejected")
	}
}

func TestRejectsAnExpiredToken(t *testing.T) {
	issuer := NewJWTIssuer(testSecret, 15*time.Minute, 30*time.Second)
	// Issued an hour ago with a 15-minute lifetime.
	token, _, err := issuer.Issue(testActor(), time.Now().UTC().Add(-time.Hour))
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	if _, err := issuer.Parse(token); err == nil {
		t.Fatal("an expired token must be rejected")
	}
}

// TestScopesAreNotInterchangeable is why the token carries a scope claim.
// Access tokens and realtime handshake tickets are signed with the same key,
// so without the scope check a 30-second socket ticket — which travels in a
// query string and lands in every proxy log — would work as a full API
// credential.
func TestScopesAreNotInterchangeable(t *testing.T) {
	issuer := NewJWTIssuer(testSecret, 15*time.Minute, 30*time.Second)
	actor := testActor()
	now := time.Now().UTC()

	accessToken, _, err := issuer.Issue(actor, now)
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	realtimeTicket, err := issuer.IssueRealtimeTicket(actor, now)
	if err != nil {
		t.Fatalf("issue realtime ticket: %v", err)
	}

	if _, err := issuer.ParseRealtimeTicket(accessToken); err == nil {
		t.Fatal("an access token must not be accepted as a realtime ticket")
	}
	if _, err := issuer.Parse(realtimeTicket); err == nil {
		t.Fatal("a realtime ticket must not be accepted as an access token")
	}

	// Each is still valid in its own lane.
	if _, err := issuer.Parse(accessToken); err != nil {
		t.Fatalf("the access token should parse in its own scope: %v", err)
	}
	if _, err := issuer.ParseRealtimeTicket(realtimeTicket); err != nil {
		t.Fatalf("the realtime ticket should parse in its own scope: %v", err)
	}
}

func TestRealtimeTicketIsShortLived(t *testing.T) {
	issuer := NewJWTIssuer(testSecret, 15*time.Minute, 30*time.Second)
	// Minted well over its 30-second lifetime ago.
	realtimeTicket, err := issuer.IssueRealtimeTicket(testActor(), time.Now().UTC().Add(-5*time.Minute))
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	if _, err := issuer.ParseRealtimeTicket(realtimeTicket); err == nil {
		t.Fatal("a stale realtime ticket must be rejected — that short window is the whole point")
	}
}

func TestRejectsAnUnknownRoleInAToken(t *testing.T) {
	issuer := NewJWTIssuer(testSecret, 15*time.Minute, 30*time.Second)
	actor := testActor()

	forged := jwt.NewWithClaims(jwt.SigningMethodHS256, claims{
		OrgID: actor.OrgID.String(),
		Role:  "superuser",
		Scope: scopeAccess,
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   actor.UserID.String(),
			Issuer:    "ticketing-system",
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour)),
		},
	})
	token, err := forged.SignedString([]byte(testSecret))
	if err != nil {
		t.Fatalf("signing: %v", err)
	}

	// Correctly signed, but the role is not one we recognise. Falling back to
	// a default would turn a token from a future or rolled-back version of the
	// service into an authorisation decision nobody intended.
	if _, err := issuer.Parse(token); err == nil {
		t.Fatal("a token carrying an unrecognised role must be rejected")
	}
}

func TestRejectsGarbage(t *testing.T) {
	issuer := NewJWTIssuer(testSecret, 15*time.Minute, 30*time.Second)
	for _, token := range []string{"", "not-a-jwt", "a.b.c", strings.Repeat("x", 500)} {
		if _, err := issuer.Parse(token); err == nil {
			t.Errorf("expected %q to be rejected", token)
		}
	}
}

// --- password hashing ------------------------------------------------------

func TestBcryptHasherRoundTrip(t *testing.T) {
	// Cost 10 keeps the test fast; production uses 12.
	hasher := NewBcryptHasher(10)

	hash, err := hasher.Hash("correct-horse-battery-staple")
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	if hash == "correct-horse-battery-staple" {
		t.Fatal("the hash must not be the plaintext")
	}

	ok, needsRehash := hasher.Verify(hash, "correct-horse-battery-staple")
	if !ok {
		t.Fatal("the correct password must verify")
	}
	if needsRehash {
		t.Fatal("a hash at the current cost must not be flagged for rehashing")
	}

	if ok, _ := hasher.Verify(hash, "wrong-password-entirely"); ok {
		t.Fatal("an incorrect password must not verify")
	}
}

func TestBcryptHasherFlagsWeakerHashesForUpgrade(t *testing.T) {
	weak := NewBcryptHasher(10)
	strong := NewBcryptHasher(12)

	hash, err := weak.Hash("correct-horse-battery-staple")
	if err != nil {
		t.Fatalf("hash: %v", err)
	}

	// The user proved the password, so the stored hash can be upgraded
	// transparently — raising the cost factor must not require a mass reset.
	ok, needsRehash := strong.Verify(hash, "correct-horse-battery-staple")
	if !ok || !needsRehash {
		t.Fatalf("expected a valid-but-outdated hash, got ok=%v needsRehash=%v", ok, needsRehash)
	}
}

func TestBcryptHasherHandlesOverlongInput(t *testing.T) {
	hasher := NewBcryptHasher(10)
	// bcrypt silently truncates beyond 72 bytes; the adapter must not error.
	long := strings.Repeat("a", 100) + "-tail"
	hash, err := hasher.Hash(long)
	if err != nil {
		t.Fatalf("hashing an overlong password must not fail: %v", err)
	}
	if ok, _ := hasher.Verify(hash, long); !ok {
		t.Fatal("an overlong password must verify against its own hash")
	}
}

func TestBcryptHasherProducesDistinctHashes(t *testing.T) {
	hasher := NewBcryptHasher(10)
	first, _ := hasher.Hash("same-password-twice-over")
	second, _ := hasher.Hash("same-password-twice-over")
	// Different salts: identical passwords must not produce identical hashes,
	// or a database dump reveals which users share a password.
	if first == second {
		t.Fatal("two hashes of the same password must differ")
	}
}
