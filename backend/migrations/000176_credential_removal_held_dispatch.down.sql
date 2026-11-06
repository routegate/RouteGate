DO $$
BEGIN
 IF EXISTS (SELECT 1 FROM routegate_dispatch.held_removals) THEN
  RAISE EXCEPTION USING ERRCODE='P0141',MESSAGE='held_revocation_receipt_retained';
 END IF;
END; $$;

DROP TRIGGER rg176_retain_reservation ON public.vpn_credential_revocations;
DROP FUNCTION public.routegate_hold_dispatched_revocation();
DROP TRIGGER rg141_config_job ON public.config_apply_jobs;
CREATE TRIGGER rg141_config_job BEFORE INSERT OR UPDATE OR DELETE ON public.config_apply_jobs
 FOR EACH ROW EXECUTE FUNCTION public.routegate_revocation_job_guard();
DROP FUNCTION public.routegate_held_removal_job_guard();
CREATE OR REPLACE FUNCTION public.routegate_revocation_server_guard() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF TG_OP='DELETE' THEN PERFORM routegate_revocation_node_guard(OLD.id); RETURN OLD; END IF;
 IF (to_jsonb(OLD)-ARRAY['status','last_seen_at','updated_at']) IS DISTINCT FROM (to_jsonb(NEW)-ARRAY['status','last_seen_at','updated_at']) THEN PERFORM routegate_revocation_node_guard(OLD.id); END IF;
 RETURN NEW;
END; $$;
CREATE OR REPLACE FUNCTION public.routegate_revocation_version_guard() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF TG_OP<>'INSERT' THEN
  IF EXISTS(SELECT 1 FROM vpn_credential_revocations WHERE baseline_version_id=OLD.id) THEN
   IF TG_OP='DELETE' THEN RAISE EXCEPTION USING ERRCODE='P0141',MESSAGE='revocation_proof_retained'; END IF;
   IF (to_jsonb(OLD)-ARRAY['pinned','status','applied_at']) IS DISTINCT FROM (to_jsonb(NEW)-ARRAY['pinned','status','applied_at']) THEN RAISE EXCEPTION USING ERRCODE='P0141',MESSAGE='revocation_proof_retained'; END IF;
  END IF;
  PERFORM routegate_revocation_node_guard(OLD.server_id);
 END IF;
 IF TG_OP<>'DELETE' THEN PERFORM routegate_revocation_node_guard(NEW.server_id);RETURN NEW; END IF; RETURN OLD;
END; $$;
DROP FUNCTION public.routegate_matches_held_removal(text,uuid,uuid,jsonb);
DROP TABLE routegate_dispatch.held_removals;
DROP FUNCTION routegate_dispatch.reject_receipt_mutation();
DROP SCHEMA routegate_dispatch;
