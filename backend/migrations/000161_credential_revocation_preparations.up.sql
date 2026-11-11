-- Internal preparation ledger only. Forward-only port from legacy RG-140 000160; current main 000160 remains unchanged. No HTTP route, task producer or dispatcher
-- is enabled. Preparation is NOT pending execution or confirmed revocation.
CREATE TABLE vpn_credential_revocations (
 id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
 vpn_account_id uuid NOT NULL REFERENCES vpn_accounts(id),
 server_id uuid NOT NULL REFERENCES servers(id),
 agent_id uuid NOT NULL REFERENCES agents(id),
 agent_credential_generation bigint NOT NULL,
 baseline_version_id uuid NOT NULL REFERENCES config_versions(id),
 baseline_hash text NOT NULL CHECK (baseline_hash ~ '^[0-9a-f]{64}$'),
 baseline_runtime_hash text NOT NULL CHECK (baseline_runtime_hash ~ '^[0-9a-f]{64}$'),
 candidate_runtime_hash text NOT NULL CHECK (candidate_runtime_hash ~ '^[0-9a-f]{64}$'),
 candidate_hash text NOT NULL CHECK (candidate_hash ~ '^[0-9a-f]{64}$'),
 candidate_config jsonb NOT NULL CHECK (jsonb_typeof(candidate_config)='object'),
 account_status text NOT NULL CHECK(account_status IN ('active','suspended','revoked')),
 state text NOT NULL DEFAULT 'prepared' CHECK(state IN ('prepared','cancelled')),
 requested_by text NOT NULL CHECK(length(requested_by) BETWEEN 1 AND 200),
 last_action_by text NOT NULL CHECK(length(last_action_by) BETWEEN 1 AND 200),
 created_at timestamptz NOT NULL DEFAULT now(),
 cancelled_at timestamptz,
 CHECK((state='cancelled')=(cancelled_at IS NOT NULL)),
 CHECK(baseline_runtime_hash<>candidate_runtime_hash)
);
CREATE UNIQUE INDEX vpn_credential_revocations_one_node ON vpn_credential_revocations(server_id) WHERE state='prepared';
CREATE TABLE vpn_credential_revocation_events (
 sequence bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
 operation_id uuid NOT NULL REFERENCES vpn_credential_revocations(id),
 action text NOT NULL CHECK(action IN ('prepared','cancelled')),
 actor text NOT NULL,
 created_at timestamptz NOT NULL DEFAULT now()
);
CREATE FUNCTION routegate_revocation_ledger_guard() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF TG_OP='DELETE' THEN RAISE EXCEPTION USING ERRCODE='P0141',MESSAGE='revocation_proof_retained'; END IF;
 IF TG_OP='UPDATE' AND ((to_jsonb(OLD)-ARRAY['state','last_action_by','cancelled_at']) IS DISTINCT FROM
 (to_jsonb(NEW)-ARRAY['state','last_action_by','cancelled_at']) OR OLD.state<>'prepared' OR NEW.state<>'cancelled') THEN
 RAISE EXCEPTION USING ERRCODE='P0141',MESSAGE='revocation_invalid_transition'; END IF;
 RETURN NEW;
END; $$;
CREATE TRIGGER rg141_ledger BEFORE UPDATE OR DELETE ON vpn_credential_revocations FOR EACH ROW EXECUTE FUNCTION routegate_revocation_ledger_guard();
CREATE FUNCTION routegate_revocation_audit() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 INSERT INTO vpn_credential_revocation_events(operation_id,action,actor) VALUES(NEW.id,NEW.state,NEW.last_action_by);
 RETURN NEW;
END; $$;
CREATE TRIGGER rg141_audit AFTER INSERT OR UPDATE ON vpn_credential_revocations FOR EACH ROW EXECUTE FUNCTION routegate_revocation_audit();
CREATE FUNCTION routegate_revocation_audit_guard() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN RAISE EXCEPTION USING ERRCODE='P0141',MESSAGE='revocation_audit_retained'; END; $$;
CREATE TRIGGER rg141_audit_immutable BEFORE UPDATE OR DELETE ON vpn_credential_revocation_events FOR EACH ROW EXECUTE FUNCTION routegate_revocation_audit_guard();

CREATE FUNCTION routegate_revocation_node_guard(node uuid) RETURNS void LANGUAGE plpgsql AS $$
BEGIN
 IF node IS NULL THEN RETURN; END IF;
 PERFORM id FROM servers WHERE id=node FOR UPDATE;
 IF EXISTS(SELECT 1 FROM vpn_credential_revocations WHERE server_id=node AND state='prepared') THEN
 RAISE EXCEPTION USING ERRCODE='P0141',MESSAGE='node_reserved_for_revocation'; END IF;
