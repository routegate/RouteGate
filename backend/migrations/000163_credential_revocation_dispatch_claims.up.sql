-- RG-140 pre-dispatch fencing ONLY: no Agent job, API route, runtime mutation,
-- transport send, receipt acceptance or replay operation is created here.
-- The one-time claim deliberately starts in outcome_unknown: after a crash
-- even a "never sent" attempt cannot be automatically reclaimed and resent.
CREATE TABLE vpn_credential_revocation_dispatch_claims (
 claim_id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
 operation_id uuid NOT NULL UNIQUE REFERENCES vpn_credential_revocations(id) ON DELETE RESTRICT,
 claimed_by text NOT NULL CHECK(claimed_by ~ '^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$'),
 server_id uuid NOT NULL REFERENCES servers(id) ON DELETE RESTRICT,
 agent_id uuid NOT NULL REFERENCES agents(id) ON DELETE RESTRICT,
 agent_credential_generation bigint NOT NULL CHECK(agent_credential_generation > 0),
 baseline_version_id uuid NOT NULL REFERENCES config_versions(id) ON DELETE RESTRICT,
 confirmation_at timestamptz NOT NULL,
 candidate_hash text NOT NULL CHECK(candidate_hash ~ '^[0-9a-f]{64}$'),
 candidate_runtime_hash text NOT NULL CHECK(candidate_runtime_hash ~ '^[0-9a-f]{64}$'),
 state text NOT NULL DEFAULT 'outcome_unknown' CHECK(state='outcome_unknown'),
 claimed_at timestamptz NOT NULL DEFAULT clock_timestamp()
);

-- Lock only the prepared operation, not the parent server row: cancellation
-- locks server -> operation. The opposite lock order would deadlock.
-- A prepared operation already fences conflicting Apply/transfer mutations
-- in schema 161. Future dispatch STILL requires fresh, authenticated checks.
CREATE FUNCTION routegate_revocation_dispatch_claim_guard() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE
 original vpn_credential_revocations%ROWTYPE;
 confirmed vpn_credential_revocation_confirmations%ROWTYPE;
BEGIN
 IF TG_OP <> 'INSERT' THEN
  RAISE EXCEPTION USING ERRCODE='P0141',MESSAGE='revocation_dispatch_claim_immutable';
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

CREATE TRIGGER rg143_dispatch_claim
 BEFORE INSERT OR UPDATE OR DELETE ON vpn_credential_revocation_dispatch_claims
 FOR EACH ROW EXECUTE FUNCTION routegate_revocation_dispatch_claim_guard();

-- Cancellation is safe after merely confirming, but NEVER once a possible
-- dispatch has been claimed: a lost response could follow actual mutation.
-- The update already locks the parent row. The competing claim also locks
-- this parent before inserting, so no read-committed cancellation race.
CREATE FUNCTION routegate_revocation_dispatch_cancel_guard() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF OLD.state='prepared' AND NEW.state='cancelled'
    AND EXISTS (SELECT 1 FROM vpn_credential_revocation_dispatch_claims WHERE operation_id=OLD.id) THEN
  RAISE EXCEPTION USING ERRCODE='P0141',MESSAGE='revocation_dispatch_outcome_unknown';
 END IF;
 RETURN NEW;
END; $$;

CREATE TRIGGER rg143_dispatch_cancel
 BEFORE UPDATE OF state ON vpn_credential_revocations
 FOR EACH ROW EXECUTE FUNCTION routegate_revocation_dispatch_cancel_guard();
