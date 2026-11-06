CREATE TABLE routegate_dispatch.confirmed_removals (
    operation_id uuid PRIMARY KEY REFERENCES routegate_dispatch.removal_results(operation_id),
    proof jsonb NOT NULL CHECK (jsonb_typeof(proof) = 'object'),
    verifier_key bytea NOT NULL CHECK (octet_length(verifier_key) = 32),
    signature bytea NOT NULL CHECK (octet_length(signature) = 64),
    writer_xid xid8 NOT NULL DEFAULT pg_current_xact_id(),
    confirmed_at timestamptz NOT NULL
);
REVOKE ALL ON TABLE routegate_dispatch.confirmed_removals FROM PUBLIC;
CREATE TRIGGER rg179_confirmation_immutable BEFORE UPDATE OR DELETE OR TRUNCATE
ON routegate_dispatch.confirmed_removals FOR EACH STATEMENT
EXECUTE FUNCTION routegate_dispatch.reject_receipt_mutation();

ALTER TABLE public.vpn_credential_revocations DROP CONSTRAINT vpn_credential_revocations_state_check,
    ADD CONSTRAINT vpn_credential_revocations_state_check CHECK (state IN ('prepared', 'cancelled', 'confirmed'));
ALTER TABLE public.vpn_credential_revocation_events DROP CONSTRAINT vpn_credential_revocation_events_action_check,
    ADD CONSTRAINT vpn_credential_revocation_events_action_check CHECK (action IN ('prepared', 'cancelled', 'confirmed'));

-- Only the owner session that wrote the private receipt in this transaction
-- may enter one of the protected transitions. Ordinary runtime has no access
-- to the receipt or the terminal writer.
CREATE FUNCTION public.routegate_matches_terminal_removal(kind text, row_id uuid)
RETURNS boolean LANGUAGE sql SECURITY DEFINER SET search_path = pg_catalog, pg_temp AS $$
    SELECT EXISTS (
        SELECT 1 FROM routegate_dispatch.confirmed_removals c
        JOIN routegate_dispatch.held_removals h USING (operation_id)
        WHERE session_user = current_user AND c.writer_xid = pg_current_xact_id_if_assigned()
          AND row_id = CASE kind WHEN 'operation' THEN h.operation_id
                                 WHEN 'version' THEN h.version_id WHEN 'job' THEN h.job_id END
    );
$$;

-- Prevent a concurrent token insert or reactivation from escaping the terminal
-- update. The writer holds the account row through token revocation and commit.
CREATE FUNCTION public.routegate_guard_confirmed_removal_delivery() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE account_status text;
BEGIN
    IF NEW.status = 'active' THEN
        SELECT status INTO account_status FROM public.vpn_accounts WHERE id = NEW.vpn_account_id FOR SHARE;
        IF account_status = 'revoked' AND EXISTS (
            SELECT 1 FROM public.vpn_credential_revocations
            WHERE vpn_account_id = NEW.vpn_account_id AND state = 'confirmed'
        ) THEN
            RAISE EXCEPTION USING ERRCODE = 'P0141', MESSAGE = 'confirmed revocation blocks subscription issuance';
        END IF;
    END IF;
    RETURN NEW;
END;
$$;
CREATE TRIGGER rg179_subscription_delivery BEFORE INSERT OR UPDATE OF status, vpn_account_id
ON public.vpn_subscription_tokens FOR EACH ROW
EXECUTE FUNCTION public.routegate_guard_confirmed_removal_delivery();

CREATE OR REPLACE FUNCTION public.routegate_revocation_ledger_guard() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF TG_OP='INSERT' AND NEW.state='confirmed' THEN
  RAISE EXCEPTION USING ERRCODE='P0141',MESSAGE='revocation_invalid_transition'; END IF;
 IF TG_OP='DELETE' THEN RAISE EXCEPTION USING ERRCODE='P0141',MESSAGE='revocation_proof_retained'; END IF;
 IF TG_OP='UPDATE' AND ((to_jsonb(OLD)-ARRAY['state','last_action_by','cancelled_at']) IS DISTINCT FROM
 (to_jsonb(NEW)-ARRAY['state','last_action_by','cancelled_at']) OR OLD.state<>'prepared' OR (NEW.state<>'cancelled' AND NOT
 (NEW.state='confirmed' AND NEW.cancelled_at IS NULL AND public.routegate_matches_terminal_removal('operation', OLD.id)))) THEN
 RAISE EXCEPTION USING ERRCODE='P0141',MESSAGE='revocation_invalid_transition'; END IF;
 RETURN NEW;
END; $$;

CREATE OR REPLACE FUNCTION public.routegate_hold_dispatched_revocation() RETURNS trigger
 LANGUAGE plpgsql SECURITY DEFINER SET search_path = pg_catalog, pg_temp AS $$
