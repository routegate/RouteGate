package auth

import (
    "slices"
    "testing"
)

func TestRuntimeRecoveryDedicatedPermissionIsSuperAdminOnly(t *testing.T) {
    if !slices.Contains(BuiltInPermissions, PermissionRuntimeRecoveryAuthorize) {
        t.Fatal("dedicated permission is not seeded")
    }
    for role, perms := range BuiltInRoles {
        got := slices.Contains(perms, PermissionRuntimeRecoveryAuthorize)
        if got != (role == RoleSuperAdmin) {
            t.Fatalf("role %s has recovery authorization=%v", role, got)
        }
    }
}

func TestRuntimeRecoveryRequiresActiveHumanSuperAdminAndSession(t *testing.T) {
    base := AuthenticatedUser{UserProfile: UserProfile{
        ID: "human-id", UserType: UserTypeHuman, Status: "active",
        Roles: []string{RoleSuperAdmin},
        Permissions: []string{PermissionRuntimeRecoveryAuthorize},
    }, SessionID: "session-id"}
    if !CanAuthorizeRuntimeRecovery(base) {
        t.Fatal("active human super_admin should satisfy policy")
    }
    for name, mutate := range map[string]func(*AuthenticatedUser){
        "admin_role_only": func(u *AuthenticatedUser) { u.Roles = []string{RoleAdmin} },
        "operator_with_grant": func(u *AuthenticatedUser) { u.Roles = []string{RoleOperator} },
        "agent_with_forged_role": func(u *AuthenticatedUser) { u.UserType = RoleAgent },
        "disabled_super_admin": func(u *AuthenticatedUser) { u.Status = "disabled" },
        "absent_permission": func(u *AuthenticatedUser) { u.Permissions = []string{"configs:rollback"} },
        "missing_session": func(u *AuthenticatedUser) { u.SessionID = "" },
        "missing_identity": func(u *AuthenticatedUser) { u.ID = "" },
        "missing_role": func(u *AuthenticatedUser) { u.Roles = nil },
    } {
        t.Run(name,func(t *testing.T){
            candidate:=base
            mutate(&candidate)
            if CanAuthorizeRuntimeRecovery(candidate) {
                t.Fatal("unsafe recovery permission granted")
            }
        })
    }
}