END; $$;
CREATE FUNCTION routegate_revocation_job_guard() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF TG_OP<>'INSERT' THEN PERFORM routegate_revocation_node_guard(OLD.server_id); END IF;
 IF TG_OP<>'DELETE' THEN PERFORM routegate_revocation_node_guard(NEW.server_id); END IF;
 IF TG_OP='DELETE' THEN RETURN OLD; END IF; RETURN NEW;
END; $$;
CREATE TRIGGER rg141_config_job BEFORE INSERT OR UPDATE OR DELETE ON config_apply_jobs FOR EACH ROW EXECUTE FUNCTION routegate_revocation_job_guard();
CREATE TRIGGER rg141_operation_job BEFORE INSERT OR UPDATE OR DELETE ON agent_operation_jobs FOR EACH ROW EXECUTE FUNCTION routegate_revocation_job_guard();
CREATE TRIGGER rg141_update_job BEFORE INSERT OR UPDATE OR DELETE ON agent_platform_update_jobs FOR EACH ROW EXECUTE FUNCTION routegate_revocation_job_guard();
CREATE TRIGGER rg141_transfer_node BEFORE INSERT OR UPDATE ON vpn_account_transfer_nodes FOR EACH ROW EXECUTE FUNCTION routegate_revocation_job_guard();

CREATE FUNCTION routegate_revocation_account_guard() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF TG_OP='UPDATE' AND (to_jsonb(OLD)-ARRAY['email','phone','telegram_username','telegram_recipient_id','notes','updated_at']) = (to_jsonb(NEW)-ARRAY['email','phone','telegram_username','telegram_recipient_id','notes','updated_at']) THEN RETURN NEW; END IF;
 IF TG_OP<>'INSERT' THEN PERFORM routegate_revocation_node_guard(OLD.server_id); END IF;
 IF TG_OP<>'DELETE' THEN PERFORM routegate_revocation_node_guard(NEW.server_id); END IF;
 IF TG_OP='DELETE' THEN RETURN OLD; END IF; RETURN NEW;
END; $$;
CREATE TRIGGER rg141_account BEFORE INSERT OR UPDATE OR DELETE ON vpn_accounts FOR EACH ROW EXECUTE FUNCTION routegate_revocation_account_guard();
CREATE FUNCTION routegate_revocation_preferences_guard() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE account uuid; node uuid; current_node uuid;
BEGIN
 IF TG_TABLE_NAME='traffic_limits' AND TG_OP='UPDATE' THEN
  IF (to_jsonb(OLD)-ARRAY['enforcement_status','limit_exceeded_at','enforcement_updated_at','updated_at'])=(to_jsonb(NEW)-ARRAY['enforcement_status','limit_exceeded_at','enforcement_updated_at','updated_at']) THEN RETURN NEW; END IF;
 END IF;
 IF TG_OP='UPDATE' AND (to_jsonb(OLD)-'updated_at')=(to_jsonb(NEW)-'updated_at') THEN RETURN NEW; END IF;
 IF TG_TABLE_NAME='vpn_client_profiles' AND TG_OP='INSERT' AND EXISTS(SELECT 1 FROM vpn_client_profiles WHERE vpn_account_id=NEW.vpn_account_id) THEN RETURN NEW; END IF;
 IF TG_TABLE_NAME='vpn_account_protocols' AND TG_OP='INSERT' THEN
 IF NOT NEW.desired_explicit THEN RETURN NEW; END IF; END IF;
 IF TG_OP='UPDATE' AND OLD.vpn_account_id IS DISTINCT FROM NEW.vpn_account_id THEN
  SELECT server_id INTO node FROM vpn_accounts WHERE id=OLD.vpn_account_id;PERFORM routegate_revocation_node_guard(node);
  SELECT server_id INTO current_node FROM vpn_accounts WHERE id=OLD.vpn_account_id;
  IF current_node IS DISTINCT FROM node THEN RAISE EXCEPTION USING ERRCODE='P0141',MESSAGE='account_placement_changed'; END IF;
 END IF;
 IF TG_OP='DELETE' THEN account:=OLD.vpn_account_id; ELSE account:=NEW.vpn_account_id; END IF;
 -- Policy disabling must remain possible while an unrelated operation waits
 -- on the account row. Serialize via the node, then recheck placement instead.
 SELECT server_id INTO node FROM vpn_accounts WHERE id=account;
 PERFORM routegate_revocation_node_guard(node);
 SELECT server_id INTO current_node FROM vpn_accounts WHERE id=account;
 IF current_node IS DISTINCT FROM node THEN RAISE EXCEPTION USING ERRCODE='P0141',MESSAGE='account_placement_changed'; END IF;
 IF TG_OP='DELETE' THEN RETURN OLD; END IF; RETURN NEW;
