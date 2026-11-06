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

// Explicit schema-178 DML policy, not a discovery query or default grant.
// Every added table requires a reviewed policy change. In particular, future
// dispatch-consumption tables must NOT automatically inherit ordinary DML.
var runtimeTables = strings.Fields(`
agent_heartbeats
agent_operation_jobs
agent_platform_update_jobs
agents
audit_events
audit_log
auth_security_event_visibility
auth_sessions
config_apply_jobs
config_versions
deliveries
delivery_provider_settings
delivery_recipients
initial_setup_tokens
maintenance_cleanup_plans
managed_routing_rule_sets
node_group_members
node_groups
observability_agent_telemetry
observability_alert_acknowledgements
observability_alert_transitions
observability_alerts
observability_current_health
observability_diagnostic_runs
observability_events
observability_health_transitions
observability_notification_deliveries
observability_notification_intents
permissions
platform_update_rollout_entries
platform_update_rollouts
role_permissions
roles
routing_profile_rules
routing_profiles
runtime_recovery_admission_release_evidence
runtime_recovery_cleaned_commit_evidence
runtime_recovery_commit_evidence
runtime_recovery_events
runtime_recovery_finalization_evidence
runtime_recovery_mutation_grants
runtime_recovery_observation_approvals
runtime_recovery_observation_evidence
runtime_recovery_preparations
runtime_recovery_prepared_release_evidence
runtime_recovery_release_authorizations
runtime_recovery_retention_evidence
runtime_recovery_terminal_evidence
server_registration_tokens
server_routing_profiles
servers
telegram_pairing_sessions
telegram_update_state
traffic_limits
traffic_usage_daily
traffic_usage_events
update_jobs
user_roles
users
vpn_account_automatic_selection_policies
vpn_account_devices
vpn_account_node_groups
vpn_account_notes
vpn_account_presence
vpn_account_protocols
vpn_account_routing_profiles
vpn_account_transfer_nodes
vpn_account_transfers
vpn_accounts
vpn_client_profiles
vpn_credential_removal_dispatch_authorizations
vpn_credential_removal_dispatch_stages
vpn_credential_revocation_dispatch_bindings
vpn_credential_revocation_events
vpn_credential_revocations
vpn_subscription_tokens
`)

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
	if latest != "000178_retired_vless_credentials" {
		return fmt.Errorf("runtime role policy requires schema 178")
	}
	expected := map[string]bool{"schema_migrations": true}
	for _, table := range runtimeTables {
		expected[table] = true
	}
	rows, err := tx.Query(ctx, `SELECT c.relname FROM pg_catalog.pg_class c
 JOIN pg_catalog.pg_namespace n ON n.oid=c.relnamespace
 WHERE n.nspname='public' AND c.relkind IN ('r','p','v','m','f')`)
	if err != nil {
		return err
	}
	for rows.Next() {
		var table string
		if err := rows.Scan(&table); err != nil {
			rows.Close()
			return err
		}
		if !expected[table] {
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
 OR has_table_privilege($1,'public.schema_migrations','INSERT,UPDATE,DELETE,TRUNCATE,TRIGGER')`, name).Scan(&unsafe); err != nil {
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
	for _, table := range runtimeTables {
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
		if _, err := tx.Exec(ctx, "GRANT SELECT, INSERT, UPDATE, DELETE ON TABLE "+qualified+" TO "+ident); err != nil {
			return err
		}
	}
	// Only sequences owned by explicitly listed tables. No ALL SEQUENCES,
	// UPDATE/setval, or default privileges on future sequences.
	rows, err = tx.Query(ctx, `SELECT ns.nspname,s.relname
 FROM pg_catalog.pg_class s
 JOIN pg_catalog.pg_namespace ns ON ns.oid=s.relnamespace
 JOIN pg_catalog.pg_depend d ON d.classid='pg_class'::regclass AND d.objid=s.oid
 JOIN pg_catalog.pg_class t ON t.oid=d.refobjid
 JOIN pg_catalog.pg_namespace nt ON nt.oid=t.relnamespace
 WHERE s.relkind='S' AND d.refclassid='pg_class'::regclass
 AND d.deptype IN ('a','i') AND nt.nspname='public' AND t.relname=ANY($1::text[])`, runtimeTables)
	if err != nil {
		return err
	}
	var sequences []string
	for rows.Next() {
		var schema, sequence string
		if err := rows.Scan(&schema, &sequence); err != nil {
			rows.Close()
			return err
		}
		sequences = append(sequences, pgx.Identifier{schema, sequence}.Sanitize())
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, sequence := range sequences {
		if err := tx.QueryRow(ctx, `SELECT has_sequence_privilege($1,$2,'UPDATE')`, name, sequence).Scan(&unsafe); err != nil {
			return err
		}
		if unsafe {
			return fmt.Errorf("unsafe PUBLIC sequence UPDATE privilege")
		}
		if _, err := tx.Exec(ctx, "GRANT USAGE, SELECT ON SEQUENCE "+sequence+" TO "+ident); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}