BEGIN
 IF NEW.state IS DISTINCT FROM OLD.state
    AND NOT (OLD.state='prepared' AND NEW.state='confirmed'
             AND public.routegate_matches_terminal_removal('operation', OLD.id))
    AND EXISTS(SELECT 1 FROM routegate_dispatch.held_removals WHERE operation_id=OLD.id) THEN
  RAISE EXCEPTION USING ERRCODE='P0141',MESSAGE='held_revocation_requires_terminal_transition';
 END IF;
 RETURN NEW;
END; $$;

CREATE OR REPLACE FUNCTION public.routegate_held_removal_job_guard() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF TG_OP='UPDATE' AND OLD.status='held_for_revocation' AND NEW.status='revocation_confirmed'
    AND (to_jsonb(OLD)-ARRAY['status','completed_at','updated_at']) =
        (to_jsonb(NEW)-ARRAY['status','completed_at','updated_at'])
    AND NEW.completed_at IS NOT NULL AND public.routegate_matches_terminal_removal('job',OLD.id) THEN
  RETURN NEW;
 END IF;
 IF (TG_OP<>'INSERT' AND OLD.status='revocation_confirmed')
    OR (TG_OP<>'DELETE' AND NEW.status='revocation_confirmed') THEN
  RAISE EXCEPTION USING ERRCODE='P0141',MESSAGE='confirmed_revocation_job_protected';
 END IF;
 IF TG_OP='INSERT' AND NEW.status='held_for_revocation'
    AND public.routegate_matches_held_removal('job',NEW.server_id,NEW.id,to_jsonb(NEW)) THEN
  RETURN NEW;
 END IF;
 IF (TG_OP<>'INSERT' AND OLD.status='held_for_revocation')
    OR (TG_OP<>'DELETE' AND NEW.status='held_for_revocation') THEN
  RAISE EXCEPTION USING ERRCODE='P0141',MESSAGE='held_revocation_job_protected';
 END IF;
 IF TG_OP<>'INSERT' THEN PERFORM public.routegate_revocation_node_guard(OLD.server_id); END IF;
 IF TG_OP<>'DELETE' THEN PERFORM public.routegate_revocation_node_guard(NEW.server_id); END IF;
 IF TG_OP='DELETE' THEN RETURN OLD; END IF;
 RETURN NEW;
END; $$;


CREATE FUNCTION public.routegate_is_confirmed_removal_version(version_id uuid)
RETURNS boolean LANGUAGE sql SECURITY DEFINER SET search_path = pg_catalog, pg_temp AS $$
    SELECT EXISTS (
        SELECT 1 FROM routegate_dispatch.held_removals h
        JOIN routegate_dispatch.confirmed_removals c USING (operation_id)
        WHERE h.version_id = $1
    );
$$;

CREATE OR REPLACE FUNCTION public.routegate_revocation_version_guard() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF TG_OP='UPDATE' AND OLD.status='held_for_revocation' AND NEW.status='applied'
    AND (to_jsonb(OLD)-ARRAY['status','applied_at','client_settings','pinned']) =
        (to_jsonb(NEW)-ARRAY['status','applied_at','client_settings','pinned'])
    AND NEW.applied_at IS NOT NULL AND NEW.client_settings IS NOT NULL AND NEW.pinned
    AND public.routegate_matches_terminal_removal('version',OLD.id) THEN
  RETURN NEW;
 END IF;
 IF TG_OP<>'INSERT' AND public.routegate_is_confirmed_removal_version(OLD.id) THEN
  IF TG_OP='DELETE' OR NOT NEW.pinned OR
     (to_jsonb(OLD)-ARRAY['status','applied_at']) IS DISTINCT FROM (to_jsonb(NEW)-ARRAY['status','applied_at']) THEN
   RAISE EXCEPTION USING ERRCODE='P0141',MESSAGE='confirmed_revocation_version_protected';
  END IF;
 END IF;
 IF TG_OP='INSERT' AND NEW.status='held_for_revocation'
    AND public.routegate_matches_held_removal('version',NEW.server_id,NEW.id,to_jsonb(NEW)) THEN
  RETURN NEW;
 END IF;
 IF (TG_OP<>'INSERT' AND OLD.status='held_for_revocation')
    OR (TG_OP<>'DELETE' AND NEW.status='held_for_revocation') THEN
  RAISE EXCEPTION USING ERRCODE='P0141',MESSAGE='held_revocation_version_protected';
 END IF;
 IF TG_OP<>'INSERT' THEN
  IF EXISTS(SELECT 1 FROM vpn_credential_revocations WHERE baseline_version_id=OLD.id) THEN
   IF TG_OP='DELETE' THEN RAISE EXCEPTION USING ERRCODE='P0141',MESSAGE='revocation_proof_retained'; END IF;
   IF (to_jsonb(OLD)-ARRAY['pinned','status','applied_at']) IS DISTINCT FROM (to_jsonb(NEW)-ARRAY['pinned','status','applied_at']) THEN RAISE EXCEPTION USING ERRCODE='P0141',MESSAGE='revocation_proof_retained'; END IF;
  END IF;
  PERFORM routegate_revocation_node_guard(OLD.server_id);
 END IF;
 IF TG_OP<>'DELETE' THEN PERFORM routegate_revocation_node_guard(NEW.server_id);RETURN NEW; END IF; RETURN OLD;
