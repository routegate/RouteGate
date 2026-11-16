-- RG-140: dormant, append-only recovery holds. No endpoint or caller creates
-- these rows in production. Insertion is not approval and NEVER unlocks Agent.
CREATE TABLE vpn_runtime_recovery_reservations (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    server_id uuid NOT NULL REFERENCES servers(id),
    agent_id uuid NOT NULL REFERENCES agents(id),
    job_id uuid NOT NULL,
    job_kind text NOT NULL CHECK (job_kind IN ('config_apply','vpn_core_service')),
    agent_credential_generation bigint NOT NULL CHECK (agent_credential_generation > 0),
    mutation_intent_sha256 text NOT NULL CHECK (mutation_intent_sha256 ~ '^[0-9a-f]{64}$'),
    result_receipt_sha256 text NOT NULL CHECK (result_receipt_sha256 ~ '^[0-9a-f]{64}$'),
    runtime_witness_sha256 text NOT NULL CHECK (runtime_witness_sha256 ~ '^[0-9a-f]{64}$'),
    requested_by text NOT NULL CHECK (length(requested_by) BETWEEN 1 AND 200),
    created_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (server_id),
    UNIQUE (job_kind, job_id)
);

-- This is a one-way reservation. A future, separately reviewed, audited
-- authorization protocol must supply an explicit release transition.
CREATE FUNCTION routegate_runtime_recovery_immutable() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION USING ERRCODE='P0142', MESSAGE='recovery_reservation_immutable';
END; $$;
CREATE TRIGGER rg142_reservation_immutable
    BEFORE UPDATE OR DELETE ON vpn_runtime_recovery_reservations
    FOR EACH ROW EXECUTE FUNCTION routegate_runtime_recovery_immutable();

-- The insertion path shares the server row lock and global Manager-update
-- advisory lock with existing RG-140 interlocks. No unverified/active task,
-- concurrent transfer, revocation or platform update may be admitted.
CREATE FUNCTION routegate_runtime_recovery_reservation_guard() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE
    generation bigint;
    fenced boolean;
    completed_status text;
    completed_error text;
BEGIN
    PERFORM pg_advisory_xact_lock(140159);
    PERFORM id FROM servers WHERE id=NEW.server_id FOR UPDATE;
    IF NOT FOUND THEN
        RAISE EXCEPTION USING ERRCODE='P0142', MESSAGE='recovery_node_missing';
    END IF;
    -- A concurrent shared-routing write must not commit alongside this
    -- observation reservation. NOWAIT avoids a routing-table/server-row
    -- deadlock when another transaction owns locks in the opposite order.
    BEGIN
        LOCK TABLE routing_profiles, routing_profile_rules,
                   server_routing_profiles, managed_routing_rule_sets
            IN SHARE MODE NOWAIT;
    EXCEPTION WHEN lock_not_available THEN
        RAISE EXCEPTION USING ERRCODE='P0142', MESSAGE='recovery_routing_busy';
    END;
    IF EXISTS(SELECT 1 FROM vpn_account_transfer_nodes WHERE server_id=NEW.server_id)
       OR EXISTS(SELECT 1 FROM vpn_credential_revocations WHERE server_id=NEW.server_id AND state='prepared')
    THEN
        RAISE EXCEPTION USING ERRCODE='P0142', MESSAGE='recovery_node_reserved';
    END IF;

    SELECT a.credential_generation,
           a.capabilities @> '{"runtimeMutationFencingV1":true}'::jsonb
      INTO generation, fenced
      FROM agents a WHERE a.id=NEW.agent_id AND a.server_id=NEW.server_id;
    IF NOT FOUND OR NOT COALESCE(fenced,false)
       OR generation IS DISTINCT FROM NEW.agent_credential_generation
    THEN
        RAISE EXCEPTION USING ERRCODE='P0142', MESSAGE='recovery_agent_identity_unverified';
    END IF;

    IF NEW.job_kind='config_apply' THEN
        SELECT j.status, COALESCE(j.error_message,'')
          INTO completed_status, completed_error
          FROM config_apply_jobs j
         WHERE j.id=NEW.job_id AND j.server_id=NEW.server_id
           AND j.agent_id=NEW.agent_id AND j.action='apply';
    ELSE
        SELECT j.status, COALESCE(j.error_message,'')
          INTO completed_status, completed_error
          FROM agent_operation_jobs j
         WHERE j.id=NEW.job_id AND j.server_id=NEW.server_id
           AND j.agent_id=NEW.agent_id
           AND j.kind='vpn_core_service' AND j.operation='restart';
    END IF;
    IF NOT FOUND OR completed_status IS DISTINCT FROM 'succeeded'
       OR completed_error IS DISTINCT FROM ''
    THEN
        RAISE EXCEPTION USING ERRCODE='P0142', MESSAGE='recovery_job_not_confirmed';
    END IF;

    IF EXISTS(SELECT 1 FROM config_apply_jobs
              WHERE server_id=NEW.server_id AND status IN ('pending','in_progress'))
       OR EXISTS(SELECT 1 FROM agent_operation_jobs
                 WHERE server_id=NEW.server_id AND status IN ('pending','in_progress'))
       OR EXISTS(SELECT 1 FROM agent_platform_update_jobs
                 WHERE server_id=NEW.server_id AND status NOT IN ('succeeded','failed'))
       OR EXISTS(SELECT 1 FROM update_jobs
                 WHERE operation='apply' AND status IN ('pending','running'))
    THEN
        RAISE EXCEPTION USING ERRCODE='P0142', MESSAGE='recovery_conflicting_work';
    END IF;
    RETURN NEW;
