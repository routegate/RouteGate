-- Forward-only security fix for schema 163, never rewrite its recorded identity.
-- A dispatch claim's server foreign key also takes KEY SHARE implicitly.
-- The old trigger acquired operation FOR UPDATE first, then tried the FK;
-- cancellation acquires server FOR UPDATE first, then operation FOR UPDATE.
-- Consistent server -> operation order prevents FK-induced deadlocks.
-- Does not send an Agent task, change any credential, or grant execution.
CREATE OR REPLACE FUNCTION routegate_revocation_dispatch_claim_guard() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE
 original vpn_credential_revocations%ROWTYPE;
 confirmed vpn_credential_revocation_confirmations%ROWTYPE;
 locked_server_id uuid;
BEGIN
 IF TG_OP <> 'INSERT' THEN
  RAISE EXCEPTION USING ERRCODE='P0141',MESSAGE='revocation_dispatch_claim_immutable';
 END IF;
 -- An INSERT's server_id FK takes KEY SHARE. Acquire it BEFORE the
 -- parent operation's FOR UPDATE, matching cancellation's server-first order.
 -- This also protects authenticated callers who insert via direct SQL.
 SELECT id INTO locked_server_id FROM servers WHERE id=NEW.server_id FOR KEY SHARE;
 IF NOT FOUND THEN
  RAISE EXCEPTION USING ERRCODE='P0141',MESSAGE='revocation_dispatch_claim_stale_node';
 END IF;
 SELECT * INTO original FROM vpn_credential_revocations WHERE id=NEW.operation_id FOR UPDATE;
 IF NOT FOUND OR original.state <> 'prepared' THEN
  RAISE EXCEPTION USING ERRCODE='P0141',MESSAGE='revocation_not_prepared';
 END IF;
 SELECT * INTO confirmed FROM vpn_credential_revocation_confirmations
  WHERE operation_id=original.id;
 IF NOT FOUND OR NOT confirmed.shared_restart_acknowledged THEN
  RAISE EXCEPTION USING ERRCODE='P0141',MESSAGE='revocation_not_confirmed';
 END IF;
 IF NEW.claimed_by <> original.requested_by
    OR NEW.claimed_by <> confirmed.confirmed_by
    OR NEW.server_id <> original.server_id
    OR NEW.agent_id <> original.agent_id
    OR NEW.agent_id <> confirmed.agent_id
    OR NEW.agent_credential_generation <> original.agent_credential_generation
    OR NEW.agent_credential_generation <> confirmed.agent_credential_generation
    OR NEW.baseline_version_id <> original.baseline_version_id
    OR NEW.baseline_version_id <> confirmed.baseline_version_id
    OR NEW.candidate_hash <> original.candidate_hash
    OR NEW.candidate_hash <> confirmed.candidate_hash
    OR NEW.candidate_runtime_hash <> original.candidate_runtime_hash
    OR NEW.candidate_runtime_hash <> confirmed.candidate_runtime_hash
    OR NEW.state <> 'outcome_unknown' THEN
  RAISE EXCEPTION USING ERRCODE='P0141',MESSAGE='revocation_dispatch_claim_snapshot_mismatch';
 END IF;
 IF NOT EXISTS (
    SELECT 1 FROM agents a
    JOIN servers s ON s.id=original.server_id
    JOIN config_versions cv ON cv.id=original.baseline_version_id
    WHERE a.id=original.agent_id
      AND a.server_id=original.server_id
      AND a.credential_generation=original.agent_credential_generation
      AND a.status='online'
      AND a.last_authenticated_heartbeat_at > now()-interval '90 seconds'
      AND a.last_authenticated_heartbeat_generation=a.credential_generation
      AND s.active_config_version_id=original.baseline_version_id
      AND cv.server_id=original.server_id AND cv.status='applied'
 ) THEN
  RAISE EXCEPTION USING ERRCODE='P0141',MESSAGE='revocation_dispatch_claim_stale_node';
 END IF;
 -- All timestamps are assigned by PostgreSQL; a request cannot backdate
 -- itself into an earlier confirmation or declare it "succeeded".
 NEW.confirmation_at := confirmed.confirmed_at;
 NEW.claimed_at := clock_timestamp();
 RETURN NEW;
END; $$;

