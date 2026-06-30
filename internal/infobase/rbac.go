package infobase

import "sort"

type initPayload struct {
	Roles    map[string]Role `json:"roles"`
	Users    map[string]User `json:"users"`
	Settings BookSettings    `json:"settings"`
}

type roleUpsertPayload struct {
	Role Role `json:"role"`
}

type userUpsertPayload struct {
	User User `json:"user"`
}

func initEvent() eventEnvelope {
	roles := defaultRoles()
	return wrapEvent("init", initPayload{
		Roles: roles,
		Users: map[string]User{
			"owner": {ID: "owner", Role: "Owner"},
		},
		Settings: DefaultBookSettings(),
	})
}

func defaultRoles() map[string]Role {
	return map[string]Role{
		"Owner": {
			Name: "Owner",
			Permissions: []Permission{
				PermissionLedgerRead, PermissionLedgerWrite, PermissionNotesRead, PermissionNotesWrite,
				PermissionRBACManage, PermissionSnapshot, PermissionAuditRead,
				PermissionAdminRecover, PermissionSettingsManage,
			},
		},
		"Admin": {
			Name: "Admin",
			Permissions: []Permission{
				PermissionLedgerRead, PermissionLedgerWrite, PermissionNotesRead, PermissionNotesWrite,
				PermissionRBACManage, PermissionSnapshot, PermissionAuditRead, PermissionSettingsManage,
			},
		},
		"Accountant": {
			Name: "Accountant",
			Permissions: []Permission{
				PermissionLedgerRead, PermissionLedgerWrite, PermissionNotesRead, PermissionAuditRead,
			},
		},
		"Operations": {
			Name:        "Operations",
			Permissions: []Permission{PermissionNotesRead, PermissionNotesWrite},
		},
		"Sales Rep": {
			Name:        "Sales Rep",
			Permissions: []Permission{PermissionNotesRead, PermissionNotesWrite},
		},
	}
}

func EnsurePermission(st State, ctx Context, permission Permission) error {
	if ctx.Actor == "" {
		return appErr(ErrPermission, "actor is required")
	}
	user, ok := st.Users[ctx.Actor]
	if !ok {
		return appErr(ErrPermission, "unknown actor %q", ctx.Actor)
	}
	roleName := ctx.Role
	if roleName == "" {
		roleName = user.Role
	}
	if roleName != user.Role {
		return appErr(ErrPermission, "actor %q cannot assume role %q", ctx.Actor, roleName)
	}
	role, ok := st.Roles[roleName]
	if !ok {
		return appErr(ErrPermission, "unknown role %q", roleName)
	}
	for _, p := range role.Permissions {
		if p == permission {
			return nil
		}
	}
	return appErr(ErrPermission, "role %q lacks %s", roleName, permission)
}

func (s *Store) UpsertRole(ctx Context, role Role) (string, error) {
	st, err := s.LoadState()
	if err != nil {
		return "", err
	}
	if err := EnsurePermission(st, ctx, PermissionRBACManage); err != nil {
		return "", err
	}
	if role.Name == "" {
		return "", appErr(ErrValidation, "role name is required")
	}
	known := map[Permission]bool{}
	for _, p := range PermissionNames() {
		known[Permission(p)] = true
	}
	for _, p := range role.Permissions {
		if !known[p] {
			return "", appErr(ErrValidation, "unknown permission %q", p)
		}
	}
	role.Permissions = sortedPermissions(role.Permissions)
	return s.appendEvent(ctx, "rbac.role", "role:"+role.Name, "rbac role upsert", wrapEvent("role.upsert", roleUpsertPayload{Role: role}), true)
}

func (s *Store) UpsertUser(ctx Context, user User) (string, error) {
	st, err := s.LoadState()
	if err != nil {
		return "", err
	}
	if err := EnsurePermission(st, ctx, PermissionRBACManage); err != nil {
		return "", err
	}
	if user.ID == "" || user.Role == "" {
		return "", appErr(ErrValidation, "user id and role are required")
	}
	if _, ok := st.Roles[user.Role]; !ok {
		return "", appErr(ErrValidation, "role %q does not exist", user.Role)
	}
	return s.appendEvent(ctx, "rbac.user", "user:"+user.ID, "rbac user upsert", wrapEvent("user.upsert", userUpsertPayload{User: user}), true)
}

func PermissionNames() []string {
	perms := []string{
		string(PermissionLedgerRead), string(PermissionLedgerWrite), string(PermissionNotesRead),
		string(PermissionNotesWrite), string(PermissionRBACManage),
		string(PermissionSnapshot), string(PermissionAuditRead), string(PermissionAdminRecover),
		string(PermissionSettingsManage),
	}
	sort.Strings(perms)
	return perms
}
