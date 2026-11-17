package db

import (
	"errors"
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


// Confirm the startup guard itself fails closed when the Manager runtime DB
// login owns a private schema, despite an apparently restrictive PUBLIC
// revocation. The ephemeral database is exclusive to this integration test;
// no installed RouteGate database or service is touched.
func TestCredentialRemovalStartupRefusesOwnerOnlyHeldDispatch(t *testing.T) {
	ctx,pool,done:=setupAppliedSettingsTest(t)
	defer done()
	if err:=CheckHeldDispatchRuntimeIsolation(ctx,pool);err!=nil {
		t.Fatal("canonical 170 unexpectedly has held-dispatch authority",err)
	}
	_,err:=pool.Exec(ctx,`CREATE SCHEMA routegate_dispatch;
		REVOKE ALL ON SCHEMA routegate_dispatch FROM PUBLIC;`)
	if err!=nil {t.Fatal("create disposable owner-only schema",err)}
	t.Cleanup(func(){
		_,_ = pool.Exec(ctx,`DROP SCHEMA IF EXISTS routegate_dispatch CASCADE`)
	})
	if err:=CheckHeldDispatchRuntimeIsolation(ctx,pool);!errors.Is(err,ErrUnsafeHeldDispatchRuntimeRole){
		t.Fatal("Manager startup accepted a private schema it owns despite PUBLIC revocation",err)
	}
	if _,err=pool.Exec(ctx,`DROP SCHEMA routegate_dispatch`);err!=nil {
		t.Fatal("drop disposable held schema",err)
	}
	if err=CheckHeldDispatchRuntimeIsolation(ctx,pool);err!=nil {
		t.Fatal("Manager startup did not recover after unsafe schema was removed",err)
	}
}
