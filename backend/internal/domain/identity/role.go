package identity

import "github.com/blessnduta/ticketing-system/internal/domain/shared"

// Role is a coarse bundle of Permissions. Roles are what administrators think
// in; permissions are what the code checks. Keeping the two separate means a
// new role is a data change here, not a scattered edit across every handler.
type Role string

const (
	// RoleRequester is an employee raising tickets about their own equipment
	// and access. They see only what they raised.
	RoleRequester Role = "requester"
	// RoleAgent is service-desk staff working their team's queue.
	RoleAgent Role = "agent"
	// RoleManager supervises teams: reassignment across teams, SLA policy
	// authorship, reporting.
	RoleManager Role = "manager"
	// RoleAdmin administers the organisation: users, teams, assets, settings.
	RoleAdmin Role = "admin"
)

// Permission is the unit actually enforced at the call site.
type Permission string

const (
	PermTicketCreate      Permission = "ticket:create"
	PermTicketReadOwn     Permission = "ticket:read_own"
	PermTicketReadTeam    Permission = "ticket:read_team"
	PermTicketReadAll     Permission = "ticket:read_all"
	PermTicketTransition  Permission = "ticket:transition"
	PermTicketAssign      Permission = "ticket:assign"
	PermTicketAssignCross Permission = "ticket:assign_cross_team"
	PermNoteInternal      Permission = "ticket:note_internal"
	PermAuditRead         Permission = "audit:read"
	PermApprovalDecide    Permission = "approval:decide"
	PermAssetManage       Permission = "asset:manage"
	PermAssetRead         Permission = "asset:read"
	PermSLAManage         Permission = "sla:manage"
	PermReportRead        Permission = "report:read"
	PermUserManage        Permission = "user:manage"
	PermTeamManage        Permission = "team:manage"
)

// rolePermissions is the authorisation matrix — the single place to look when
// asking "can this role do X?". Deliberately explicit rather than derived by
// inheritance: an inherited matrix is where privilege-escalation bugs hide,
// because nobody can read off what a role actually holds.
var rolePermissions = map[Role]map[Permission]bool{
	RoleRequester: {
		PermTicketCreate:  true,
		PermTicketReadOwn: true,
	},
	RoleAgent: {
		PermTicketCreate:     true,
		PermTicketReadOwn:    true,
		PermTicketReadTeam:   true,
		PermTicketTransition: true,
		PermTicketAssign:     true,
		PermNoteInternal:     true,
		PermAssetRead:        true,
		PermAuditRead:        true,
	},
	RoleManager: {
		PermTicketCreate:      true,
		PermTicketReadOwn:     true,
		PermTicketReadTeam:    true,
		PermTicketReadAll:     true,
		PermTicketTransition:  true,
		PermTicketAssign:      true,
		PermTicketAssignCross: true,
		PermNoteInternal:      true,
		PermAuditRead:         true,
		PermApprovalDecide:    true,
		PermAssetRead:         true,
		PermSLAManage:         true,
		PermReportRead:        true,
		PermTeamManage:        true,
	},
	RoleAdmin: {
		PermTicketCreate:      true,
		PermTicketReadOwn:     true,
		PermTicketReadTeam:    true,
		PermTicketReadAll:     true,
		PermTicketTransition:  true,
		PermTicketAssign:      true,
		PermTicketAssignCross: true,
		PermNoteInternal:      true,
		PermAuditRead:         true,
		PermApprovalDecide:    true,
		PermAssetManage:       true,
		PermAssetRead:         true,
		PermSLAManage:         true,
		PermReportRead:        true,
		PermUserManage:        true,
		PermTeamManage:        true,
	},
}

// Valid reports whether the role is one the system recognises. Anything else
// is treated as holding no permissions at all — an unknown role must fail
// closed, never open.
func (r Role) Valid() bool {
	_, ok := rolePermissions[r]
	return ok
}

// Permissions returns the role's grants. The returned map is a copy so a
// caller cannot mutate the authorisation matrix at runtime.
func (r Role) Permissions() map[Permission]bool {
	granted := rolePermissions[r]
	out := make(map[Permission]bool, len(granted))
	for perm := range granted {
		out[perm] = true
	}
	return out
}

// ParseRole validates untrusted input into a Role.
func ParseRole(raw string) (Role, error) {
	role := Role(raw)
	if !role.Valid() {
		return "", shared.Invalid("role.unknown", "unknown role").WithDetail("role", raw)
	}
	return role, nil
}
