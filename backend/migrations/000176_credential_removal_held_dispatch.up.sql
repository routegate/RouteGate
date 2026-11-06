-- Only the isolated trusted database owner may consume signed decisions.
-- Ordinary Manager runtime provisioning never grants this schema.
CREATE SCHEMA routegate_dispatch;
REVOKE ALL ON SCHEMA routegate_dispatch FROM PUBLIC;

CREATE TABLE routegate_dispatch.held_removals (
 operation_id uuid PRIMARY KEY REFERENCES public.vpn_credential_removal_dispatch_stages(operation_id),
 server_id uuid NOT NULL REFERENCES public.servers(id),
 version_id uuid NOT NULL UNIQUE REFERENCES public.config_versions(id) DEFERRABLE INITIALLY DEFERRED,
 job_id uuid NOT NULL UNIQUE REFERENCES public.config_apply_jobs(id) DEFERRABLE INITIALLY DEFERRED,
 version_number integer NOT NULL CHECK (version_number > 0),
 signature_digest bytea NOT NULL CHECK (octet_length(signature_digest)=32),
 audience uuid NOT NULL,
 verifier_key bytea NOT NULL CHECK (octet_length(verifier_key)=32),
 writer_xid xid8 NOT NULL DEFAULT pg_current_xact_id(),
 issued_at timestamptz NOT NULL DEFAULT clock_timestamp()
);
REVOKE ALL ON TABLE routegate_dispatch.held_removals FROM PUBLIC;

CREATE FUNCTION routegate_dispatch.reject_receipt_mutation() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 RAISE EXCEPTION USING ERRCODE='P0141',MESSAGE='held_revocation_receipt_retained';
END; $$;
CREATE TRIGGER rg176_receipt_immutable BEFORE UPDATE OR DELETE OR TRUNCATE
 ON routegate_dispatch.held_removals FOR EACH STATEMENT
 EXECUTE FUNCTION routegate_dispatch.reject_receipt_mutation();

-- No credentials or authority are returned. The private receipt has to have
-- been written in this exact transaction by the protected coordinator.
CREATE FUNCTION public.routegate_matches_held_removal(kind text, node uuid, row_id uuid, row_data jsonb)
RETURNS boolean LANGUAGE sql SECURITY DEFINER SET search_path = pg_catalog, pg_temp AS $$
 SELECT EXISTS(
  SELECT 1 FROM routegate_dispatch.held_removals h
  JOIN public.vpn_credential_revocations r ON r.id=h.operation_id
  JOIN public.vpn_credential_removal_dispatch_stages s ON s.operation_id=h.operation_id
  JOIN public.vpn_credential_removal_dispatch_authorizations a ON a.operation_id=h.operation_id
  WHERE h.writer_xid=pg_current_xact_id_if_assigned()
    AND h.server_id=node AND r.server_id=node AND r.state='prepared'
    AND h.version_id=s.candidate_version_id AND h.job_id=s.apply_job_id
    AND a.expires_at>clock_timestamp()
    AND CASE kind
      WHEN 'version' THEN row_id=h.version_id AND row_data->'rendered_config'=r.candidate_config
        AND row_data @> jsonb_build_object(
        'version',h.version_number,'config_hash',r.candidate_hash,
        'status','held_for_revocation',
        'applied_at',NULL,'client_settings',NULL,'pinned',false)
      WHEN 'job' THEN row_id=h.job_id AND row_data @> jsonb_build_object(
        'config_version_id',h.version_id,'agent_id',s.agent_id,
        'status','held_for_revocation','action','apply',
        'started_at',NULL,'completed_at',NULL,'error_message',NULL)
        AND row_data->'request_payload'='{}'::jsonb
        AND row_data->'result_payload'='{}'::jsonb
      WHEN 'counter' THEN row_id=node AND row_data @> jsonb_build_object(
        'next_config_version',h.version_number+1)
      ELSE false
    END
 )
$$;

CREATE FUNCTION public.routegate_held_removal_job_guard() RETURNS trigger LANGUAGE plpgsql AS $$
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
DROP TRIGGER rg141_config_job ON public.config_apply_jobs;
CREATE TRIGGER rg141_config_job BEFORE INSERT OR UPDATE OR DELETE ON public.config_apply_jobs
 FOR EACH ROW EXECUTE FUNCTION public.routegate_held_removal_job_guard();

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

CREATE OR REPLACE FUNCTION public.routegate_revocation_server_guard() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF TG_OP='DELETE' THEN PERFORM routegate_revocation_node_guard(OLD.id); RETURN OLD; END IF;
 IF NEW.next_config_version::bigint=OLD.next_config_version::bigint+1
    AND (to_jsonb(OLD)-'next_config_version')=(to_jsonb(NEW)-'next_config_version')
    AND public.routegate_matches_held_removal('counter',NEW.id,NEW.id,to_jsonb(NEW)) THEN
  RETURN NEW;
 END IF;
 IF (to_jsonb(OLD)-ARRAY['status','last_seen_at','updated_at']) IS DISTINCT FROM (to_jsonb(NEW)-ARRAY['status','last_seen_at','updated_at']) THEN PERFORM routegate_revocation_node_guard(OLD.id); END IF;
 RETURN NEW;
END; $$;

-- Cancellation is a pre-dispatch operation. After issuance the reservation
-- is retained until a future scoped terminal transition can prove the outcome.
CREATE FUNCTION public.routegate_hold_dispatched_revocation() RETURNS trigger
 LANGUAGE plpgsql SECURITY DEFINER SET search_path = pg_catalog, pg_temp AS $$
BEGIN
 IF NEW.state IS DISTINCT FROM OLD.state
    AND EXISTS(SELECT 1 FROM routegate_dispatch.held_removals WHERE operation_id=OLD.id) THEN
  RAISE EXCEPTION USING ERRCODE='P0141',MESSAGE='held_revocation_requires_terminal_transition';
 END IF;
 RETURN NEW;
END; $$;
REVOKE ALL ON FUNCTION public.routegate_hold_dispatched_revocation() FROM PUBLIC;
CREATE TRIGGER rg176_retain_reservation BEFORE UPDATE OF state ON public.vpn_credential_revocations
 FOR EACH ROW EXECUTE FUNCTION public.routegate_hold_dispatched_revocation();
