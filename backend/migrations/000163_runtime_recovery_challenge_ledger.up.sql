-- RG-140: immutable, non-public, one-use challenge commitment ledger.
-- This is a storage primitive, NOT a challenge issuing/consuming API.
-- The later application service MUST validate the secret binding first.
CREATE TABLE vpn_runtime_recovery_challenges (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    reservation_id uuid NOT NULL REFERENCES vpn_runtime_recovery_reservations(id) ON DELETE RESTRICT,
    operator_user_id uuid NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
    operator_session_id uuid NOT NULL REFERENCES auth_sessions(id) ON DELETE RESTRICT,
    context_sha256 text NOT NULL CHECK (context_sha256 ~ '^[0-9a-f]{64}$'),
    challenge_sha256 text NOT NULL CHECK (challenge_sha256 ~ '^[0-9a-f]{64}$'),
    issued_at timestamptz NOT NULL,
    expires_at timestamptz NOT NULL,
    state text NOT NULL DEFAULT 'issued' CHECK (state IN ('issued','consumed')),
    consumed_at timestamptz,
    CHECK (expires_at = issued_at + interval '90 seconds'),
    CHECK ((state = 'issued' AND consumed_at IS NULL) OR
           (state = 'consumed' AND consumed_at IS NOT NULL AND
            consumed_at >= issued_at AND consumed_at < expires_at)),
    -- This intentionally allows only ONE challenge ever per held reservation.
    -- Expiry does not silently unlock/reissue. Replacement needs a later
    -- independently audited cancellation/re-authorization design.
    UNIQUE (reservation_id),
    UNIQUE (challenge_sha256)
);

CREATE TABLE vpn_runtime_recovery_challenge_events (
    sequence bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    challenge_id uuid NOT NULL REFERENCES vpn_runtime_recovery_challenges(id) ON DELETE RESTRICT,
    action text NOT NULL CHECK (action IN ('issued','consumed')),
    operator_user_id uuid NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
    event_at timestamptz NOT NULL DEFAULT clock_timestamp()
);

-- The actor check is repeated INSIDE PostgreSQL for issue and consume.
-- Lock user, session, role and grant rows until transaction commit so a
-- parallel revoke/permission removal cannot win after authorization.
CREATE FUNCTION routegate_recovery_challenge_actor_guard(p_user uuid, p_session uuid)
RETURNS void LANGUAGE plpgsql AS $$
BEGIN
    PERFORM s.id
      FROM auth_sessions s
      JOIN users u ON u.id=s.user_id
      JOIN user_roles ur ON ur.user_id=u.id
      JOIN roles r ON r.id=ur.role_id
      JOIN role_permissions rp ON rp.role_id=r.id
      JOIN permissions p ON p.id=rp.permission_id
     WHERE s.id=p_session AND s.user_id=p_user
       AND s.revoked_at IS NULL AND s.expires_at > clock_timestamp()
       AND u.user_type='human' AND u.status='active'
       AND r.code='super_admin' AND p.code='runtime_recovery:authorize'
     FOR SHARE OF s,u,ur,r,rp,p;
    IF NOT FOUND THEN
        RAISE EXCEPTION USING ERRCODE='P0143', MESSAGE='recovery_human_authorization_required';
    END IF;
END; $$;

CREATE FUNCTION routegate_recovery_challenge_ledger_guard()
RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE
    observed_at timestamptz := clock_timestamp();
BEGIN
    IF TG_OP='DELETE' THEN
        RAISE EXCEPTION USING ERRCODE='P0143', MESSAGE='recovery_challenge_history_immutable';
    END IF;
    IF TG_OP='INSERT' THEN
        IF NEW.state <> 'issued' OR NEW.consumed_at IS NOT NULL OR
           NEW.issued_at < observed_at - interval '10 seconds' OR
           NEW.issued_at > observed_at + interval '1 second' THEN
            RAISE EXCEPTION USING ERRCODE='P0143', MESSAGE='recovery_challenge_issue_invalid';
        END IF;
    ELSE
        IF OLD.state <> 'issued' OR NEW.state <> 'consumed' OR
           NEW.consumed_at IS NULL OR
           NEW.consumed_at < observed_at - interval '10 seconds' OR
           NEW.consumed_at > observed_at + interval '1 second' OR
           NEW.consumed_at >= OLD.expires_at OR
           (to_jsonb(NEW)-ARRAY['state','consumed_at']) IS DISTINCT FROM
           (to_jsonb(OLD)-ARRAY['state','consumed_at']) THEN
            RAISE EXCEPTION USING ERRCODE='P0143', MESSAGE='recovery_challenge_consumption_invalid';
        END IF;
    END IF;
    -- A held node and same Agent generation are necessary but do NOT prove
    -- the actual runtime witness or authorize an Agent unlock.
    PERFORM r.id
      FROM vpn_runtime_recovery_reservations r
      JOIN agents a ON a.id=r.agent_id AND a.server_id=r.server_id
     WHERE r.id=NEW.reservation_id
       AND a.credential_generation=r.agent_credential_generation
       AND a.capabilities @> '{"runtimeMutationFencingV1":true}'::jsonb
     FOR KEY SHARE OF r,a;
    IF NOT FOUND THEN
        RAISE EXCEPTION USING ERRCODE='P0143', MESSAGE='recovery_challenge_reservation_changed';
    END IF;
    PERFORM routegate_recovery_challenge_actor_guard(NEW.operator_user_id,NEW.operator_session_id);
    RETURN NEW;
END; $$;
CREATE TRIGGER rg143_challenge_guard
    BEFORE INSERT OR UPDATE OR DELETE ON vpn_runtime_recovery_challenges
    FOR EACH ROW EXECUTE FUNCTION routegate_recovery_challenge_ledger_guard();

CREATE FUNCTION routegate_recovery_challenge_audit()
RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    INSERT INTO vpn_runtime_recovery_challenge_events
        (challenge_id,action,operator_user_id)
    VALUES (NEW.id,NEW.state,NEW.operator_user_id);
    RETURN NEW;
END; $$;
CREATE TRIGGER rg143_challenge_audit
    AFTER INSERT OR UPDATE ON vpn_runtime_recovery_challenges
    FOR EACH ROW EXECUTE FUNCTION routegate_recovery_challenge_audit();

CREATE FUNCTION routegate_recovery_challenge_history_immutable()
RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION USING ERRCODE='P0143', MESSAGE='recovery_challenge_history_immutable';
END; $$;
CREATE TRIGGER rg143_event_immutable
    BEFORE UPDATE OR DELETE ON vpn_runtime_recovery_challenge_events
    FOR EACH ROW EXECUTE FUNCTION routegate_recovery_challenge_history_immutable();
-- TRUNCATE bypasses row triggers, so prohibit it explicitly.
CREATE TRIGGER rg143_challenge_no_truncate
    BEFORE TRUNCATE ON vpn_runtime_recovery_challenges
    FOR EACH STATEMENT EXECUTE FUNCTION routegate_recovery_challenge_history_immutable();
CREATE TRIGGER rg143_event_no_truncate
    BEFORE TRUNCATE ON vpn_runtime_recovery_challenge_events
    FOR EACH STATEMENT EXECUTE FUNCTION routegate_recovery_challenge_history_immutable();
