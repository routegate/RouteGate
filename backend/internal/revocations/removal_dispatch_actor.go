package revocations

import (
	"context"

	"github.com/ikaevus/routegate/backend/internal/auth"
	"github.com/jackc/pgx/v5"
)

// Must follow server/preparation/Agent locks, revalidating a live human
// super-admin session and complete grant set; never trust browser actor fields.
func lockRemovalDispatchActor(ctx context.Context, tx pgx.Tx, u auth.AuthenticatedUser) error {
	id, err := authenticatedRevocationActor(ctx)
	if err != nil || id != u.ID || !evidenceID.MatchString(u.SessionID) {
		return ErrPreflight
	}
	return lockRemovalDispatchSubject(ctx, tx, u.ID, u.SessionID)
}

// IDs must originate from the immutable approved dispatch, never an Agent
// request. Reuse the same live-human SQL gate for issuance and delivery.
func lockRemovalDispatchSubject(ctx context.Context, tx pgx.Tx, id, session string) error {
	if !evidenceID.MatchString(id) || !evidenceID.MatchString(session) {
		return ErrPreflight
	}
	var err error
	if _, err = tx.Exec(ctx, `LOCK TABLE user_roles,roles,role_permissions,permissions IN SHARE MODE`); err != nil {
		return err
	}
	var locked string
	if err = tx.QueryRow(ctx, `SELECT u.id::text FROM users u JOIN auth_sessions s ON s.user_id=u.id
		WHERE u.id=$1::uuid AND s.id=$2::uuid FOR SHARE OF u,s`, id, session).Scan(&locked); err != nil || locked != id {
		return ErrPreflight
	}
	var allowed bool
	err = tx.QueryRow(ctx, `SELECT EXISTS (
	SELECT 1 FROM users u JOIN auth_sessions s ON s.user_id=u.id
	WHERE u.id=$1::uuid AND s.id=$2::uuid AND u.status='active'
	AND u.user_type='human' AND s.revoked_at IS NULL AND s.expires_at>clock_timestamp()
	AND EXISTS(SELECT 1 FROM user_roles ur JOIN roles r ON r.id=ur.role_id WHERE ur.user_id=u.id AND r.code='super_admin')
	AND EXISTS(SELECT 1 FROM user_roles ur JOIN role_permissions rp ON rp.role_id=ur.role_id JOIN permissions p ON p.id=rp.permission_id WHERE ur.user_id=u.id AND p.code='system:manage')
	AND EXISTS(SELECT 1 FROM user_roles ur JOIN role_permissions rp ON rp.role_id=ur.role_id JOIN permissions p ON p.id=rp.permission_id WHERE ur.user_id=u.id AND p.code='configs:apply')
	AND EXISTS(SELECT 1 FROM user_roles ur JOIN role_permissions rp ON rp.role_id=ur.role_id JOIN permissions p ON p.id=rp.permission_id WHERE ur.user_id=u.id AND p.code='vpn_users:disable')
	)`, id, session).Scan(&allowed)
	if err != nil || !allowed {
		return ErrPreflight
	}
	return nil
}
