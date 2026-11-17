-- Internal ID-binding only: neither a dispatch nor a version or credential mutation.
-- Preserve original revocation node/job/version guards verbatim.
CREATE TABLE vpn_credential_revocation_dispatch_bindings (
 operation_id uuid PRIMARY KEY REFERENCES vpn_credential_revocations(id),
 candidate_version_id uuid NOT NULL UNIQUE DEFAULT gen_random_uuid(),
 apply_job_id uuid NOT NULL UNIQUE DEFAULT gen_random_uuid(),
 agent_credential_generation bigint NOT NULL CHECK (agent_credential_generation > 0),
 baseline_version_id uuid NOT NULL REFERENCES config_versions(id),
 candidate_hash text NOT NULL CHECK (candidate_hash ~ '^[0-9a-f]{64}$'),
 candidate_runtime_hash text NOT NULL CHECK (candidate_runtime_hash ~ '^[0-9a-f]{64}$'),
 bound_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 CHECK (candidate_version_id <> apply_job_id),
 CHECK (candidate_version_id <> baseline_version_id),
 CHECK (apply_job_id <> baseline_version_id)
);

CREATE FUNCTION routegate_removal_dispatch_binding_guard() RETURNS trigger
LANGUAGE plpgsql AS $$
DECLARE p vpn_credential_revocations; node uuid;
BEGIN
 IF TG_OP <> 'INSERT' THEN
  RAISE EXCEPTION USING ERRCODE='P0152', MESSAGE='removal_dispatch_binding_retained';
 END IF;
 SELECT server_id INTO node FROM vpn_credential_revocations WHERE id=NEW.operation_id;
 IF node IS NULL THEN
  RAISE EXCEPTION USING ERRCODE='P0152', MESSAGE='removal_dispatch_binding_unreserved';
 END IF;
 -- Same server-before-preparation ordering as cancellation. No exception to
 -- any pre-existing node mutation guard is introduced by this migration.
 PERFORM id FROM servers WHERE id=node FOR UPDATE;
 SELECT * INTO p FROM vpn_credential_revocations WHERE id=NEW.operation_id FOR UPDATE;
 IF p.id IS NULL OR p.state <> 'prepared'
  OR NEW.agent_credential_generation <> p.agent_credential_generation
  OR NEW.baseline_version_id <> p.baseline_version_id
  OR NEW.candidate_hash <> p.candidate_hash
  OR NEW.candidate_runtime_hash <> p.candidate_runtime_hash
  OR NOT EXISTS (
    SELECT 1 FROM servers s JOIN agents a ON a.id=p.agent_id
      JOIN config_versions v ON v.id=p.baseline_version_id
    WHERE s.id=p.server_id AND a.server_id=s.id
      AND a.credential_generation=p.agent_credential_generation
      AND s.active_config_version_id=p.baseline_version_id
      AND v.server_id=p.server_id AND v.config_hash=p.baseline_hash
      AND v.status='applied' AND v.applied_at IS NOT NULL
  )
  OR EXISTS(SELECT 1 FROM config_versions WHERE id=NEW.candidate_version_id)
  OR EXISTS(SELECT 1 FROM config_apply_jobs WHERE id=NEW.apply_job_id)
 THEN
  RAISE EXCEPTION USING ERRCODE='P0152', MESSAGE='removal_dispatch_binding_invalid';
 END IF;
 RETURN NEW;
END; $$;
CREATE TRIGGER rg152_dispatch_binding
 BEFORE INSERT OR UPDATE OR DELETE ON vpn_credential_revocation_dispatch_bindings
 FOR EACH ROW EXECUTE FUNCTION routegate_removal_dispatch_binding_guard();
