package db

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
)

// ErrUnsafeHeldDispatchRuntimeRole is a startup-stop condition. A private
// held-removal schema must never be owned or usable by Manager's normal DB
// login: REVOKE FROM PUBLIC does not restrict the schema's owner.
var ErrUnsafeHeldDispatchRuntimeRole = errors.New("unsafe held-removal dispatch database role")

// CheckHeldDispatchRuntimeIsolation is safe and read-only on every startup.
// Existing canonical RG-140 <=170 intentionally has no dispatch schema.
// Once a future separately owned schema exists, this check refuses to serve
// until the runtime connection is independently isolated from its owner.
func CheckHeldDispatchRuntimeIsolation(ctx context.Context, pool *pgxpool.Pool) error {
	var exists bool
	if err := pool.QueryRow(ctx,`SELECT EXISTS(
		SELECT 1 FROM pg_namespace WHERE nspname='routegate_dispatch'
	)`).Scan(&exists); err != nil {
		return fmt.Errorf("inspect held-removal dispatch schema: %w",err)
	}
	if !exists {
		return nil
	}
	// A private schema is only safe when the ordinary runtime also passes the
	// shared external-role ownership/administrative-authority boundary.
	// Reuse that check rather than maintaining a weaker second role policy.
	if err := CheckExternalMigrationRuntimeRole(ctx, pool); err != nil {
		if errors.Is(err, ErrUnsafeExternalMigrationRuntimeRole) {
			return ErrUnsafeHeldDispatchRuntimeRole
		}
		return fmt.Errorf("verify held-removal dispatch runtime authority: %w", err)
	}
	// A non-member can still receive USAGE or CREATE by an explicit grant
	// (including PUBLIC). Keep the private-schema ACL check independent.
	var schemaAccess bool
	if err := pool.QueryRow(ctx, `SELECT
		has_schema_privilege(session_user,n.oid,'USAGE')
		OR has_schema_privilege(session_user,n.oid,'CREATE')
		FROM pg_namespace n WHERE n.nspname='routegate_dispatch'`).Scan(&schemaAccess); err != nil {
		return fmt.Errorf("verify held-removal dispatch namespace: %w", err)
	}
	if schemaAccess {
		return ErrUnsafeHeldDispatchRuntimeRole
	}
	return nil
}

// ErrUnsafeExternalMigrationRuntimeRole stops an opt-in external-mode Manager
// or dedicated migrator before it accepts an owner-capable runtime login.
var ErrUnsafeExternalMigrationRuntimeRole = errors.New("external migrations require a non-owner, non-privileged Manager runtime database login")

// CheckExternalMigrationRuntimeRole adapts the ownership check already
// validated in historical Draft #647. Unlike the held-dispatch schema check,
// it must run even before routegate_dispatch exists: database ownership,
// public-object ownership or an inheritable/SET ROLE path to them already
// invalidates the least-privileged runtime boundary. This check is SELECT-only.
func CheckExternalMigrationRuntimeRole(ctx context.Context, pool *pgxpool.Pool) error {
	var unsafe bool
	err := pool.QueryRow(ctx, `SELECT
		session_user <> current_user
		OR has_database_privilege(session_user, current_database(), 'CREATE')
		OR EXISTS (
			SELECT 1 FROM pg_namespace n
			WHERE n.nspname = 'public'
			AND has_schema_privilege(session_user, n.oid, 'CREATE')
		)
		OR EXISTS (
			SELECT 1 FROM pg_roles r
			WHERE pg_has_role(session_user, r.oid, 'MEMBER')
			AND (
				r.rolsuper OR r.rolcreaterole OR r.rolcreatedb
				OR r.rolreplication OR r.rolbypassrls
				OR r.oid IN (
					SELECT datdba FROM pg_database WHERE datname = current_database()
				)
				OR r.oid IN (
					SELECT nspowner FROM pg_namespace
					WHERE nspname IN ('public', 'routegate_dispatch')
				)
				OR r.oid IN (
					SELECT c.relowner FROM pg_class c
					JOIN pg_namespace n ON n.oid = c.relnamespace
					WHERE n.nspname = 'public'
				)
			)
		)`).Scan(&unsafe)
	if err != nil {
		return fmt.Errorf("inspect external runtime database authority: %w", err)
	}
	if unsafe {
		return ErrUnsafeExternalMigrationRuntimeRole
	}
	return nil
}
