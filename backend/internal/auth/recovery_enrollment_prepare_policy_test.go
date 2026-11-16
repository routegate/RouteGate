package auth

import (
	"slices"
	"testing"
)

func TestRuntimeRecoveryEnrollmentPreparationDedicatedPermissionIsSuperAdminOnly(t *testing.T) {
	if !slices.Contains(BuiltInPermissions,PermissionRuntimeRecoveryEnrollmentPrepare) {
		t.Fatal("enrollment preparation permission is not seeded")
	}
	for role, permissions:=range BuiltInRoles{
		got:=slices.Contains(permissions,PermissionRuntimeRecoveryEnrollmentPrepare)
		if got!=(role==RoleSuperAdmin){
			t.Fatalf("role %s has unsafe Agent enrollment preparation permission=%v",role,got)
		}
	}
	if PermissionRuntimeRecoveryEnrollmentPrepare==PermissionRuntimeRecoveryAuthorize {
		t.Fatal("enrollment preparation and recovery-unlock authorization are conflated")
	}
}

func TestRuntimeRecoveryEnrollmentPreparationRequiresFreshHumanSuperAdmin(t *testing.T){
	base:=AuthenticatedUser{UserProfile:UserProfile{
		ID:"human-id",UserType:UserTypeHuman,Status:"active",
		Roles:[]string{RoleSuperAdmin},Permissions:[]string{PermissionRuntimeRecoveryEnrollmentPrepare},
	},SessionID:"session-id"}
	if !CanPrepareRuntimeRecoveryEnrollment(base){t.Fatal("explicit super-admin prepare permission refused")}
	for name, mutate:=range map[string]func(*AuthenticatedUser){
		"admin_only":func(u *AuthenticatedUser){u.Roles=[]string{RoleAdmin}},
		"operator_only":func(u *AuthenticatedUser){u.Roles=[]string{RoleOperator}},
		"recovery_authorize_only":func(u *AuthenticatedUser){u.Permissions=[]string{PermissionRuntimeRecoveryAuthorize}},
		"agent_with_superadmin_claim":func(u *AuthenticatedUser){u.UserType=RoleAgent},
		"disabled_human":func(u *AuthenticatedUser){u.Status="disabled"},
		"missing_user":func(u *AuthenticatedUser){u.ID=""},
		"missing_session":func(u *AuthenticatedUser){u.SessionID=""},
		"missing_role":func(u *AuthenticatedUser){u.Roles=nil},
		"missing_permission":func(u *AuthenticatedUser){u.Permissions=nil},
	}{
		t.Run(name,func(t *testing.T){
			u:=base
			mutate(&u)
			if CanPrepareRuntimeRecoveryEnrollment(u){t.Fatal("unauthorized preparation policy accepted")}
		})
	}
}
