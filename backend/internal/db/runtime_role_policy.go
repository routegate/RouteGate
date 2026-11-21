package db

// Schema-170 policy adapted from #639/#652. One operation-level inventory
// serves provisioning, required-grant inspection and the catalog test.
// SELECT is explicit on every relation; writes follow current repositories,
// SECURITY INVOKER trigger effects and lock requirements. See the review in
// docs/development/rg140-schema170-runtime-role-review.md for evidence.
// No future table, dispatch schema or sequence receives default privileges.
type runtimeTablePolicy struct {
	name       string
	privileges string
	lockColumn string // narrow UPDATE for row locks on trigger-immutable rows
}

var runtimePolicy170 = []runtimeTablePolicy{
	{"agent_heartbeats", "SELECT, DELETE", ""},
	{"agent_operation_jobs", "SELECT, INSERT, UPDATE", ""},
	{"agent_platform_update_jobs", "SELECT, INSERT, UPDATE", ""},
	{"agents", "SELECT, INSERT, UPDATE", ""},
	{"audit_events", "SELECT, INSERT, DELETE", ""},
	{"audit_log", "SELECT", ""},
	{"auth_security_event_visibility", "SELECT, INSERT, UPDATE", ""},
	{"auth_sessions", "SELECT, INSERT, UPDATE, DELETE", ""},
	{"config_apply_jobs", "SELECT, INSERT, UPDATE, DELETE", ""},
	{"config_versions", "SELECT, INSERT, UPDATE, DELETE", ""},
	{"deliveries", "SELECT, INSERT, UPDATE", ""},
	{"delivery_provider_settings", "SELECT, INSERT, UPDATE, DELETE", ""},
	{"delivery_recipients", "SELECT, INSERT, UPDATE, DELETE", ""},
	{"initial_setup_tokens", "SELECT, INSERT, UPDATE, DELETE", ""},
	{"maintenance_cleanup_plans", "SELECT, INSERT, UPDATE", ""},
	{"managed_routing_rule_sets", "SELECT, UPDATE", ""},
	{"node_group_members", "SELECT, INSERT, UPDATE, DELETE", ""},
	{"node_groups", "SELECT, INSERT, UPDATE, DELETE", ""},
	{"observability_agent_telemetry", "SELECT, INSERT, UPDATE", ""},
	{"observability_alert_acknowledgements", "SELECT", ""},
	{"observability_alert_transitions", "SELECT, INSERT", ""},
	{"observability_alerts", "SELECT, INSERT, UPDATE", ""},
	{"observability_current_health", "SELECT, INSERT, UPDATE", ""},
	{"observability_diagnostic_runs", "SELECT, INSERT, UPDATE, DELETE", ""},
	{"observability_events", "SELECT, DELETE", ""},
	{"observability_health_transitions", "SELECT, INSERT, DELETE", ""},
	{"observability_notification_deliveries", "SELECT, INSERT", ""},
	{"observability_notification_intents", "SELECT, INSERT, UPDATE", ""},
	{"permissions", "SELECT, INSERT, UPDATE", ""},
	{"platform_update_rollout_entries", "SELECT, INSERT, UPDATE", ""},
	{"platform_update_rollouts", "SELECT, INSERT, UPDATE", ""},
	{"role_permissions", "SELECT, INSERT, UPDATE", ""},
	{"roles", "SELECT, INSERT, UPDATE", ""},
	{"routing_profile_rules", "SELECT, INSERT, UPDATE, DELETE", ""},
	{"routing_profiles", "SELECT, INSERT, UPDATE, DELETE", ""},
	{"server_registration_tokens", "SELECT, INSERT, UPDATE, DELETE", ""},
	{"server_routing_profiles", "SELECT, INSERT, UPDATE, DELETE", ""},
	{"servers", "SELECT, INSERT, UPDATE, DELETE", ""},
	{"telegram_pairing_sessions", "SELECT, INSERT, UPDATE, DELETE", ""},
	{"telegram_update_state", "SELECT, UPDATE", ""},
	{"traffic_limits", "SELECT, INSERT, UPDATE", ""},
	{"traffic_report_checkpoints", "SELECT, INSERT, UPDATE", ""},
	{"traffic_usage_daily", "SELECT, INSERT, UPDATE, DELETE", ""},
	{"traffic_usage_events", "SELECT, INSERT, DELETE", ""},
	{"update_jobs", "SELECT, INSERT, UPDATE", ""},
	{"user_roles", "SELECT, INSERT, UPDATE, DELETE", ""},
	{"users", "SELECT, INSERT, UPDATE", ""},
	{"vpn_account_automatic_selection_policies", "SELECT, INSERT, UPDATE", ""},
	{"vpn_account_devices", "SELECT, INSERT, UPDATE", ""},
	{"vpn_account_node_groups", "SELECT, INSERT, UPDATE, DELETE", ""},
	{"vpn_account_notes", "SELECT, INSERT, UPDATE", ""},
	{"vpn_account_presence", "SELECT, INSERT, DELETE", ""},
	{"vpn_account_protocols", "SELECT, INSERT, UPDATE", ""},
	{"vpn_account_routing_profiles", "SELECT, INSERT, UPDATE, DELETE", ""},
	{"vpn_account_transfer_nodes", "SELECT, INSERT, DELETE", ""},
	{"vpn_account_transfers", "SELECT, INSERT, UPDATE", ""},
	{"vpn_accounts", "SELECT, INSERT, UPDATE, DELETE", ""},
	{"vpn_client_profiles", "SELECT, INSERT, UPDATE", ""},
	{"vpn_credential_removal_dispatch_authorizations", "SELECT, INSERT", ""},
	{"vpn_credential_removal_dispatch_stages", "SELECT, INSERT", ""},
	{"vpn_credential_revocation_dispatch_bindings", "SELECT, INSERT", ""},
	{"vpn_credential_revocation_events", "SELECT, INSERT", ""},
	{"vpn_credential_revocations", "SELECT, INSERT, UPDATE", ""},
	{"vpn_runtime_pre_dispatch_checkpoint_events", "SELECT", ""},
	{"vpn_runtime_pre_dispatch_checkpoints", "SELECT", ""},
	{"vpn_runtime_recovery_attempts", "SELECT, INSERT", "id"},
	{"vpn_runtime_recovery_challenge_events", "SELECT, INSERT", ""},
	{"vpn_runtime_recovery_challenges", "SELECT, INSERT, UPDATE", ""},
	{"vpn_runtime_recovery_enrollment_preparation_events", "SELECT, INSERT", ""},
	{"vpn_runtime_recovery_enrollment_preparations", "SELECT, INSERT", ""},
	{"vpn_runtime_recovery_observation_nonce_events", "SELECT, INSERT", ""},
	{"vpn_runtime_recovery_observation_nonces", "SELECT, INSERT, UPDATE", ""},
	{"vpn_runtime_recovery_reservations", "SELECT", "id"},
	{"vpn_subscription_tokens", "SELECT, INSERT, UPDATE", ""},
}

func runtimeTableNames() []string {
	names := make([]string, 0, len(runtimePolicy170))
	for _, table := range runtimePolicy170 {
		names = append(names, table.name)
	}
	return names
}
