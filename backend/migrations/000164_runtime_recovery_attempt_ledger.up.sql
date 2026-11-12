-- RG-140: stage a read-only, irrevocable recovery attempt after a verified
-- challenge becomes consumed. No Agent API, runtime mutation or unlock exists.
--
-- An already-consumed challenge from an earlier schema must not be silently
-- forgotten. Historical rows are backfilled as pending observations only.
CREATE TABLE vpn_runtime_recovery_attempts (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    attempt_epoch bigint GENERATED ALWAYS AS IDENTITY UNIQUE,
    reservation_id uuid NOT NULL REFERENCES vpn_runtime_recovery_reservations(id) ON DELETE RESTRICT,
    challenge_id uuid NOT NULL REFERENCES vpn_runtime_recovery_challenges(id) ON DELETE RESTRICT,
    server_id uuid NOT NULL REFERENCES servers(id) ON DELETE RESTRICT,
    agent_id uuid NOT NULL REFERENCES agents(id) ON DELETE RESTRICT,
    job_id uuid NOT NULL,
    job_kind text NOT NULL CHECK (job_kind IN ('config_apply','vpn_core_service')),
    agent_credential_generation bigint NOT NULL CHECK (agent_credential_generation > 0),
    operator_user_id uuid NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
    operator_session_id uuid NOT NULL REFERENCES auth_sessions(id) ON DELETE RESTRICT,
    mutation_intent_sha256 text NOT NULL CHECK (mutation_intent_sha256 ~ '^[0-9a-f]{64}$'),
    result_receipt_sha256 text NOT NULL CHECK (result_receipt_sha256 ~ '^[0-9a-f]{64}$'),
    runtime_witness_sha256 text NOT NULL CHECK (runtime_witness_sha256 ~ '^[0-9a-f]{64}$'),
    challenge_consumed_at timestamptz NOT NULL,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    state text NOT NULL DEFAULT 'awaiting_fresh_observation'
        CHECK (state = 'awaiting_fresh_observation'),
    CHECK (created_at >= challenge_consumed_at),
    UNIQUE (reservation_id),
    UNIQUE (challenge_id),
    UNIQUE (server_id)
);

-- This trigger does not authenticate a presented challenge: only the private
-- Consumer does that. It independently requires the already consumed
-- challenge's *exact* immutable identity and reservation evidence.
CREATE FUNCTION routegate_recovery_attempt_guard() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF TG_OP <> 'INSERT' THEN
        RAISE EXCEPTION USING ERRCODE='P0144', MESSAGE='recovery_attempt_immutable';
    END IF;
    IF NEW.state IS DISTINCT FROM 'awaiting_fresh_observation' OR
       NEW.created_at < NEW.challenge_consumed_at THEN
        RAISE EXCEPTION USING ERRCODE='P0144', MESSAGE='recovery_attempt_invalid';
    END IF;
    PERFORM 1
      FROM vpn_runtime_recovery_challenges c
      JOIN vpn_runtime_recovery_reservations r ON r.id=c.reservation_id
     WHERE c.id=NEW.challenge_id AND c.state='consumed'
       AND c.consumed_at=NEW.challenge_consumed_at
       AND r.id=NEW.reservation_id
       AND r.server_id=NEW.server_id AND r.agent_id=NEW.agent_id
       AND r.job_id=NEW.job_id AND r.job_kind=NEW.job_kind
       AND r.agent_credential_generation=NEW.agent_credential_generation
       AND c.operator_user_id=NEW.operator_user_id
       AND c.operator_session_id=NEW.operator_session_id
       AND r.mutation_intent_sha256=NEW.mutation_intent_sha256
       AND r.result_receipt_sha256=NEW.result_receipt_sha256
       AND r.runtime_witness_sha256=NEW.runtime_witness_sha256
     FOR KEY SHARE OF c,r;
    IF NOT FOUND THEN
        RAISE EXCEPTION USING ERRCODE='P0144', MESSAGE='recovery_attempt_evidence_mismatch';
    END IF;
    RETURN NEW;
END; $$;

CREATE TRIGGER rg144_attempt_insert_guard
    BEFORE INSERT ON vpn_runtime_recovery_attempts
    FOR EACH ROW EXECUTE FUNCTION routegate_recovery_attempt_guard();

CREATE FUNCTION routegate_recovery_attempt_immutable() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION USING ERRCODE='P0144', MESSAGE='recovery_attempt_immutable';
END; $$;
CREATE TRIGGER rg144_attempt_no_change
    BEFORE UPDATE OR DELETE ON vpn_runtime_recovery_attempts
    FOR EACH ROW EXECUTE FUNCTION routegate_recovery_attempt_immutable();
CREATE TRIGGER rg144_attempt_no_truncate
    BEFORE TRUNCATE ON vpn_runtime_recovery_attempts
    FOR EACH STATEMENT EXECUTE FUNCTION routegate_recovery_attempt_immutable();

-- Attempt creation is atomic with the original challenge transition and its
-- 000163 audit event. A failure to write the attempt aborts consumption.
CREATE FUNCTION routegate_recovery_attempt_on_consumed_challenge()
RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    INSERT INTO vpn_runtime_recovery_attempts (
        reservation_id,challenge_id,server_id,agent_id,job_id,job_kind,
        agent_credential_generation,operator_user_id,operator_session_id,
        mutation_intent_sha256,result_receipt_sha256,runtime_witness_sha256,
        challenge_consumed_at
    )
    SELECT r.id,NEW.id,r.server_id,r.agent_id,r.job_id,r.job_kind,
           r.agent_credential_generation,NEW.operator_user_id,NEW.operator_session_id,
           r.mutation_intent_sha256,r.result_receipt_sha256,r.runtime_witness_sha256,
           NEW.consumed_at
      FROM vpn_runtime_recovery_reservations r
     WHERE r.id=NEW.reservation_id;
    IF NOT FOUND THEN
        RAISE EXCEPTION USING ERRCODE='P0144', MESSAGE='recovery_attempt_reservation_missing';
    END IF;
    RETURN NEW;
END; $$;
CREATE TRIGGER rg144_challenge_consumed_attempt
    AFTER UPDATE OF state ON vpn_runtime_recovery_challenges
    FOR EACH ROW WHEN (OLD.state='issued' AND NEW.state='consumed')
    EXECUTE FUNCTION routegate_recovery_attempt_on_consumed_challenge();

-- Upgrade compatibility. Consumed historical challenges are recorded as
-- observation-pending, not as runtime-cleared or operator-approved attempts.
INSERT INTO vpn_runtime_recovery_attempts (
    reservation_id,challenge_id,server_id,agent_id,job_id,job_kind,
    agent_credential_generation,operator_user_id,operator_session_id,
    mutation_intent_sha256,result_receipt_sha256,runtime_witness_sha256,
    challenge_consumed_at
)
SELECT r.id,c.id,r.server_id,r.agent_id,r.job_id,r.job_kind,
       r.agent_credential_generation,c.operator_user_id,c.operator_session_id,
       r.mutation_intent_sha256,r.result_receipt_sha256,r.runtime_witness_sha256,
       c.consumed_at
  FROM vpn_runtime_recovery_challenges c
  JOIN vpn_runtime_recovery_reservations r ON r.id=c.reservation_id
 WHERE c.state='consumed'
 ORDER BY c.consumed_at,c.id;
