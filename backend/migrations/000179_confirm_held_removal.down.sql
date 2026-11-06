DO $$
BEGIN
    LOCK TABLE routegate_dispatch.confirmed_removals IN ACCESS EXCLUSIVE MODE;
    IF EXISTS (SELECT 1 FROM routegate_dispatch.confirmed_removals) THEN
        RAISE EXCEPTION USING ERRCODE = 'P0141', MESSAGE = 'cannot remove confirmed VLESS revocation history';
    END IF;
END;
$$;
DROP FUNCTION routegate_dispatch.confirm_held_removal(uuid,jsonb,bytea,bytea);
DROP TRIGGER rg179_subscription_delivery ON public.vpn_subscription_tokens;
DROP FUNCTION public.routegate_guard_confirmed_removal_delivery();
DROP TRIGGER rg141_ledger ON public.vpn_credential_revocations;
CREATE OR REPLACE FUNCTION public.routegate_revocation_ledger_guard() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF TG_OP='DELETE' THEN RAISE EXCEPTION USING ERRCODE='P0141',MESSAGE='revocation_proof_retained'; END IF;
 IF TG_OP='UPDATE' AND ((to_jsonb(OLD)-ARRAY['state','last_action_by','cancelled_at']) IS DISTINCT FROM
 (to_jsonb(NEW)-ARRAY['state','last_action_by','cancelled_at']) OR OLD.state<>'prepared' OR NEW.state<>'cancelled') THEN
 RAISE EXCEPTION USING ERRCODE='P0141',MESSAGE='revocation_invalid_transition'; END IF;
 RETURN NEW;
END; $$;

CREATE OR REPLACE FUNCTION public.routegate_hold_dispatched_revocation() RETURNS trigger
 LANGUAGE plpgsql SECURITY DEFINER SET search_path = pg_catalog, pg_temp AS $$
BEGIN
 IF NEW.state IS DISTINCT FROM OLD.state
    AND EXISTS(SELECT 1 FROM routegate_dispatch.held_removals WHERE operation_id=OLD.id) THEN
  RAISE EXCEPTION USING ERRCODE='P0141',MESSAGE='held_revocation_requires_terminal_transition';
 END IF;
 RETURN NEW;
END; $$;

CREATE OR REPLACE FUNCTION public.routegate_held_removal_job_guard() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
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

CREATE OR REPLACE FUNCTION public.routegate_revocation_version_guard() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
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

CREATE TRIGGER rg141_ledger BEFORE UPDATE OR DELETE ON public.vpn_credential_revocations
FOR EACH ROW EXECUTE FUNCTION public.routegate_revocation_ledger_guard();
ALTER TABLE public.vpn_credential_revocations DROP CONSTRAINT vpn_credential_revocations_state_check,
    ADD CONSTRAINT vpn_credential_revocations_state_check CHECK (state IN ('prepared', 'cancelled'));
ALTER TABLE public.vpn_credential_revocation_events DROP CONSTRAINT vpn_credential_revocation_events_action_check,
    ADD CONSTRAINT vpn_credential_revocation_events_action_check CHECK (action IN ('prepared', 'cancelled'));
DROP FUNCTION public.routegate_is_confirmed_removal_version(uuid);
DROP FUNCTION public.routegate_matches_terminal_removal(text,uuid);
DROP TABLE routegate_dispatch.confirmed_removals;
