-- RG-140: DORMANT Manager-owned pre-dispatch checkpoint schema.
-- No application writer or dispatch integration is enabled. This migration
-- only creates empty tables/triggers; it never selects or rewrites live jobs.
-- A checkpoint is NOT permission for Agent recovery or runtime unlock.
CREATE TABLE vpn_runtime_pre_dispatch_checkpoints (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    dispatch_epoch bigint GENERATED ALWAYS AS IDENTITY UNIQUE,
    task_id uuid NOT NULL,
    task_kind text NOT NULL CHECK (task_kind IN ('config_apply','vpn_core_service')),
    server_id uuid NOT NULL REFERENCES servers(id) ON DELETE RESTRICT,
    agent_id uuid NOT NULL REFERENCES agents(id) ON DELETE RESTRICT,
    agent_credential_generation bigint NOT NULL CHECK (agent_credential_generation > 0),
    expected_marker_sha256 text NOT NULL CHECK (expected_marker_sha256 ~ '^[0-9a-f]{64}$'),
    config_version_id uuid REFERENCES config_versions(id) ON DELETE RESTRICT,
    expected_config_sha256 text CHECK (expected_config_sha256 ~ '^[0-9a-f]{64}$'),
    committed_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    state text NOT NULL DEFAULT 'pre_dispatch_unattested'
        CHECK (state = 'pre_dispatch_unattested'),
    CHECK (
        (task_kind='config_apply' AND config_version_id IS NOT NULL AND expected_config_sha256 IS NOT NULL)
        OR
        (task_kind='vpn_core_service' AND config_version_id IS NULL AND expected_config_sha256 IS NULL)
    ),
    UNIQUE (task_kind,task_id),
    -- Until a later separately audited terminal/re-enrollment protocol exists,
    -- this permanently permits at most ONE experimental checkpoint per node.
    UNIQUE (server_id)
);

CREATE TABLE vpn_runtime_pre_dispatch_checkpoint_events (
    sequence bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    checkpoint_id uuid NOT NULL REFERENCES vpn_runtime_pre_dispatch_checkpoints(id) ON DELETE RESTRICT,
    action text NOT NULL CHECK (action = 'committed'),
    created_at timestamptz NOT NULL DEFAULT clock_timestamp()
);

CREATE FUNCTION routegate_pre_dispatch_checkpoint_guard()
RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE
    original_server uuid;
    original_agent uuid;
    original_generation bigint;
    original_config_version uuid;
    original_config_hash text;
