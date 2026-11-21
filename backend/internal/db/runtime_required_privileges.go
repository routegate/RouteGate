package db

import (
	"context"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
)

// CheckRuntimeRequiredPrivileges adapts #652 to the canonical schema-170
// policy. It checks each privilege separately (PostgreSQL's comma-separated
// privilege argument means ANY, not ALL). Inspection is SELECT-only and never
// repairs a restored ACL. Call the shared authority guard and exact migration
// history check first; this check establishes minimum grants, not isolation.
func CheckRuntimeRequiredPrivileges(ctx context.Context, pool *pgxpool.Pool) error {
	var tables, privileges, lockTables, lockColumns, insertTables []string
	for _, policy := range runtimePolicy170 {
		for _, privilege := range strings.Split(policy.privileges, ", ") {
			tables = append(tables, policy.name)
			privileges = append(privileges, privilege)
		}
		if policy.lockColumn != "" {
			lockTables = append(lockTables, policy.name)
			lockColumns = append(lockColumns, policy.lockColumn)
		}
		if strings.Contains(policy.privileges, "INSERT") {
			insertTables = append(insertTables, policy.name)
		}
	}
	var ready bool
	err := pool.QueryRow(ctx, `SELECT
 pg_catalog.has_database_privilege(current_database(), 'CONNECT')
 AND pg_catalog.has_schema_privilege('public', 'USAGE')
 AND pg_catalog.has_table_privilege('public.schema_migrations', 'SELECT')
 AND NOT EXISTS (
   SELECT 1 FROM unnest($1::text[], $2::text[]) AS required(name, privilege)
   LEFT JOIN pg_catalog.pg_namespace n ON n.nspname='public'
   LEFT JOIN pg_catalog.pg_class c ON c.relnamespace=n.oid AND c.relname=required.name
   WHERE c.oid IS NULL OR c.relkind NOT IN ('r','p')
      OR NOT pg_catalog.has_table_privilege(c.oid, required.privilege)
 )
 AND NOT EXISTS (
   SELECT 1 FROM unnest($3::text[], $4::text[]) AS required(name, column_name)
   LEFT JOIN pg_catalog.pg_namespace n ON n.nspname='public'
   LEFT JOIN pg_catalog.pg_class c ON c.relnamespace=n.oid AND c.relname=required.name
   LEFT JOIN pg_catalog.pg_attribute a ON a.attrelid=c.oid
     AND a.attname=required.column_name AND a.attnum>0 AND NOT a.attisdropped
   WHERE c.oid IS NULL OR a.attnum IS NULL
      OR NOT pg_catalog.has_column_privilege(c.oid, a.attnum, 'UPDATE')
 )
 AND NOT EXISTS (
   SELECT 1 FROM pg_catalog.pg_class s
   JOIN pg_catalog.pg_depend d ON d.classid='pg_class'::regclass AND d.objid=s.oid
   JOIN pg_catalog.pg_class t ON t.oid=d.refobjid
   JOIN pg_catalog.pg_namespace n ON n.oid=t.relnamespace
   WHERE s.relkind='S' AND d.refclassid='pg_class'::regclass
     AND d.deptype IN ('a','i') AND n.nspname='public' AND t.relname=ANY($5::text[])
     AND CASE WHEN s.relkind='S' THEN
       NOT pg_catalog.has_sequence_privilege(s.oid, 'USAGE') ELSE false END
 )`, tables, privileges, lockTables, lockColumns, insertTables).Scan(&ready)
	if err != nil {
		return fmt.Errorf("runtime required privileges check: %w", err)
	}
	if !ready {
		return fmt.Errorf("external migration mode requires complete schema-170 runtime privileges; administrative reconciliation required")
	}
	return nil
}
