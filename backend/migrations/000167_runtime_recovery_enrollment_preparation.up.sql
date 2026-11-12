-- RG-140: dormant enrollment PREPARATION only; an enrolled Agent must
-- never be inferred from this table. No Runner, dispatcher, HTTP route,
-- installer or runtime mutation gate reads/writes this state.
-- Creating these empty tables does not change legacy Agent admission.
CREATE TABLE vpn_runtime_recovery_enrollment_preparations (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    agent_id uuid NOT NULL UNIQUE REFERENCES agents(id) ON DELETE RESTRICT,
    server_id uuid NOT NULL UNIQUE REFERENCES servers(id) ON DELETE RESTRICT,
    agent_credential_generation bigint NOT NULL CHECK (agent_credential_generation > 0),
    phase text NOT NULL DEFAULT 'prepared_unattested'
        CHECK (phase = 'prepared_unattested'),
    prepared_at timestamptz NOT NULL DEFAULT clock_timestamp()
);

CREATE TABLE vpn_runtime_recovery_enrollment_preparation_events (
    sequence bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    preparation_id uuid NOT NULL REFERENCES vpn_runtime_recovery_enrollment_preparations(id) ON DELETE RESTRICT,
    action text NOT NULL CHECK (action = 'prepared_unattested'),
    event_at timestamptz NOT NULL DEFAULT clock_timestamp()
);

CREATE FUNCTION routegate_recovery_enrollment_prepare_guard()
RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE
    manager_server uuid;
    manager_generation bigint;
BEGIN
    IF TG_OP <> 'INSERT' THEN
        RAISE EXCEPTION USING ERRCODE='P0147',
            MESSAGE='recovery_enrollment_preparation_immutable';
    END IF;
    -- Only the selected Manager Agent row is caller input. A remote Agent
    -- cannot provide its generation, node, phase or any "attested" state.
    IF NEW.server_id IS NOT NULL OR
       NEW.agent_credential_generation IS NOT NULL OR
       NEW.phase <> 'prepared_unattested' THEN
        RAISE EXCEPTION USING ERRCODE='P0147',
            MESSAGE='recovery_enrollment_claimed_identity_refused';
    END IF;

    SELECT a.server_id, a.credential_generation
      INTO manager_server, manager_generation
      FROM agents a
      JOIN servers s ON s.id=a.server_id
     WHERE a.id=NEW.agent_id AND a.status <> 'disabled'
       AND a.capabilities @> '{"runtimeMutationFencingV1":true}'::jsonb
       AND a.credential_generation > 0
     FOR UPDATE OF a, s;
    IF NOT FOUND OR manager_server IS NULL OR manager_generation IS NULL THEN
        RAISE EXCEPTION USING ERRCODE='P0147',
            MESSAGE='recovery_enrollment_agent_not_ready';
    END IF;

    -- Preparation requires a quiet Manager snapshot; this does NOT prove
    -- the host is quiet, and no existing job path is gated by preparation.
    IF EXISTS (SELECT 1 FROM vpn_runtime_recovery_reservations
               WHERE server_id=manager_server)
       OR EXISTS (SELECT 1 FROM vpn_runtime_pre_dispatch_checkpoints
                  WHERE server_id=manager_server)
       OR EXISTS (SELECT 1 FROM config_apply_jobs
                  WHERE server_id=manager_server AND status IN ('pending','in_progress'))
       OR EXISTS (SELECT 1 FROM agent_operation_jobs
                  WHERE server_id=manager_server AND status IN ('pending','in_progress'))
       OR EXISTS (SELECT 1 FROM agent_platform_update_jobs
                  WHERE server_id=manager_server
                    AND status IN ('pending','in_progress','mutation_dispatched','outcome_unknown'))
    THEN
        RAISE EXCEPTION USING ERRCODE='P0147',
            MESSAGE='recovery_enrollment_manager_not_quiet';
    END IF;

    NEW.server_id := manager_server;
    NEW.agent_credential_generation := manager_generation;
    NEW.prepared_at := clock_timestamp();
    RETURN NEW;
END; $$;

CREATE TRIGGER rg147_enrollment_prepare_guard
    BEFORE INSERT OR UPDATE OR DELETE ON vpn_runtime_recovery_enrollment_preparations
    FOR EACH ROW EXECUTE FUNCTION routegate_recovery_enrollment_prepare_guard();

CREATE FUNCTION routegate_recovery_enrollment_prepare_audit()
RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    INSERT INTO vpn_runtime_recovery_enrollment_preparation_events(preparation_id,action)
    VALUES(NEW.id, 'prepared_unattested');
    RETURN NEW;
END; $$;
CREATE TRIGGER rg147_enrollment_prepare_audit
    AFTER INSERT ON vpn_runtime_recovery_enrollment_preparations
    FOR EACH ROW EXECUTE FUNCTION routegate_recovery_enrollment_prepare_audit();

CREATE FUNCTION routegate_recovery_enrollment_history_immutable()
RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION USING ERRCODE='P0147',
        MESSAGE='recovery_enrollment_preparation_history_immutable';
END; $$;
CREATE TRIGGER rg147_enrollment_event_immutable
    BEFORE UPDATE OR DELETE ON vpn_runtime_recovery_enrollment_preparation_events
    FOR EACH ROW EXECUTE FUNCTION routegate_recovery_enrollment_history_immutable();
CREATE TRIGGER rg147_enrollment_no_truncate
    BEFORE TRUNCATE ON vpn_runtime_recovery_enrollment_preparations
    FOR EACH STATEMENT EXECUTE FUNCTION routegate_recovery_enrollment_history_immutable();
CREATE TRIGGER rg147_enrollment_event_no_truncate
    BEFORE TRUNCATE ON vpn_runtime_recovery_enrollment_preparation_events
    FOR EACH STATEMENT EXECUTE FUNCTION routegate_recovery_enrollment_history_immutable();