END; $$;
CREATE TRIGGER rg142_reservation_insert
    BEFORE INSERT ON vpn_runtime_recovery_reservations
    FOR EACH ROW EXECUTE FUNCTION routegate_runtime_recovery_reservation_guard();

-- Reuse every existing node-level guard installed in migration 000161:
-- concurrent config jobs, account/credential edits, transfer membership,
-- platform updates, Agent replacement and config-version changes are fenced.
CREATE OR REPLACE FUNCTION routegate_revocation_node_guard(node uuid) RETURNS void LANGUAGE plpgsql AS $$
BEGIN
    IF node IS NULL THEN RETURN; END IF;
    PERFORM id FROM servers WHERE id=node FOR UPDATE;
    IF EXISTS(SELECT 1 FROM vpn_credential_revocations WHERE server_id=node AND state='prepared') THEN
        RAISE EXCEPTION USING ERRCODE='P0141', MESSAGE='node_reserved_for_revocation';
    END IF;
    IF EXISTS(SELECT 1 FROM vpn_runtime_recovery_reservations WHERE server_id=node) THEN
        RAISE EXCEPTION USING ERRCODE='P0142', MESSAGE='node_reserved_for_recovery';
    END IF;
END; $$;

-- An existing hold also excludes a new revocation preparation on that node.
CREATE FUNCTION routegate_runtime_recovery_revocation_guard() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    PERFORM id FROM servers WHERE id=NEW.server_id FOR UPDATE;
    IF EXISTS(SELECT 1 FROM vpn_runtime_recovery_reservations WHERE server_id=NEW.server_id) THEN
        RAISE EXCEPTION USING ERRCODE='P0142', MESSAGE='node_reserved_for_recovery';
    END IF;
    RETURN NEW;
END; $$;
CREATE TRIGGER rg142_revocation_insert
    BEFORE INSERT ON vpn_credential_revocations
    FOR EACH ROW EXECUTE FUNCTION routegate_runtime_recovery_revocation_guard();

-- Global routing or a Manager host update cannot invalidate a local witness.
CREATE OR REPLACE FUNCTION routegate_revocation_routing_guard() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF EXISTS(SELECT 1 FROM vpn_credential_revocations WHERE state='prepared') THEN
        RAISE EXCEPTION USING ERRCODE='P0141', MESSAGE='routing_reserved_for_revocation';
    END IF;
    IF EXISTS(SELECT 1 FROM vpn_runtime_recovery_reservations) THEN
        RAISE EXCEPTION USING ERRCODE='P0142', MESSAGE='routing_reserved_for_recovery';
    END IF;
    IF TG_OP='DELETE' THEN RETURN OLD; END IF; RETURN NEW;
END; $$;
CREATE OR REPLACE FUNCTION routegate_revocation_manager_update_guard() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF NEW.operation='apply' THEN
        PERFORM pg_advisory_xact_lock(140159);
        IF EXISTS(SELECT 1 FROM vpn_credential_revocations WHERE state='prepared') THEN
            RAISE EXCEPTION USING ERRCODE='P0141', MESSAGE='manager_update_reserved_for_revocation';
        END IF;
        IF EXISTS(SELECT 1 FROM vpn_runtime_recovery_reservations) THEN
            RAISE EXCEPTION USING ERRCODE='P0142', MESSAGE='manager_update_reserved_for_recovery';
        END IF;
    END IF; RETURN NEW;
END; $$;
