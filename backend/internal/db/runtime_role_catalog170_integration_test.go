package db

import (
	"sort"
	"strings"
	"testing"
)

// A read-only, schema-170 public relation inventory reconstructed from the
// canonical migration chain. This is NOT an authorization or a blanket DML
// grant list: each future runtime privilege must still be reviewed separately.
// Historical #639 targets schema 175 and differs by 11 new / 13 obsolete
// table identities. Keep one canonical inventory rather than copying it.
const canonicalSchema170PublicTables = `
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
server_registration_tokens
server_routing_profiles
servers
telegram_pairing_sessions
telegram_update_state
traffic_limits
traffic_report_checkpoints
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
vpn_runtime_pre_dispatch_checkpoint_events
vpn_runtime_pre_dispatch_checkpoints
vpn_runtime_recovery_attempts
vpn_runtime_recovery_challenge_events
vpn_runtime_recovery_challenges
vpn_runtime_recovery_enrollment_preparation_events
vpn_runtime_recovery_enrollment_preparations
vpn_runtime_recovery_observation_nonce_events
vpn_runtime_recovery_observation_nonces
vpn_runtime_recovery_reservations
vpn_subscription_tokens
`

func TestRG140Schema170PublicTableInventory(t *testing.T) {
	ctx, pool, done := setupAppliedSettingsTest(t)
	defer done()
	var current bool
	if err := pool.QueryRow(ctx, `SELECT EXISTS(
		SELECT 1 FROM public.schema_migrations
		WHERE version='000170_credential_removal_dispatch_stages'
	)`).Scan(&current); err != nil || !current {
		t.Fatalf("canonical schema 170 was not applied: current=%v err=%v", current, err)
	}
	rows, err := pool.Query(ctx, `SELECT c.relname FROM pg_catalog.pg_class c
		JOIN pg_catalog.pg_namespace n ON n.oid=c.relnamespace
		WHERE n.nspname='public' AND c.relkind IN ('r','p')
		AND c.relname <> 'schema_migrations'`)
	if err != nil { t.Fatal(err) }
	actual := map[string]bool{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			rows.Close()
			t.Fatal(err)
		}
		actual[name]=true
	}
	err=rows.Err()
	rows.Close()
	if err != nil { t.Fatal(err) }
	expected := map[string]bool{}
	for _, name := range strings.Fields(canonicalSchema170PublicTables) {
		if expected[name] { t.Fatalf("duplicate schema-170 table identity: %s", name) }
		expected[name]=true
	}
	var missing, unexpected []string
	for name := range expected {
		if !actual[name] { missing=append(missing,name) }
	}
	for name := range actual {
		if !expected[name] { unexpected=append(unexpected,name) }
	}
	sort.Strings(missing)
	sort.Strings(unexpected)
	if len(missing)>0 || len(unexpected)>0 {
		t.Fatalf("schema-170 catalog mismatch; missing=%v unexpected=%v",missing,unexpected)
	}
}