END; $$;
CREATE TRIGGER rg141_profile BEFORE INSERT OR UPDATE OR DELETE ON vpn_client_profiles FOR EACH ROW EXECUTE FUNCTION routegate_revocation_preferences_guard();
CREATE TRIGGER rg141_protocols BEFORE INSERT OR UPDATE OR DELETE ON vpn_account_protocols FOR EACH ROW EXECUTE FUNCTION routegate_revocation_preferences_guard();
CREATE TRIGGER rg141_account_routing BEFORE INSERT OR UPDATE OR DELETE ON vpn_account_routing_profiles FOR EACH ROW EXECUTE FUNCTION routegate_revocation_preferences_guard();
CREATE TRIGGER rg141_account_groups BEFORE INSERT OR UPDATE OR DELETE ON vpn_account_node_groups FOR EACH ROW EXECUTE FUNCTION routegate_revocation_preferences_guard();
CREATE TRIGGER rg141_account_selection BEFORE INSERT OR UPDATE OR DELETE ON vpn_account_automatic_selection_policies FOR EACH ROW EXECUTE FUNCTION routegate_revocation_preferences_guard();
CREATE TRIGGER rg141_traffic_limit BEFORE INSERT OR UPDATE OR DELETE ON traffic_limits FOR EACH ROW EXECUTE FUNCTION routegate_revocation_preferences_guard();

CREATE FUNCTION routegate_revocation_server_guard() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF TG_OP='DELETE' THEN PERFORM routegate_revocation_node_guard(OLD.id); RETURN OLD; END IF;
 IF (to_jsonb(OLD)-ARRAY['status','last_seen_at','updated_at']) IS DISTINCT FROM (to_jsonb(NEW)-ARRAY['status','last_seen_at','updated_at']) THEN PERFORM routegate_revocation_node_guard(OLD.id); END IF;
 RETURN NEW;
END; $$;
CREATE TRIGGER rg141_server BEFORE UPDATE OR DELETE ON servers FOR EACH ROW EXECUTE FUNCTION routegate_revocation_server_guard();
CREATE FUNCTION routegate_revocation_agent_guard() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF TG_OP='DELETE' THEN PERFORM routegate_revocation_node_guard(OLD.server_id); RETURN OLD; END IF;
 IF (OLD.id,OLD.server_id,OLD.token_hash,OLD.credential_generation) IS DISTINCT FROM (NEW.id,NEW.server_id,NEW.token_hash,NEW.credential_generation) THEN
 PERFORM routegate_revocation_node_guard(OLD.server_id);PERFORM routegate_revocation_node_guard(NEW.server_id); END IF;
 RETURN NEW;
END; $$;
CREATE TRIGGER rg141_agent BEFORE UPDATE OR DELETE ON agents FOR EACH ROW EXECUTE FUNCTION routegate_revocation_agent_guard();
CREATE FUNCTION routegate_revocation_version_guard() RETURNS trigger LANGUAGE plpgsql AS $$
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
CREATE TRIGGER rg141_version BEFORE INSERT OR UPDATE OR DELETE ON config_versions FOR EACH ROW EXECUTE FUNCTION routegate_revocation_version_guard();

CREATE FUNCTION routegate_revocation_routing_guard() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF EXISTS(SELECT 1 FROM vpn_credential_revocations WHERE state='prepared') THEN RAISE EXCEPTION USING ERRCODE='P0141',MESSAGE='routing_reserved_for_revocation'; END IF;
 IF TG_OP='DELETE' THEN RETURN OLD; END IF; RETURN NEW;
END; $$;
CREATE TRIGGER rg141_rules BEFORE INSERT OR UPDATE OR DELETE ON routing_profile_rules FOR EACH ROW EXECUTE FUNCTION routegate_revocation_routing_guard();
CREATE TRIGGER rg141_server_routing BEFORE INSERT OR UPDATE OR DELETE ON server_routing_profiles FOR EACH ROW EXECUTE FUNCTION routegate_revocation_routing_guard();
CREATE TRIGGER rg141_routing BEFORE INSERT OR UPDATE OR DELETE ON routing_profiles FOR EACH ROW EXECUTE FUNCTION routegate_revocation_routing_guard();
CREATE TRIGGER rg141_rule_sets BEFORE INSERT OR UPDATE OR DELETE ON managed_routing_rule_sets FOR EACH ROW EXECUTE FUNCTION routegate_revocation_routing_guard();
CREATE FUNCTION routegate_revocation_manager_update_guard() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF NEW.operation='apply' THEN
 PERFORM pg_advisory_xact_lock(140159);
 IF EXISTS(SELECT 1 FROM vpn_credential_revocations WHERE state='prepared') THEN RAISE EXCEPTION USING ERRCODE='P0141',MESSAGE='manager_update_reserved_for_revocation'; END IF;
 END IF; RETURN NEW;
END; $$;
CREATE TRIGGER rg141_manager_update BEFORE INSERT OR UPDATE ON update_jobs FOR EACH ROW EXECUTE FUNCTION routegate_revocation_manager_update_guard();
