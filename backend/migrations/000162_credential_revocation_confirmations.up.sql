-- Dormant, forward-only confirmation history; NOT an execution queue.
-- No API handler, Agent task, runtime mutation or dispatch claim is installed.
-- Preserve the complete and immutable 000161 preparation migration.
CREATE TABLE vpn_credential_revocation_confirmations (
 operation_id uuid PRIMARY KEY REFERENCES vpn_credential_revocations(id) ON DELETE RESTRICT,
 confirmed_by text NOT NULL CHECK(confirmed_by ~ '^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$'),
 baseline_version_id uuid NOT NULL REFERENCES config_versions(id) ON DELETE RESTRICT,
 agent_id uuid NOT NULL REFERENCES agents(id) ON DELETE RESTRICT,
 agent_credential_generation bigint NOT NULL CHECK(agent_credential_generation > 0),
 candidate_hash text NOT NULL CHECK(candidate_hash ~ '^[0-9a-f]{64}$'),
 candidate_runtime_hash text NOT NULL CHECK(candidate_runtime_hash ~ '^[0-9a-f]{64}$'),
 shared_restart_acknowledged boolean NOT NULL CHECK(shared_restart_acknowledged),
 confirmed_at timestamptz NOT NULL DEFAULT clock_timestamp()
);

-- The parent row is locked before an insert, making an in-flight
-- cancellation and the confirmation mutually exclusive in a transaction.
-- A committed confirmation is immutable history, NOT authority to execute.
-- If the parent is subsequently cancelled the confirmation remains recorded,
-- and any eventual dispatcher MUST additionally check parent.state='prepared'.
CREATE FUNCTION routegate_revocation_confirmation_guard() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE original vpn_credential_revocations%ROWTYPE;
BEGIN
 IF TG_OP <> 'INSERT' THEN
  RAISE EXCEPTION USING ERRCODE='P0141', MESSAGE='revocation_confirmation_immutable';
 END IF;
 SELECT * INTO original FROM vpn_credential_revocations
  WHERE id=NEW.operation_id FOR UPDATE;
 IF NOT FOUND OR original.state <> 'prepared' THEN
  RAISE EXCEPTION USING ERRCODE='P0141', MESSAGE='revocation_not_prepared';
 END IF;
 IF NEW.confirmed_by <> original.requested_by
  OR NEW.baseline_version_id <> original.baseline_version_id
  OR NEW.agent_id <> original.agent_id
  OR NEW.agent_credential_generation <> original.agent_credential_generation
  OR NEW.candidate_hash <> original.candidate_hash
  OR NEW.candidate_runtime_hash <> original.candidate_runtime_hash THEN
  RAISE EXCEPTION USING ERRCODE='P0141', MESSAGE='revocation_confirmation_snapshot_mismatch';
 END IF;
 NEW.confirmed_at := clock_timestamp();
 RETURN NEW;
END; $$;

CREATE TRIGGER rg142_confirmation
 BEFORE INSERT OR UPDATE OR DELETE ON vpn_credential_revocation_confirmations
 FOR EACH ROW EXECUTE FUNCTION routegate_revocation_confirmation_guard();
