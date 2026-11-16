-- RG-140: immutable, non-executable task identity reservation, AFTER a
-- schema-163 outcome_unknown claim and BEFORE any future possible Agent send.
-- This is not an Agent job queue and does not authorize task delivery.
-- Keep the original 161/162/163/164 migration files byte-for-byte intact.
CREATE TABLE vpn_credential_revocation_task_reservations (
 task_id uuid PRIMARY KEY,
 claim_id uuid NOT NULL UNIQUE REFERENCES vpn_credential_revocation_dispatch_claims(claim_id) ON DELETE RESTRICT,
 operation_id uuid NOT NULL UNIQUE REFERENCES vpn_credential_revocations(id) ON DELETE RESTRICT,
 reserved_by text NOT NULL CHECK (reserved_by ~ '^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$'),
 server_id uuid NOT NULL,
 agent_id uuid NOT NULL,
 agent_credential_generation bigint NOT NULL CHECK (agent_credential_generation > 0),
 baseline_version_id uuid NOT NULL,
 candidate_hash text NOT NULL CHECK (candidate_hash ~ '^[0-9a-f]{64}$'),
 candidate_runtime_hash text NOT NULL CHECK (candidate_runtime_hash ~ '^[0-9a-f]{64}$'),
 task_kind text NOT NULL CHECK (task_kind = 'vless_credential_removal_v1'),
 state text NOT NULL CHECK (state = 'identity_reserved'),
 reserved_at timestamptz NOT NULL
);

-- The caller supplies ONLY the claimed ID. PostgreSQL assigns a fresh UUIDv4
-- and copies every identity/hash from immutable Manager history. It never
-- trusts caller-supplied job IDs, hashes, agents, status or timestamps.
-- No new FK to servers/agents: snapshots are protected by the claim's FKs,
-- whose related tables are already guarded while the parent is prepared.
CREATE FUNCTION routegate_revocation_task_identity_guard() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE
 c vpn_credential_revocation_dispatch_claims%ROWTYPE;
 p vpn_credential_revocations%ROWTYPE;
 confirmed vpn_credential_revocation_confirmations%ROWTYPE;
BEGIN
 IF TG_OP <> 'INSERT' THEN
  RAISE EXCEPTION USING ERRCODE='P0141',MESSAGE='revocation_task_identity_immutable';
 END IF;
 IF NEW.task_id IS NOT NULL OR NEW.operation_id IS NOT NULL OR
    NEW.reserved_by IS NOT NULL OR NEW.server_id IS NOT NULL OR
    NEW.agent_id IS NOT NULL OR NEW.agent_credential_generation IS NOT NULL OR
    NEW.baseline_version_id IS NOT NULL OR NEW.candidate_hash IS NOT NULL OR
    NEW.candidate_runtime_hash IS NOT NULL OR NEW.task_kind IS NOT NULL OR
    NEW.state IS NOT NULL OR NEW.reserved_at IS NOT NULL THEN
  RAISE EXCEPTION USING ERRCODE='P0141',MESSAGE='revocation_task_identity_caller_fields_forbidden';
 END IF;
 SELECT * INTO c FROM vpn_credential_revocation_dispatch_claims WHERE claim_id=NEW.claim_id;
 IF NOT FOUND OR c.state <> 'outcome_unknown' THEN
  RAISE EXCEPTION USING ERRCODE='P0141',MESSAGE='revocation_task_identity_claim_unavailable';
 END IF;
 SELECT * INTO p FROM vpn_credential_revocations WHERE id=c.operation_id;
 SELECT * INTO confirmed FROM vpn_credential_revocation_confirmations WHERE operation_id=c.operation_id;
 IF NOT FOUND OR p.state <> 'prepared' OR c.claimed_by <> p.requested_by
    OR c.operation_id <> p.id
    OR c.server_id <> p.server_id OR c.agent_id <> p.agent_id
    OR c.agent_credential_generation <> p.agent_credential_generation
    OR c.baseline_version_id <> p.baseline_version_id
    OR c.candidate_hash <> p.candidate_hash
    OR c.candidate_runtime_hash <> p.candidate_runtime_hash
    OR confirmed.confirmed_by <> p.requested_by
    OR confirmed.agent_id <> c.agent_id
    OR confirmed.agent_credential_generation <> c.agent_credential_generation
    OR confirmed.baseline_version_id <> c.baseline_version_id
    OR confirmed.candidate_hash <> c.candidate_hash
    OR confirmed.candidate_runtime_hash <> c.candidate_runtime_hash
    OR NOT confirmed.shared_restart_acknowledged THEN
  RAISE EXCEPTION USING ERRCODE='P0141',MESSAGE='revocation_task_identity_snapshot_mismatch';
 END IF;
 IF NOT EXISTS (
  SELECT 1 FROM agents a JOIN servers s ON s.id=c.server_id
  JOIN config_versions cv ON cv.id=c.baseline_version_id
  WHERE a.id=c.agent_id AND a.server_id=c.server_id
    AND a.credential_generation=c.agent_credential_generation
    AND a.last_authenticated_heartbeat_generation=a.credential_generation
    AND a.last_authenticated_heartbeat_at > now()-interval '90 seconds'
    AND a.status='online'
    AND s.active_config_version_id=c.baseline_version_id
    AND cv.server_id=c.server_id AND cv.status='applied'
 ) THEN
  RAISE EXCEPTION USING ERRCODE='P0141',MESSAGE='revocation_task_identity_stale_node';
 END IF;
 NEW.task_id := gen_random_uuid();
 NEW.operation_id := c.operation_id;
 NEW.reserved_by := c.claimed_by;
 NEW.server_id := c.server_id;
 NEW.agent_id := c.agent_id;
 NEW.agent_credential_generation := c.agent_credential_generation;
 NEW.baseline_version_id := c.baseline_version_id;
 NEW.candidate_hash := c.candidate_hash;
 NEW.candidate_runtime_hash := c.candidate_runtime_hash;
 NEW.task_kind := 'vless_credential_removal_v1';
 NEW.state := 'identity_reserved';
 NEW.reserved_at := clock_timestamp();
 RETURN NEW;
END; $$;

CREATE TRIGGER rg145_task_identity
 BEFORE INSERT OR UPDATE OR DELETE ON vpn_credential_revocation_task_reservations
 FOR EACH ROW EXECUTE FUNCTION routegate_revocation_task_identity_guard();