END; $$;

DROP TRIGGER rg141_ledger ON public.vpn_credential_revocations;
CREATE TRIGGER rg141_ledger BEFORE INSERT OR UPDATE OR DELETE ON public.vpn_credential_revocations
FOR EACH ROW EXECUTE FUNCTION public.routegate_revocation_ledger_guard();

-- Callable only by the isolated owner coordinator after cryptographic
-- verification. Every state change is tied to this exact held operation.
CREATE FUNCTION routegate_dispatch.confirm_held_removal(op uuid, traffic_proof jsonb, verification_key bytea, proof_signature bytea)
RETURNS timestamptz LANGUAGE plpgsql SET search_path = pg_catalog, public, pg_temp AS $$
DECLARE
    held record;
    confirmed timestamptz := clock_timestamp();
BEGIN
    SELECT r.server_id, r.vpn_account_id, r.baseline_version_id, h.job_id, h.version_id,
           a.vless_uuid, baseline.client_settings
    INTO STRICT held
    FROM public.vpn_credential_revocations r
    JOIN routegate_dispatch.held_removals h ON h.operation_id=r.id
    JOIN routegate_dispatch.removal_results e USING (operation_id)
    JOIN public.vpn_accounts a ON a.id=r.vpn_account_id AND a.server_id=r.server_id AND a.status=r.account_status
    JOIN public.servers s ON s.id=r.server_id AND s.active_config_version_id=r.baseline_version_id
    JOIN public.config_versions baseline ON baseline.id=r.baseline_version_id
    WHERE r.id=op AND r.state='prepared'
      AND traffic_proof->>'operationId'=r.id::text AND traffic_proof->>'audience'=h.audience::text
      AND h.verifier_key<>verification_key
      AND (traffic_proof->>'observedAtUnixMicros')::bigint BETWEEN
          (extract(epoch FROM GREATEST(e.received_at,confirmed - interval '2 minutes'))*1000000)::bigint AND
          (extract(epoch FROM confirmed)*1000000)::bigint
      AND jsonb_typeof(baseline.client_settings->'accounts')='object'
      AND baseline.client_settings->'accounts' ? a.id::text;

    INSERT INTO routegate_dispatch.confirmed_removals(operation_id,proof,verifier_key,signature,confirmed_at)
    VALUES(op,traffic_proof,verification_key,proof_signature,confirmed);

    -- The server row remains locked until commit. Changing the reservation
    -- state inside the transaction admits these account-scoped writes only
    -- after a terminal receipt exists, while other sessions remain excluded.
    UPDATE public.vpn_credential_revocations SET state='confirmed',last_action_by='terminal-verifier' WHERE id=op;
    UPDATE public.vpn_accounts SET status='revoked',vless_uuid=gen_random_uuid(),updated_at=confirmed
    WHERE id=held.vpn_account_id;
    UPDATE public.vpn_subscription_tokens SET status='revoked',revoked_at=confirmed,updated_at=confirmed
    WHERE vpn_account_id=held.vpn_account_id AND status='active';
    UPDATE public.vpn_account_protocols SET active_enabled=false,activated_at=NULL
    WHERE vpn_account_id=held.vpn_account_id AND protocol='vless' AND active_enabled;

    UPDATE public.config_versions SET pinned=true WHERE id=held.baseline_version_id AND NOT pinned;
    UPDATE public.config_versions SET status='applied',applied_at=confirmed,pinned=true,
        client_settings=held.client_settings #- ARRAY['accounts',held.vpn_account_id::text]
    WHERE id=held.version_id;
    UPDATE public.config_apply_jobs SET status='revocation_confirmed',completed_at=confirmed,updated_at=confirmed
    WHERE id=held.job_id;
    UPDATE public.servers SET active_config_version_id=held.version_id,updated_at=confirmed WHERE id=held.server_id;

    INSERT INTO routegate_dispatch.retired_vless_credentials(digest,operation_id,retired_at)
    VALUES(sha256(uuid_send(held.vless_uuid)),op,confirmed);
    RETURN confirmed;
END;
$$;
REVOKE ALL ON FUNCTION routegate_dispatch.confirm_held_removal(uuid,jsonb,bytea,bytea) FROM PUBLIC;
