package db

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Opt-in destructive rehearsal on the disposable 'routegate_ci' database
// only. This is a future-role design test, NOT a production role migration.
// Crucially, the assertion uses a genuine second PostgreSQL login, not
// SET ROLE on the all-powerful migration-owner connection.
func TestCredentialRemovalHeldDispatchSeparateRolesRehearsal(t *testing.T) {
	if os.Getenv("ROUTEGATE_TEST_HELD_ROLE_REHEARSAL")!="1" {
		t.Skip("two-login PostgreSQL role rehearsal is opt-in")
	}
	ctx,ownerPool,done:=setupAppliedSettingsTest(t)
	defer done()

	var database string
	var superuser bool
	if err:=ownerPool.QueryRow(ctx,`SELECT current_database(),
		(SELECT rolsuper FROM pg_roles WHERE rolname=current_user)`).Scan(&database,&superuser);err!=nil {
		t.Fatal("cannot inspect disposable PostgreSQL role",err)
	}
	// Never run role creation/deletion against an arbitrary supplied URL.
	if database!="routegate_ci" || !superuser {
		t.Fatal("two-role rehearsal requires disposable routegate_ci PostgreSQL superuser")
	}
	const owner="rg140_test_dispatch_owner"
	const runtime="rg140_test_manager_runtime"
	var preexistingSchema,preexistingRoles int
	if err:=ownerPool.QueryRow(ctx,`SELECT
		(SELECT count(*) FROM pg_namespace WHERE nspname='routegate_dispatch'),
		(SELECT count(*) FROM pg_roles WHERE rolname IN($1,$2))`,owner,runtime).
		Scan(&preexistingSchema,&preexistingRoles);err!=nil || preexistingSchema!=0 || preexistingRoles!=0 {
		t.Fatal("role rehearsal will not overwrite a pre-existing schema or role",err)
	}
	// Always clean after the independent login is closed, before owner pool.
	rolesCreated:=false
	schemaCreated:=false
	defer func(){
		cleanup,cancel:=context.WithTimeout(context.Background(),10*time.Second)
		defer cancel()
		if schemaCreated {
			_,_=ownerPool.Exec(cleanup,`DROP SCHEMA IF EXISTS routegate_dispatch CASCADE`)
		}
		if rolesCreated {
			_,_=ownerPool.Exec(cleanup,`REVOKE rg140_test_dispatch_owner FROM rg140_test_manager_runtime`)
			_,_=ownerPool.Exec(cleanup,`DROP ROLE IF EXISTS rg140_test_manager_runtime`)
			_,_=ownerPool.Exec(cleanup,`DROP ROLE IF EXISTS rg140_test_dispatch_owner`)
		}
	}()
	var secret [24]byte
	if _,err:=rand.Read(secret[:]);err!=nil {t.Fatal(err)}
	password:=hex.EncodeToString(secret[:])
	for _,statement:=range []string{
		`CREATE ROLE rg140_test_dispatch_owner NOLOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOINHERIT`,
		fmt.Sprintf(`CREATE ROLE rg140_test_manager_runtime LOGIN PASSWORD '%s'
			NOSUPERUSER NOCREATEDB NOCREATEROLE NOINHERIT NOBYPASSRLS`,password),
	} {
		if _,err:=ownerPool.Exec(ctx,statement);err!=nil{
			t.Fatal("cannot provision disposable CI roles",err)
		}
		rolesCreated=true
	}
	// Schema was verified absent above; mark it as ours before DDL so a
	// partial multi-statement setup failure still removes any created objects.
	schemaCreated=true
	if _,err:=ownerPool.Exec(ctx,`GRANT CONNECT ON DATABASE routegate_ci TO rg140_test_manager_runtime;
		GRANT USAGE ON SCHEMA public TO rg140_test_manager_runtime;
		GRANT SELECT ON public.schema_migrations TO rg140_test_manager_runtime;
		CREATE SCHEMA routegate_dispatch AUTHORIZATION rg140_test_dispatch_owner;
		REVOKE ALL ON SCHEMA routegate_dispatch FROM PUBLIC;
		CREATE TABLE routegate_dispatch.held_removals (id bigint PRIMARY KEY);
		ALTER TABLE routegate_dispatch.held_removals OWNER TO rg140_test_dispatch_owner;
		REVOKE ALL ON ALL TABLES IN SCHEMA routegate_dispatch FROM PUBLIC;`);err!=nil{
		t.Fatal("cannot set up isolated owner/runtime ACL",err)
	}

	cfg,err:=pgxpool.ParseConfig(os.Getenv("ROUTEGATE_TEST_DATABASE_URL"))
	if err!=nil {t.Fatal(err)}
	cfg.ConnConfig.User=runtime
	cfg.ConnConfig.Password=password
	cfg.MinConns=0
	cfg.MaxConns=2
	login,err:=pgxpool.NewWithConfig(ctx,cfg)
	if err!=nil {t.Fatal("cannot create distinct runtime connection",err)}
	defer login.Close()
	if err=login.Ping(ctx);err!=nil {t.Fatal("runtime login cannot connect",err)}

	var session,current string
	if err=login.QueryRow(ctx,`SELECT session_user,current_user`).Scan(&session,&current);
		err!=nil || session!=runtime || current!=runtime {
		t.Fatal("runtime rehearsal did not use an independent database login",err)
	}
	var n int
	if err=login.QueryRow(ctx,`SELECT count(*) FROM public.schema_migrations`).Scan(&n);err!=nil || n==0 {
		t.Fatal("least-privileged runtime cannot read ordinary granted public data",err)
	}
	if err=RequireAppliedMigrations(ctx,login,"../../migrations");err!=nil {
		t.Fatal("read-only external migration preflight failed on true runtime login",err)
	}
	if err=CheckHeldDispatchRuntimeIsolation(ctx,login);err!=nil {
		t.Fatal("isolated runtime role was falsely rejected",err)
	}
	if err=CheckExternalMigrationRuntimeRole(ctx,login);err!=nil {
		t.Fatal("non-owner external runtime was rejected",err)
	}
	if err=CheckExternalMigrationRuntimeRole(ctx,ownerPool);!errors.Is(err,ErrUnsafeExternalMigrationRuntimeRole) {
		t.Fatal("external runtime accepted privileged disposable database owner",err)
	}
	for _,statement:=range []string{
		`SELECT count(*) FROM routegate_dispatch.held_removals`,
		`INSERT INTO routegate_dispatch.held_removals(id) VALUES(1)`,
		`CREATE TABLE routegate_dispatch.forged_receipt(id integer)`,
		`SET ROLE rg140_test_dispatch_owner`,
	} {
		_,err:=login.Exec(ctx,statement)
		var p *pgconn.PgError
		if !errors.As(err,&p)||p.Code!="42501" {
			t.Fatalf("runtime role unexpectedly accessed protected dispatch authority: %q: %v",statement,err)
		}
	}
	// NOINHERIT does not prevent SET ROLE after role membership is granted.
	// The guard must detect even a membership that currently has no USAGE.
	if _,err=ownerPool.Exec(ctx,`GRANT rg140_test_dispatch_owner TO rg140_test_manager_runtime`);err!=nil {
		t.Fatal("could not simulate indirect privileged membership",err)
	}
	if err=CheckHeldDispatchRuntimeIsolation(ctx,login);!errors.Is(err,ErrUnsafeHeldDispatchRuntimeRole) {
		t.Fatal("runtime role membership gave a SET ROLE escalation path",err)
	}
	if err=CheckExternalMigrationRuntimeRole(ctx,login);!errors.Is(err,ErrUnsafeExternalMigrationRuntimeRole) {
		t.Fatal("external runtime accepted private-owner SET ROLE membership",err)
	}
	if _,err=ownerPool.Exec(ctx,`REVOKE rg140_test_dispatch_owner FROM rg140_test_manager_runtime`);err!=nil {
		t.Fatal("could not remove simulated privileged membership",err)
	}
	if err=CheckHeldDispatchRuntimeIsolation(ctx,login);err!=nil {
		t.Fatal("runtime role did not return to isolated state",err)
	}
	if err=CheckExternalMigrationRuntimeRole(ctx,login);err!=nil {
		t.Fatal("external runtime did not recover after role membership revocation",err)
	}
	// A future runtime database login must not be promoted to a powerful
	// database administrator even without explicit schema ownership.
	if _,err=ownerPool.Exec(ctx,`ALTER ROLE rg140_test_manager_runtime CREATEDB`);err!=nil {t.Fatal(err)}
	if err=CheckHeldDispatchRuntimeIsolation(ctx,login);!errors.Is(err,ErrUnsafeHeldDispatchRuntimeRole) {
		t.Fatal("CREATEDB runtime credential escaped the security boundary",err)
	}
	if err=CheckExternalMigrationRuntimeRole(ctx,login);!errors.Is(err,ErrUnsafeExternalMigrationRuntimeRole) {
		t.Fatal("external runtime accepted CREATEDB role",err)
	}
	if _,err=ownerPool.Exec(ctx,`ALTER ROLE rg140_test_manager_runtime NOCREATEDB`);err!=nil {t.Fatal(err)}
	if _,err=ownerPool.Exec(ctx,`GRANT CREATE ON DATABASE routegate_ci TO rg140_test_manager_runtime`);err!=nil {t.Fatal(err)}
	if err=CheckHeldDispatchRuntimeIsolation(ctx,login);!errors.Is(err,ErrUnsafeHeldDispatchRuntimeRole) {
		t.Fatal("database CREATE grant escaped the security boundary",err)
	}
	if err=CheckExternalMigrationRuntimeRole(ctx,login);!errors.Is(err,ErrUnsafeExternalMigrationRuntimeRole) {
		t.Fatal("external runtime accepted database CREATE grant",err)
	}
	if _,err=ownerPool.Exec(ctx,`REVOKE CREATE ON DATABASE routegate_ci FROM rg140_test_manager_runtime`);err!=nil {t.Fatal(err)}
	if err=CheckHeldDispatchRuntimeIsolation(ctx,login);err!=nil {
		t.Fatal("least-privileged role did not recover after administrator grants removed",err)
	}
	if err=CheckExternalMigrationRuntimeRole(ctx,login);err!=nil {
		t.Fatal("external runtime did not recover after privileged grants removed",err)
	}
}
