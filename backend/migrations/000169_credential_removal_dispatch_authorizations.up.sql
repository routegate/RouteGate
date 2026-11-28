-- Dormant, human-issued dispatch authorization. Not a config version,
-- not an Agent task, and NOT a verified VPN revocation.
CREATE TABLE vpn_credential_removal_dispatch_authorizations (
 operation_id uuid PRIMARY KEY REFERENCES vpn_credential_revocation_dispatch_bindings(operation_id),
 actor_user_id uuid NOT NULL REFERENCES users(id),
 actor_session_id uuid NOT NULL REFERENCES auth_sessions(id),
 nonce text NOT NULL UNIQUE CHECK (nonce ~ '^[0-9a-f]{64}$'),
 candidate_version_id uuid NOT NULL,
 apply_job_id uuid NOT NULL,
 agent_credential_generation bigint NOT NULL CHECK (agent_credential_generation > 0),
 candidate_hash text NOT NULL CHECK (candidate_hash ~ '^[0-9a-f]{64}$'),
 candidate_runtime_hash text NOT NULL CHECK (candidate_runtime_hash ~ '^[0-9a-f]{64}$'),
 issued_at timestamptz NOT NULL,
 expires_at timestamptz NOT NULL,
 CHECK (expires_at > issued_at AND expires_at <= issued_at + interval '2 minutes')
);

CREATE FUNCTION routegate_removal_dispatch_authorization_guard()
RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE node uuid; prep vpn_credential_revocations;
 bind vpn_credential_revocation_dispatch_bindings;
BEGIN
 IF TG_OP <> 'INSERT' THEN
  RAISE EXCEPTION USING ERRCODE='P0153',MESSAGE='removal_dispatch_authorization_retained';
 END IF;
 SELECT r.server_id INTO node FROM vpn_credential_revocations r
  WHERE r.id=NEW.operation_id;
 IF node IS NULL THEN
  RAISE EXCEPTION USING ERRCODE='P0153',MESSAGE='removal_dispatch_authorization_unreserved';
 END IF;
 PERFORM id FROM servers WHERE id=node FOR UPDATE;
 SELECT * INTO prep FROM vpn_credential_revocations WHERE id=NEW.operation_id FOR UPDATE;
 SELECT * INTO bind FROM vpn_credential_revocation_dispatch_bindings
  WHERE operation_id=NEW.operation_id;
 PERFORM id FROM agents WHERE id=prep.agent_id FOR UPDATE;
 LOCK TABLE user_roles,roles,role_permissions,permissions IN SHARE MODE;
 PERFORM u.id FROM users u JOIN auth_sessions s ON s.user_id=u.id
  WHERE u.id=NEW.actor_user_id AND s.id=NEW.actor_session_id FOR SHARE OF u,s;
 IF prep.id IS NULL OR prep.state<>'prepared'
  OR bind.operation_id IS NULL
  OR NEW.candidate_version_id<>bind.candidate_version_id
  OR NEW.apply_job_id<>bind.apply_job_id
  OR NEW.agent_credential_generation<>bind.agent_credential_generation
  OR NEW.candidate_hash<>bind.candidate_hash
  OR NEW.candidate_runtime_hash<>bind.candidate_runtime_hash
  OR NEW.issued_at>clock_timestamp()
  OR NEW.expires_at<=clock_timestamp()
  OR NEW.expires_at>NEW.issued_at+interval '2 minutes'
  OR NOT EXISTS (
    SELECT 1 FROM servers s JOIN agents a ON a.id=prep.agent_id AND a.server_id=s.id
    WHERE s.id=prep.server_id AND s.status='active'
      AND s.active_config_version_id=prep.baseline_version_id
      AND a.credential_generation=prep.agent_credential_generation
      AND a.status='online'
      AND a.last_authenticated_heartbeat_generation=a.credential_generation
      AND a.last_authenticated_heartbeat_at>clock_timestamp()-interval '90 seconds'
      AND a.capabilities @> '{"sing-box":true,"systemctl":true,"ss":true}'::jsonb
  )
  OR NOT EXISTS (
    SELECT 1 FROM users u JOIN auth_sessions s ON s.user_id=u.id
    WHERE u.id=NEW.actor_user_id AND s.id=NEW.actor_session_id
      AND u.status='active' AND u.user_type='human'
      AND s.revoked_at IS NULL AND s.expires_at>=NEW.expires_at
      AND EXISTS (SELECT 1 FROM user_roles ur JOIN roles r ON r.id=ur.role_id
        WHERE ur.user_id=u.id AND r.code='super_admin')
      AND EXISTS (SELECT 1 FROM user_roles ur JOIN role_permissions rp ON rp.role_id=ur.role_id
        JOIN permissions p ON p.id=rp.permission_id
        WHERE ur.user_id=u.id AND p.code='configs:apply')
      AND EXISTS (SELECT 1 FROM user_roles ur JOIN role_permissions rp ON rp.role_id=ur.role_id
        JOIN permissions p ON p.id=rp.permission_id
        WHERE ur.user_id=u.id AND p.code='system:manage')
      AND EXISTS (SELECT 1 FROM user_roles ur JOIN role_permissions rp ON rp.role_id=ur.role_id
        JOIN permissions p ON p.id=rp.permission_id
        WHERE ur.user_id=u.id AND p.code='vpn_users:disable')
  )
 THEN
  RAISE EXCEPTION USING ERRCODE='P0153',MESSAGE='removal_dispatch_authorization_invalid';
 END IF;
 RETURN NEW;
END; $$;
CREATE TRIGGER rg153_dispatch_authorization
 BEFORE INSERT OR UPDATE OR DELETE ON vpn_credential_removal_dispatch_authorizations
 FOR EACH ROW EXECUTE FUNCTION routegate_removal_dispatch_authorization_guard();
