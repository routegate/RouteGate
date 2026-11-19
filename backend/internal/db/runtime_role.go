package db

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var runtimeRoleName = regexp.MustCompile(`^[a-z][a-z0-9_]{0,62}$`)

// ProvisionRuntimeRole creates a NEW NOLOGIN role atomically. It never adopts,
// repairs, rotates credentials for, or changes memberships of an existing role.
// A separate trusted installer must provision authentication and enable LOGIN.
// This is the ordinary application role, NOT a scoped dispatch executor.
func ProvisionRuntimeRole(ctx context.Context, pool *pgxpool.Pool, name string) error {
	if !runtimeRoleName.MatchString(name) || strings.HasPrefix(name, "pg_") {
		return fmt.Errorf("invalid runtime role name")
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		_ = tx.Rollback(cleanup)
	}()
	var latest string
	if err := tx.QueryRow(ctx, `SELECT COALESCE(max(version),'') FROM public.schema_migrations`).Scan(&latest); err != nil {
		return err
	}
	if latest != "000170_credential_removal_dispatch_stages" {
		return fmt.Errorf("runtime role policy requires canonical schema 170")
	}
	expected := map[string]bool{"schema_migrations": true}
	for _, table := range runtimePolicy170 {
		expected[table.name] = true
	}
	rows, err := tx.Query(ctx, `SELECT c.relname, c.relkind::text FROM pg_catalog.pg_class c
 JOIN pg_catalog.pg_namespace n ON n.oid=c.relnamespace
 WHERE n.nspname='public' AND c.relkind IN ('r','p','v','m','f')`)
	if err != nil {
		return err
	}
	for rows.Next() {
		var table, kind string
		if err := rows.Scan(&table, &kind); err != nil {
			rows.Close()
			return err
		}
		if !expected[table] || (kind != "r" && kind != "p") {
			rows.Close()
			return fmt.Errorf("runtime role policy: unexpected public relation")
		}
		delete(expected, table)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	if len(expected) != 0 {
		return fmt.Errorf("runtime role policy: missing public relations")
	}
	ident := pgx.Identifier{name}.Sanitize()
	if _, err := tx.Exec(ctx, "CREATE ROLE "+ident+" NOLOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOINHERIT NOREPLICATION NOBYPASSRLS"); err != nil {
		return fmt.Errorf("create new runtime role (existing roles are refused): %w", err)
	}
	// PUBLIC privileges can defeat an otherwise restricted new role. Refuse
	// unsafe schema/database defaults; do not rewrite shared ACLs implicitly.
	var unsafe bool
	if err := tx.QueryRow(ctx, `SELECT has_schema_privilege($1,'public','CREATE')
 OR has_database_privilege($1,current_database(),'CREATE')
 OR has_table_privilege($1,'public.schema_migrations','INSERT,UPDATE,DELETE,TRUNCATE,TRIGGER,REFERENCES')`, name).Scan(&unsafe); err != nil {
		return err
	}
	if unsafe {
		return fmt.Errorf("unsafe PUBLIC privileges for runtime role")
	}
	var database string
	if err := tx.QueryRow(ctx, `SELECT current_database()`).Scan(&database); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, "GRANT CONNECT ON DATABASE "+pgx.Identifier{database}.Sanitize()+" TO "+ident+
		"; GRANT USAGE ON SCHEMA public TO "+ident+
		"; GRANT SELECT ON public.schema_migrations TO "+ident+
		"; ALTER ROLE "+ident+" SET search_path = pg_catalog, public, pg_temp"); err != nil {
		return err
	}
	var insertTables []string
	for _, policy := range runtimePolicy170 {
		table := policy.name
		qualified := pgx.Identifier{"public", table}.Sanitize()
		if _, err := tx.Exec(ctx, "LOCK TABLE "+qualified+" IN SHARE MODE"); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `SELECT has_table_privilege($1,$2,'TRUNCATE,TRIGGER,REFERENCES')`, name, qualified).Scan(&unsafe); err != nil {
			return err
		}
		if unsafe {
			return fmt.Errorf("unsafe PUBLIC privileges on runtime table %s", table)
		}
		// Inherited PUBLIC DML must not silently widen this narrower policy.
		// Column grants count too; only the reviewed lock column is exempt.
		for _, privilege := range []string{"INSERT", "UPDATE", "DELETE"} {
			if strings.Contains(policy.privileges, privilege) {
				continue
			}
			if privilege == "DELETE" {
				err = tx.QueryRow(ctx, `SELECT has_table_privilege($1,$2,'DELETE')`, name, qualified).Scan(&unsafe)
			} else {
				allowedColumn := ""
				if privilege == "UPDATE" {
					allowedColumn = policy.lockColumn
				}
				err = tx.QueryRow(ctx, `SELECT EXISTS (
					SELECT 1 FROM pg_catalog.pg_attribute a
					WHERE a.attrelid=$2::regclass AND a.attnum>0 AND NOT a.attisdropped
					AND a.attname<>$4
					AND pg_catalog.has_column_privilege($1,a.attrelid,a.attnum,$3)
				)`, name, qualified, privilege, allowedColumn).Scan(&unsafe)
			}
			if err != nil {
				return err
			}
			if unsafe {
				return fmt.Errorf("PUBLIC %s exceeds runtime policy on %s", privilege, table)
			}
		}
		if _, err := tx.Exec(ctx, "GRANT "+policy.privileges+" ON TABLE "+qualified+" TO "+ident); err != nil {
			return err
		}
		if policy.lockColumn != "" {
			if _, err := tx.Exec(ctx, "GRANT UPDATE ("+pgx.Identifier{policy.lockColumn}.Sanitize()+") ON TABLE "+qualified+" TO "+ident); err != nil {
				return err
			}
		}
		if strings.Contains(policy.privileges, "INSERT") {
			insertTables = append(insertTables, table)
		}
	}
	// Only sequences owned by explicitly insertable tables. No ALL SEQUENCES,
	// UPDATE/setval, or default privileges on future sequences. Current writers
	// use nextval defaults, so USAGE suffices; SELECT/currval is not required.
	rows, err = tx.Query(ctx, `SELECT ns.nspname,s.relname, EXISTS (
 SELECT 1 FROM pg_catalog.pg_depend d
 JOIN pg_catalog.pg_class t ON t.oid=d.refobjid
 JOIN pg_catalog.pg_namespace nt ON nt.oid=t.relnamespace
 WHERE d.classid='pg_class'::regclass AND d.objid=s.oid
 AND d.refclassid='pg_class'::regclass AND d.deptype IN ('a','i')
 AND nt.nspname='public' AND t.relname=ANY($1::text[]))
 FROM pg_catalog.pg_class s
 JOIN pg_catalog.pg_namespace ns ON ns.oid=s.relnamespace
 WHERE s.relkind='S' AND ns.nspname='public'`, insertTables)
	if err != nil {
		return err
	}
	type sequenceGrant struct {
		name   string
		needed bool
	}
	var sequences []sequenceGrant
	for rows.Next() {
		var schema, sequence string
		var needed bool
		if err := rows.Scan(&schema, &sequence, &needed); err != nil {
			rows.Close()
			return err
		}
		sequences = append(sequences, sequenceGrant{pgx.Identifier{schema, sequence}.Sanitize(), needed})
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, sequence := range sequences {
		if err := tx.QueryRow(ctx, `SELECT has_sequence_privilege($1,$2,'UPDATE')`, name, sequence.name).Scan(&unsafe); err != nil {
			return err
		}
		if unsafe {
			return fmt.Errorf("unsafe PUBLIC sequence UPDATE privilege")
		}
		if !sequence.needed {
			continue
		}
		if _, err := tx.Exec(ctx, "GRANT USAGE ON SEQUENCE "+sequence.name+" TO "+ident); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}