BEGIN
    IF TG_OP <> 'INSERT' THEN
        RAISE EXCEPTION USING ERRCODE='P0146', MESSAGE='pre_dispatch_checkpoint_immutable';
    END IF;
    -- The caller supplies ONLY an existing job identity and kind. Never
    -- accept precomputed digests, node identity, credential epoch or time.
    IF NEW.server_id IS NOT NULL OR NEW.agent_id IS NOT NULL OR
       NEW.agent_credential_generation IS NOT NULL OR
       NEW.expected_marker_sha256 IS NOT NULL OR
       NEW.config_version_id IS NOT NULL OR
       NEW.expected_config_sha256 IS NOT NULL OR
       NEW.state <> 'pre_dispatch_unattested' OR
       NEW.committed_at < clock_timestamp() - interval '10 seconds' OR
       NEW.committed_at > clock_timestamp() + interval '1 second' THEN
        RAISE EXCEPTION USING ERRCODE='P0146', MESSAGE='pre_dispatch_checkpoint_source_required';
    END IF;
    IF NEW.task_kind='config_apply' THEN
        SELECT j.server_id,j.agent_id,a.credential_generation,
               cv.id,cv.config_hash
          INTO original_server,original_agent,original_generation,
               original_config_version,original_config_hash
          FROM config_apply_jobs j
          JOIN agents a ON a.id=j.agent_id AND a.server_id=j.server_id
          JOIN config_versions cv ON cv.id=j.config_version_id AND cv.server_id=j.server_id
         WHERE j.id=NEW.task_id AND j.action='apply' AND j.status='pending'
           AND j.started_at IS NULL AND j.completed_at IS NULL
           AND a.status <> 'disabled'
           AND a.capabilities @> '{"runtimeMutationFencingV1":true}'::jsonb
         FOR UPDATE OF j
         FOR SHARE OF a,cv;
    ELSE
        SELECT j.server_id,j.agent_id,a.credential_generation
          INTO original_server,original_agent,original_generation
          FROM agent_operation_jobs j
          JOIN agents a ON a.id=j.agent_id AND a.server_id=j.server_id
         WHERE j.id=NEW.task_id AND j.kind='vpn_core_service'
           AND j.operation='restart' AND j.status='pending'
           AND j.started_at IS NULL AND j.completed_at IS NULL
           AND a.status <> 'disabled'
           AND a.capabilities @> '{"runtimeMutationFencingV1":true}'::jsonb
         FOR UPDATE OF j
         FOR SHARE OF a;
    END IF;
    IF NOT FOUND OR original_server IS NULL OR original_agent IS NULL
       OR original_generation IS NULL OR original_generation<=0 OR
       (NEW.task_kind='config_apply' AND (
           original_config_version IS NULL OR
           original_config_hash !~ '^[0-9a-f]{64}$')) THEN
        RAISE EXCEPTION USING ERRCODE='P0146', MESSAGE='pre_dispatch_checkpoint_job_unavailable';
    END IF;
    -- A preexisting post-event recovery hold cannot be the source of a
    -- second independently claimed "initial" checkpoint.
    IF EXISTS (SELECT 1 FROM vpn_runtime_recovery_reservations
               WHERE server_id=original_server) THEN
        RAISE EXCEPTION USING ERRCODE='P0146', MESSAGE='pre_dispatch_checkpoint_node_held';
    END IF;

    NEW.server_id := original_server;
    NEW.agent_id := original_agent;
    NEW.agent_credential_generation := original_generation;
    NEW.config_version_id := original_config_version;
    NEW.expected_config_sha256 := original_config_hash;
    -- This exact byte contract is independently generated from Manager's
    -- locked job row, not from any Agent marker/receipt/witness assertion.
    NEW.expected_marker_sha256 := encode(sha256(convert_to(
        format('{"schemaVersion":1,"taskId":"%s","kind":"%s"}',
               NEW.task_id::text,NEW.task_kind),'UTF8')),'hex');
    RETURN NEW;
END; $$;

CREATE TRIGGER rg146_checkpoint_guard
    BEFORE INSERT OR UPDATE OR DELETE ON vpn_runtime_pre_dispatch_checkpoints
    FOR EACH ROW EXECUTE FUNCTION routegate_pre_dispatch_checkpoint_guard();

-- The audit insert belongs to the very same PostgreSQL transaction. A
-- failed audit must roll back the checkpoint; no writer can disclose success.
CREATE FUNCTION routegate_pre_dispatch_checkpoint_audit()
RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    INSERT INTO vpn_runtime_pre_dispatch_checkpoint_events(checkpoint_id,action)
    VALUES(NEW.id,'committed');
    RETURN NEW;
END; $$;
CREATE TRIGGER rg146_checkpoint_audit
    AFTER INSERT ON vpn_runtime_pre_dispatch_checkpoints
    FOR EACH ROW EXECUTE FUNCTION routegate_pre_dispatch_checkpoint_audit();

CREATE FUNCTION routegate_pre_dispatch_checkpoint_immutable()
RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION USING ERRCODE='P0146', MESSAGE='pre_dispatch_checkpoint_history_immutable';
END; $$;
CREATE TRIGGER rg146_checkpoint_event_immutable
    BEFORE UPDATE OR DELETE ON vpn_runtime_pre_dispatch_checkpoint_events
    FOR EACH ROW EXECUTE FUNCTION routegate_pre_dispatch_checkpoint_immutable();

CREATE TRIGGER rg146_checkpoint_no_truncate
    BEFORE TRUNCATE ON vpn_runtime_pre_dispatch_checkpoints
    FOR EACH STATEMENT EXECUTE FUNCTION routegate_pre_dispatch_checkpoint_immutable();
CREATE TRIGGER rg146_checkpoint_event_no_truncate
    BEFORE TRUNCATE ON vpn_runtime_pre_dispatch_checkpoint_events
    FOR EACH STATEMENT EXECUTE FUNCTION routegate_pre_dispatch_checkpoint_immutable();
