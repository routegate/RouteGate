-- Dormant dispatch envelope: this is NEITHER a config version nor an
-- executable Agent job. The original reserved-node P0141 guards remain.
CREATE TABLE vpn_credential_removal_dispatch_stages (
 operation_id uuid PRIMARY KEY REFERENCES vpn_credential_removal_dispatch_authorizations(operation_id),
 candidate_version_id uuid NOT NULL UNIQUE,
 apply_job_id uuid NOT NULL UNIQUE,
 agent_id uuid NOT NULL REFERENCES agents(id),
 agent_credential_generation bigint NOT NULL CHECK (agent_credential_generation>0),
 candidate_hash text NOT NULL CHECK (candidate_hash ~ '^[0-9a-f]{64}$'),
 candidate_runtime_hash text NOT NULL CHECK (candidate_runtime_hash ~ '^[0-9a-f]{64}$'),
 actor_user_id uuid NOT NULL REFERENCES users(id),
 actor_session_id uuid NOT NULL REFERENCES auth_sessions(id),
 staged_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 CHECK(candidate_version_id <> apply_job_id)
);

-- A direct SQL writer cannot forge a staged candidate for a different job,
-- agent, generation, config, user, expired approval or cancelled reservation.
-- No general-purpose GUC switch, session variable or normal-Apply exception.
CREATE FUNCTION routegate_removal_dispatch_stage_guard() RETURNS trigger
LANGUAGE plpgsql AS $$
DECLARE node uuid; prep vpn_credential_revocations;
 b vpn_credential_revocation_dispatch_bindings;
 a vpn_credential_removal_dispatch_authorizations;
BEGIN
 IF TG_OP <> 'INSERT' THEN
  RAISE EXCEPTION USING ERRCODE='P0154',MESSAGE='removal_dispatch_stage_immutable';
 END IF;
 SELECT server_id INTO node FROM vpn_credential_revocations WHERE id=NEW.operation_id;
 IF node IS NULL THEN
  RAISE EXCEPTION USING ERRCODE='P0154',MESSAGE='removal_dispatch_stage_unknown';
 END IF;
 PERFORM id FROM servers WHERE id=node FOR UPDATE;
 SELECT * INTO prep FROM vpn_credential_revocations WHERE id=NEW.operation_id FOR UPDATE;
 PERFORM id FROM agents WHERE id=prep.agent_id FOR UPDATE;
 SELECT * INTO b FROM vpn_credential_revocation_dispatch_bindings WHERE operation_id=NEW.operation_id;
 SELECT * INTO a FROM vpn_credential_removal_dispatch_authorizations WHERE operation_id=NEW.operation_id;
 LOCK TABLE user_roles,roles,role_permissions,permissions IN SHARE MODE;
 PERFORM u.id FROM users u JOIN auth_sessions s ON s.user_id=u.id
  WHERE u.id=NEW.actor_user_id AND s.id=NEW.actor_session_id FOR SHARE OF u,s;
 IF prep.id IS NULL OR prep.state<>'prepared'
  OR b.operation_id IS NULL OR a.operation_id IS NULL
  OR NEW.candidate_version_id<>b.candidate_version_id
  OR NEW.apply_job_id<>b.apply_job_id
  OR NEW.agent_id<>prep.agent_id
  OR NEW.agent_credential_generation<>prep.agent_credential_generation
  OR NEW.agent_credential_generation<>b.agent_credential_generation
  OR NEW.candidate_hash<>prep.candidate_hash
  OR NEW.candidate_hash<>b.candidate_hash
  OR NEW.candidate_runtime_hash<>prep.candidate_runtime_hash
  OR NEW.candidate_runtime_hash<>b.candidate_runtime_hash
  OR a.candidate_version_id<>NEW.candidate_version_id
  OR a.apply_job_id<>NEW.apply_job_id
  OR a.agent_credential_generation<>NEW.agent_credential_generation
  OR a.candidate_hash<>NEW.candidate_hash
  OR a.candidate_runtime_hash<>NEW.candidate_runtime_hash
  OR a.actor_user_id<>NEW.actor_user_id
  OR a.actor_session_id<>NEW.actor_session_id
  OR a.issued_at>clock_timestamp() OR a.expires_at<=clock_timestamp()
  OR EXISTS(SELECT 1 FROM config_versions WHERE id=NEW.candidate_version_id)
  OR EXISTS(SELECT 1 FROM config_apply_jobs WHERE id=NEW.apply_job_id)
  OR NOT EXISTS(
    SELECT 1 FROM servers s JOIN agents ag ON ag.id=prep.agent_id AND ag.server_id=s.id
    JOIN config_versions v ON v.id=prep.baseline_version_id AND v.server_id=s.id
    JOIN vpn_accounts acc ON acc.id=prep.vpn_account_id AND acc.server_id=s.id
    WHERE s.id=prep.server_id AND s.status='active'
      AND s.active_config_version_id=prep.baseline_version_id
      AND v.status='applied' AND v.applied_at IS NOT NULL
      AND v.config_hash=prep.baseline_hash
      AND acc.status=prep.account_status
      AND ag.status='online' AND ag.credential_generation=prep.agent_credential_generation
      AND ag.last_authenticated_heartbeat_generation=ag.credential_generation
      AND ag.last_authenticated_heartbeat_at>clock_timestamp()-interval '90 seconds'
      AND ag.capabilities @> '{"sing-box":true,"systemctl":true,"ss":true}'::jsonb
  )
  OR NOT EXISTS(
    SELECT 1 FROM users u JOIN auth_sessions s ON s.user_id=u.id
    WHERE u.id=NEW.actor_user_id AND s.id=NEW.actor_session_id
      AND u.status='active' AND u.user_type='human'
      AND s.revoked_at IS NULL AND s.expires_at>=a.expires_at
      AND EXISTS(SELECT 1 FROM user_roles ur JOIN roles r ON r.id=ur.role_id
        WHERE ur.user_id=u.id AND r.code='super_admin')
      AND EXISTS(SELECT 1 FROM user_roles ur JOIN role_permissions rp ON rp.role_id=ur.role_id
        JOIN permissions p ON p.id=rp.permission_id
        WHERE ur.user_id=u.id AND p.code='configs:apply')
      AND EXISTS(SELECT 1 FROM user_roles ur JOIN role_permissions rp ON rp.role_id=ur.role_id
        JOIN permissions p ON p.id=rp.permission_id WHERE ur.user_id=u.id AND p.code='system:manage')
      AND EXISTS(SELECT 1 FROM user_roles ur JOIN role_permissions rp ON rp.role_id=ur.role_id
        JOIN permissions p ON p.id=rp.permission_id WHERE ur.user_id=u.id AND p.code='vpn_users:disable')
  )
 THEN
  RAISE EXCEPTION USING ERRCODE='P0154',MESSAGE='removal_dispatch_stage_invalid';
 END IF;
 RETURN NEW;
END; $$;
CREATE TRIGGER rg154_dispatch_stage
 BEFORE INSERT OR UPDATE OR DELETE ON vpn_credential_removal_dispatch_stages
 FOR EACH ROW EXECUTE FUNCTION routegate_removal_dispatch_stage_guard();
