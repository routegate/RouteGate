-- RG-140: durable, one-attempt/one-use observation nonce registry.
-- Dormant by design: no application issuer, HTTP route or Agent unlock.
-- The three expected hashes MUST originate from separately verified
-- historical Agent evidence. The 000164 intent/receipt/witness claims do
-- NOT automatically prove the same byte representation.
CREATE TABLE vpn_runtime_recovery_observation_nonces (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    attempt_id uuid NOT NULL UNIQUE REFERENCES vpn_runtime_recovery_attempts(id) ON DELETE RESTRICT,
    reservation_id uuid NOT NULL REFERENCES vpn_runtime_recovery_reservations(id) ON DELETE RESTRICT,
    server_id uuid NOT NULL REFERENCES servers(id) ON DELETE RESTRICT,
    agent_id uuid NOT NULL REFERENCES agents(id) ON DELETE RESTRICT,
    task_id uuid NOT NULL,
    attempt_epoch bigint NOT NULL CHECK (attempt_epoch > 0),
    agent_credential_generation bigint NOT NULL CHECK (agent_credential_generation > 0),
    expected_marker_sha256 text NOT NULL CHECK (expected_marker_sha256 ~ '^[0-9a-f]{64}$'),
    expected_receipt_sha256 text NOT NULL CHECK (expected_receipt_sha256 ~ '^[0-9a-f]{64}$'),
    expected_runtime_witness_sha256 text NOT NULL CHECK (expected_runtime_witness_sha256 ~ '^[0-9a-f]{64}$'),
    nonce_sha256 text NOT NULL UNIQUE CHECK (nonce_sha256 ~ '^[0-9a-f]{64}$'),
    issued_at timestamptz NOT NULL,
    expires_at timestamptz NOT NULL,
    state text NOT NULL DEFAULT 'issued'
        CHECK (state IN ('issued','reply_matched_unattested','reply_rejected')),
    resolved_at timestamptz,
    response_sha256 text CHECK (response_sha256 ~ '^[0-9a-f]{64}$'),
    CHECK (expires_at > issued_at AND expires_at <= issued_at + interval '60 seconds'),
    CHECK ((state = 'issued' AND resolved_at IS NULL AND response_sha256 IS NULL) OR
           (state <> 'issued' AND resolved_at IS NOT NULL
            AND response_sha256 IS NOT NULL
            AND resolved_at >= issued_at AND resolved_at < expires_at))
);

CREATE TABLE vpn_runtime_recovery_observation_nonce_events (
    sequence bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    observation_id uuid NOT NULL REFERENCES vpn_runtime_recovery_observation_nonces(id) ON DELETE RESTRICT,
    action text NOT NULL CHECK (action IN ('issued','reply_matched_unattested','reply_rejected')),
    event_at timestamptz NOT NULL DEFAULT clock_timestamp()
);

CREATE FUNCTION routegate_recovery_observation_nonce_guard() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE
    observed_at timestamptz := clock_timestamp();
