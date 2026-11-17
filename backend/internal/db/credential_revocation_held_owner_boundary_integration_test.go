package db

import (
	"testing"
)

// The standard installer currently makes 'routegate' both the database owner
// and the running Manager's login. REVOKE FROM PUBLIC never restricts an object
// owner. Until an independently provisioned coordinator + migration role are
// implemented, a legacy held-dispatch schema in that runtime role is unsafe.
//
// This test is a future fail-closed release gate: if anyone imports historical
// 176 as canonical 171 without *actual* runtime DB role isolation, CI must
// fail rather than claim that a private receipt table is protected.
func TestCredentialRemovalHeldDispatchOwnerIsolationGate(t *testing.T) {
	ctx,pool,done:=setupAppliedSettingsTest(t)
	defer done()
	var exists bool
	if err:=pool.QueryRow(ctx,`SELECT EXISTS (
		SELECT 1 FROM pg_namespace WHERE nspname='routegate_dispatch'
	)`).Scan(&exists);err!=nil {
		t.Fatal("cannot inspect held dispatch namespace",err)
	}
	if !exists {
		// Intentional: current canonical 170 has no held dispatch schema,
		// and no task is issued. This is safer than a fictitious protected DB.
		return
	}
	var current,owner string
	var canUse bool
	if err:=pool.QueryRow(ctx,`SELECT current_user,
		pg_get_userbyid(n.nspowner),
		has_schema_privilege(current_user,n.oid,'USAGE')
		FROM pg_namespace n WHERE n.nspname='routegate_dispatch'`).
		Scan(&current,&owner,&canUse);err!=nil {
		t.Fatal("cannot verify runtime role isolation",err)
	}
	if current==owner || canUse {
		t.Fatalf("unsafe held-dispatch schema: Manager runtime role %q is owner=%t or has schema USAGE=%t; require a separate migrator/independent coordinator before schema 171",
			current,current==owner,canUse)
	}
}
