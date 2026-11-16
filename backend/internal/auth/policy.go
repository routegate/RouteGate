package auth

import (
	"net/http"

	"github.com/ikaevus/routegate/backend/internal/httpx"
)

const (
	RoleSuperAdmin = "super_admin"
	RoleAdmin      = "admin"
	RoleOperator   = "operator"
	RoleReadOnly   = "read_only"
	RoleVPNUser    = "vpn_user"
	RoleAgent      = "agent"

	UserTypeHuman = "human"
)

var AdminRoles = map[string]struct{}{
	RoleSuperAdmin: {},
	RoleAdmin:      {},
	RoleOperator:   {},
	RoleReadOnly:   {},
}

func HasRole(user UserProfile, role string) bool {
	for _, candidate := range user.Roles {
		if candidate == role {
			return true
		}
	}
	return false
}

func HasPermission(user UserProfile, permission string) bool {
	for _, candidate := range user.Permissions {
		if candidate == permission {
			return true
		}
	}
	return false
}

func IsAdminUser(user UserProfile) bool {
	if user.UserType != UserTypeHuman || user.Status != "active" {
		return false
	}
	for _, role := range user.Roles {
		if _, ok := AdminRoles[role]; ok {
			return true
		}
	}
	return false
}

// PermissionRuntimeRecoveryEnrollmentPrepare is a separate, deliberately
// narrow super_admin grant. It may create only the immutable
// "prepared_unattested" Manager record. It cannot enroll, activate, dispatch,
// perform Agent work, or clear the RG-140 mutation fence.
const PermissionRuntimeRecoveryEnrollmentPrepare = "runtime_recovery:enrollment_prepare"

// CanPrepareRuntimeRecoveryEnrollment is a local early check only.
// The writer MUST re-check the live session, role and permission in
// PostgreSQL under locks in its own transaction before recording history.
func CanPrepareRuntimeRecoveryEnrollment(u AuthenticatedUser) bool {
	return u.UserType == UserTypeHuman &&
		u.Status == "active" &&
		u.ID != "" && u.SessionID != "" &&
		HasRole(u.UserProfile, RoleSuperAdmin) &&
		HasPermission(u.UserProfile, PermissionRuntimeRecoveryEnrollmentPrepare)
}

// PermissionRuntimeRecoveryAuthorize is deliberately NOT implied by any
// ordinary admin/rollback permission. Only active human super_admin sessions
// with this dedicated grant may create future recovery authorizations.
const PermissionRuntimeRecoveryAuthorize = "runtime_recovery:authorize"

// CanAuthorizeRuntimeRecovery is a policy prerequisite, not a recovery
// command. A future writer MUST re-read the session and role transactionally.
func CanAuthorizeRuntimeRecovery(u AuthenticatedUser) bool {
	return u.UserType == UserTypeHuman &&
		u.Status == "active" &&
		u.ID != "" && u.SessionID != "" &&
		HasRole(u.UserProfile, RoleSuperAdmin) &&
		HasPermission(u.UserProfile, PermissionRuntimeRecoveryAuthorize)
}

func RequireAdminSession(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, ok := UserFromContext(r.Context())
		if !ok {
			httpx.WriteJSON(w, http.StatusUnauthorized, httpx.Error("unauthorized", "Authentication is required."))
			return
		}
		if !IsAdminUser(user.UserProfile) {
			httpx.WriteJSON(w, http.StatusForbidden, httpx.Error("forbidden", "Admin session is required."))
			return
		}
		next.ServeHTTP(w, r)
	})
}