BEGIN
    IF TG_OP='DELETE' THEN
        RAISE EXCEPTION USING ERRCODE='P0145', MESSAGE='recovery_observation_nonce_immutable';
    END IF;
    IF TG_OP='UPDATE' THEN
        IF OLD.state <> 'issued'
           OR NEW.state NOT IN ('reply_matched_unattested','reply_rejected')
           OR NEW.resolved_at IS NULL
           OR NEW.resolved_at < observed_at - interval '10 seconds'
           OR NEW.resolved_at > observed_at + interval '1 second'
           OR observed_at >= OLD.expires_at
           OR (to_jsonb(NEW)-ARRAY['state','resolved_at','response_sha256'])
              IS DISTINCT FROM
              (to_jsonb(OLD)-ARRAY['state','resolved_at','response_sha256'])
        THEN
            RAISE EXCEPTION USING ERRCODE='P0145', MESSAGE='recovery_observation_nonce_replay';
        END IF;
        -- Re-check the Agent generation AT the resolution boundary, not
        -- only when the challenge was originally recorded.
        PERFORM 1 FROM agents a
         WHERE a.id=NEW.agent_id AND a.server_id=NEW.server_id
           AND a.credential_generation=NEW.agent_credential_generation
           AND a.capabilities @> '{"runtimeMutationFencingV1":true}'::jsonb
         FOR SHARE;
        IF NOT FOUND THEN
            RAISE EXCEPTION USING ERRCODE='P0145', MESSAGE='recovery_observation_agent_rotated';
        END IF;
        RETURN NEW;
    END IF;
    IF NEW.state <> 'issued' OR NEW.resolved_at IS NOT NULL
       OR NEW.response_sha256 IS NOT NULL
       OR NEW.issued_at < observed_at - interval '10 seconds'
       OR NEW.issued_at > observed_at + interval '1 second' THEN
        RAISE EXCEPTION USING ERRCODE='P0145', MESSAGE='recovery_observation_nonce_invalid';
    END IF;
    PERFORM 1
      FROM vpn_runtime_recovery_attempts p
      JOIN vpn_runtime_recovery_reservations r ON r.id=p.reservation_id
      JOIN agents a ON a.id=p.agent_id AND a.server_id=p.server_id
     WHERE p.id=NEW.attempt_id AND p.state='awaiting_fresh_observation'
       AND p.reservation_id=NEW.reservation_id
       AND p.server_id=NEW.server_id AND p.agent_id=NEW.agent_id
       AND p.job_id=NEW.task_id
       AND p.attempt_epoch=NEW.attempt_epoch
       AND p.agent_credential_generation=NEW.agent_credential_generation
       AND r.id=NEW.reservation_id
       AND a.credential_generation=NEW.agent_credential_generation
       AND a.capabilities @> '{"runtimeMutationFencingV1":true}'::jsonb
     FOR SHARE OF a;
    IF NOT FOUND THEN
        RAISE EXCEPTION USING ERRCODE='P0145', MESSAGE='recovery_observation_attempt_changed';
    END IF;
    RETURN NEW;
END; $$;
CREATE TRIGGER rg145_observation_nonce_guard
    BEFORE INSERT OR UPDATE OR DELETE ON vpn_runtime_recovery_observation_nonces
    FOR EACH ROW EXECUTE FUNCTION routegate_recovery_observation_nonce_guard();

CREATE FUNCTION routegate_recovery_observation_nonce_audit() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    INSERT INTO vpn_runtime_recovery_observation_nonce_events(observation_id,action)
    VALUES (NEW.id,NEW.state);
    RETURN NEW;
END; $$;
CREATE TRIGGER rg145_observation_nonce_audit
    AFTER INSERT OR UPDATE ON vpn_runtime_recovery_observation_nonces
    FOR EACH ROW EXECUTE FUNCTION routegate_recovery_observation_nonce_audit();

CREATE FUNCTION routegate_recovery_observation_nonce_immutable() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION USING ERRCODE='P0145', MESSAGE='recovery_observation_nonce_immutable';
END; $$;
CREATE TRIGGER rg145_nonce_events_immutable
    BEFORE UPDATE OR DELETE ON vpn_runtime_recovery_observation_nonce_events
    FOR EACH ROW EXECUTE FUNCTION routegate_recovery_observation_nonce_immutable();
CREATE TRIGGER rg145_nonce_no_truncate
    BEFORE TRUNCATE ON vpn_runtime_recovery_observation_nonces
    FOR EACH STATEMENT EXECUTE FUNCTION routegate_recovery_observation_nonce_immutable();
CREATE TRIGGER rg145_nonce_events_no_truncate
    BEFORE TRUNCATE ON vpn_runtime_recovery_observation_nonce_events
    FOR EACH STATEMENT EXECUTE FUNCTION routegate_recovery_observation_nonce_immutable();
