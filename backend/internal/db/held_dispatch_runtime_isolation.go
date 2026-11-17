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
	var owner,usable bool
	err:=pool.QueryRow(ctx,`SELECT
		pg_get_userbyid(n.nspowner)=current_user,
		has_schema_privilege(current_user,n.oid,'USAGE')
		FROM pg_namespace n WHERE n.nspname='routegate_dispatch'
	`).Scan(&owner,&usable)
	if err!=nil {
		return fmt.Errorf("verify held-removal dispatch namespace: %w",err)
	}
	if owner || usable {
		return ErrUnsafeHeldDispatchRuntimeRole
	}
	return nil
}
