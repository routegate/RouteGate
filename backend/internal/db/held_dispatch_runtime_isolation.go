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
	// Ownership is not the only privilege escalation route: NOINHERIT
	// membership may still permit SET ROLE to the schema owner, and a
	// superuser/CREATEROLE login could grant itself access later.
	// Evaluate all these properties using the *runtime* connection.
	var ownerOrMember, schemaAccess, powerfulRole, databaseOwner, databaseCreate, switchedRole bool
	err:=pool.QueryRow(ctx,`SELECT
		n.nspowner=(SELECT oid FROM pg_roles WHERE rolname=current_user)
			OR pg_has_role(current_user,n.nspowner,'MEMBER'),
		has_schema_privilege(current_user,n.oid,'USAGE')
			OR has_schema_privilege(current_user,n.oid,'CREATE'),
		(SELECT rolsuper OR rolcreaterole OR rolcreatedb OR rolbypassrls
		 FROM pg_roles WHERE rolname=current_user),
		(SELECT datdba=(SELECT oid FROM pg_roles WHERE rolname=current_user)
		 FROM pg_database WHERE datname=current_database()),
		has_database_privilege(current_user,current_database(),'CREATE'),
		session_user<>current_user
		FROM pg_namespace n WHERE n.nspname='routegate_dispatch'
	`).Scan(&ownerOrMember,&schemaAccess,&powerfulRole,&databaseOwner,&databaseCreate,&switchedRole)
	if err!=nil {
		return fmt.Errorf("verify held-removal dispatch namespace: %w",err)
	}
	if ownerOrMember || schemaAccess || powerfulRole || databaseOwner ||
		databaseCreate || switchedRole {
		return ErrUnsafeHeldDispatchRuntimeRole
	}
	return nil
}
