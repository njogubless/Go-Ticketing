package identity

import (
	"testing"

	"github.com/blessnduta/ticketing-system/internal/domain/shared"
)

// TestRolePermissions_Boundaries pins the authorisation matrix. These
// assertions read like a specification because that is what they are: if one
// of them starts failing, someone has widened a role's reach, and that should
// never happen silently.
func TestRolePermissions_Boundaries(t *testing.T) {
	mustNotHave := map[Role][]Permission{
		RoleRequester: {
			// The whole requester/agent boundary, in one list.
			PermTicketReadTeam, PermTicketReadAll, PermTicketTransition,
			PermTicketAssign, PermNoteInternal, PermAuditRead,
			PermApprovalDecide, PermAssetManage, PermSLAManage,
			PermReportRead, PermUserManage, PermTeamManage,
		},
		RoleAgent: {
			// An agent works a queue; they do not run the organisation.
			PermTicketReadAll, PermTicketAssignCross, PermUserManage,
			PermTeamManage, PermSLAManage, PermReportRead,
			// Agents must not approve changes — that would let the person
			// making a change wave it through, which defeats the control.
			PermApprovalDecide, PermAssetManage,
		},
		RoleManager: {
			// Managers run the desk; only admins administer the tenant.
			PermUserManage, PermAssetManage,
		},
	}

	for role, forbidden := range mustNotHave {
		for _, permission := range forbidden {
			if role.Permissions()[permission] {
				t.Errorf("%s must NOT hold %s", role, permission)
			}
		}
	}

	mustHave := map[Role][]Permission{
		RoleRequester: {PermTicketCreate, PermTicketReadOwn},
		RoleAgent:     {PermTicketCreate, PermTicketReadTeam, PermTicketTransition, PermTicketAssign, PermNoteInternal},
		RoleManager:   {PermTicketReadAll, PermApprovalDecide, PermReportRead, PermSLAManage, PermTicketAssignCross},
		RoleAdmin:     {PermUserManage, PermTeamManage, PermAssetManage, PermTicketReadAll, PermApprovalDecide},
	}

	for role, required := range mustHave {
		for _, permission := range required {
			if !role.Permissions()[permission] {
				t.Errorf("%s must hold %s", role, permission)
			}
		}
	}
}

func TestUnknownRoleFailsClosed(t *testing.T) {
	unknown := Role("superuser")
	if unknown.Valid() {
		t.Fatal("an unrecognised role must not validate")
	}
	if len(unknown.Permissions()) != 0 {
		t.Fatal("an unrecognised role must hold no permissions at all")
	}

	// A token carrying an unknown role must grant nothing, not default to
	// something convenient.
	actor := Actor{UserID: shared.NewID(), OrgID: shared.NewID(), Role: unknown}
	if actor.Can(PermTicketCreate) {
		t.Fatal("an unknown role must fail closed")
	}
	if err := actor.Require(PermTicketReadOwn); err == nil {
		t.Fatal("Require must reject an unknown role")
	}
}

func TestPermissionsMapIsACopy(t *testing.T) {
	permissions := RoleRequester.Permissions()
	permissions[PermUserManage] = true

	// Mutating the returned map must not escalate the role for the whole
	// process. Returning the internal map directly would make this possible.
	if RoleRequester.Permissions()[PermUserManage] {
		t.Fatal("the authorisation matrix must not be mutable through Permissions()")
	}
}

func TestActorRequireReportsTheMissingPermission(t *testing.T) {
	actor := Actor{UserID: shared.NewID(), OrgID: shared.NewID(), Role: RoleRequester}
	err := actor.Require(PermReportRead)
	domainErr, ok := shared.AsError(err)
	if !ok {
		t.Fatalf("expected a domain error, got %T", err)
	}
	if shared.KindOf(err) != shared.KindForbidden {
		t.Fatalf("expected a forbidden error, got %q", shared.KindOf(err))
	}
	if domainErr.Details["required_permission"] != string(PermReportRead) {
		t.Fatal("the error should name the permission that was missing")
	}
}

func TestActorIsZeroDetectsMissingMiddleware(t *testing.T) {
	// A zero actor reaching a service means authentication middleware was
	// skipped for that route — a wiring bug that must fail loudly.
	if !(Actor{}).IsZero() {
		t.Fatal("an empty actor must report itself as zero")
	}
	if !(Actor{UserID: shared.NewID()}).IsZero() {
		t.Fatal("an actor with no organisation must report itself as zero")
	}
	if (Actor{UserID: shared.NewID(), OrgID: shared.NewID()}).IsZero() {
		t.Fatal("a fully-populated actor must not report itself as zero")
	}
}

func TestValidatePassword(t *testing.T) {
	cases := []struct {
		name     string
		password string
		valid    bool
	}{
		{"a good passphrase", "correct-horse-battery-staple", true},
		{"exactly at the minimum", "abcdefghijkl", true},
		{"too short", "short-pass", false},
		{"only whitespace", "            ", false},
		{"degenerate repetition", "aaaaaaaaaaaaaaaa", false},
		{"far too long", string(make([]byte, 200)), false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidatePassword(tc.password)
			if tc.valid && err != nil {
				t.Fatalf("expected %q to be accepted, got: %v", tc.name, err)
			}
			if !tc.valid && err == nil {
				t.Fatalf("expected %q to be rejected", tc.name)
			}
		})
	}
}

func TestNormaliseEmail(t *testing.T) {
	// Normalising on the way in is what makes the unique index actually
	// prevent duplicate accounts — comparing case-insensitively on the way out
	// would not.
	got, err := NormaliseEmail("  Agent@Example.COM ")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "agent@example.com" {
		t.Fatalf("expected a lowercased, trimmed address, got %q", got)
	}

	for _, invalid := range []string{"", "   ", "not-an-address", "@example.com"} {
		if _, err := NormaliseEmail(invalid); err == nil {
			t.Errorf("expected %q to be rejected", invalid)
		}
	}
}
