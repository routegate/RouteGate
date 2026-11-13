package db

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"regexp"
	"time"

	"github.com/ikaevus/routegate/backend/internal/auth"
	"github.com/ikaevus/routegate/backend/internal/recoveryenroll"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// This file is test-only. No production schema, role grants, HTTP route,
// Agent Runner, runtime gate, dispatcher, VPN mutation or unlock is changed.
// The permission, grant ledger and audit triggers exist only in disposable
// PostgreSQL fixture schemas. Prior preparation alone is NOT consent to issue.
const enrollmentChallengeIssuePermissionV1 = "runtime_recovery:enrollment_challenge_issue"
const explicitEnrollmentChallengeActionV1 = "authorize_one_agent_enrollment_challenge"

var errEnrollmentOperatorGrantRefusedV1 = errors.New("offline enrollment operator grant refused")
var enrollmentGrantCanonicalUUIDV1 = regexp.MustCompile(
	`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`,
)

const enrollmentOperatorGrantRehearsalSchemaV1 = `
CREATE TABLE rg140_test_enrollment_challenge_grants(
 id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
 preparation_id uuid NOT NULL UNIQUE
   REFERENCES vpn_runtime_recovery_enrollment_preparations(id) ON DELETE RESTRICT,
 agent_id uuid NOT NULL REFERENCES agents(id) ON DELETE RESTRICT,
 server_id uuid NOT NULL REFERENCES servers(id) ON DELETE RESTRICT,
 credential_generation bigint NOT NULL CHECK(credential_generation>0),
 actor_user_id uuid NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
 actor_session_id uuid NOT NULL REFERENCES auth_sessions(id) ON DELETE RESTRICT,
 state text NOT NULL DEFAULT 'granted_unattested'
   CHECK(state='granted_unattested'),
 granted_at timestamptz NOT NULL,
 expires_at timestamptz NOT NULL,
 CHECK(expires_at>granted_at AND expires_at<=granted_at+interval '120 seconds')
);
CREATE TABLE rg140_test_enrollment_challenge_grant_events(
 sequence bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
 grant_id uuid NOT NULL REFERENCES rg140_test_enrollment_challenge_grants(id) ON DELETE RESTRICT,
 action text NOT NULL CHECK(action='granted_unattested'),
 created_at timestamptz NOT NULL DEFAULT clock_timestamp()
);
CREATE FUNCTION rg140_test_grant_audit() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 INSERT INTO rg140_test_enrollment_challenge_grant_events(grant_id,action)
 VALUES(NEW.id,'granted_unattested');
 RETURN NEW;
END; $$;
CREATE TRIGGER rg140_test_grant_audit_insert
 AFTER INSERT ON rg140_test_enrollment_challenge_grants
 FOR EACH ROW EXECUTE FUNCTION rg140_test_grant_audit();
CREATE FUNCTION rg140_test_grant_immutable() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 RAISE EXCEPTION 'rg140_test_grant_immutable';
END; $$;
CREATE TRIGGER rg140_test_grant_immutable_row
 BEFORE UPDATE OR DELETE ON rg140_test_enrollment_challenge_grants
 FOR EACH ROW EXECUTE FUNCTION rg140_test_grant_immutable();
CREATE TRIGGER rg140_test_grant_events_immutable_row
 BEFORE UPDATE OR DELETE ON rg140_test_enrollment_challenge_grant_events
 FOR EACH ROW EXECUTE FUNCTION rg140_test_grant_immutable();
ALTER TABLE rg140_test_enrollment_attempts
 ADD COLUMN grant_id uuid UNIQUE
 REFERENCES rg140_test_enrollment_challenge_grants(id) ON DELETE RESTRICT;
`

type enrollmentOperatorGrantV1 struct {
	ID string
	PreparationID string
	AgentID string
	ServerID string
	Generation int64
	ActorID string
	SessionID string
	Unattested bool
}

func verifyEnrollmentOperatorSessionTxV1(
	ctx context.Context, tx pgx.Tx, operator auth.AuthenticatedUser,
) error {
	if !enrollmentGrantCanonicalUUIDV1.MatchString(operator.ID) ||
		!enrollmentGrantCanonicalUUIDV1.MatchString(operator.SessionID) {
		return errEnrollmentOperatorGrantRefusedV1
	}
	var id string
	err:=tx.QueryRow(ctx,`
SELECT sess.id::text
FROM auth_sessions sess
JOIN users u ON u.id=sess.user_id
JOIN user_roles ur ON ur.user_id=u.id
JOIN roles r ON r.id=ur.role_id
JOIN role_permissions rp ON rp.role_id=r.id
JOIN permissions perm ON perm.id=rp.permission_id
WHERE sess.id=$1::uuid AND sess.user_id=$2::uuid
  AND sess.revoked_at IS NULL AND sess.expires_at>clock_timestamp()
  AND u.user_type='human' AND u.status='active'
  AND r.code='super_admin'
  AND perm.code=$3
LIMIT 1 FOR SHARE OF sess,u,ur,r,rp,perm
`,operator.SessionID,operator.ID,enrollmentChallengeIssuePermissionV1).Scan(&id)
	if err!=nil || id!=operator.SessionID {
		return errEnrollmentOperatorGrantRefusedV1
	}
	return nil
}

func enrollmentOperatorGrantBlockersTxV1(
	ctx context.Context, tx pgx.Tx, serverID string,
) error {
	var blockers int64
	err:=tx.QueryRow(ctx,`
SELECT (SELECT count(*) FROM vpn_runtime_recovery_reservations
          WHERE server_id=$1::uuid)
     + (SELECT count(*) FROM vpn_runtime_pre_dispatch_checkpoints
          WHERE server_id=$1::uuid)
     + (SELECT count(*) FROM config_apply_jobs
          WHERE server_id=$1::uuid AND status IN ('pending','in_progress'))
     + (SELECT count(*) FROM agent_operation_jobs
          WHERE server_id=$1::uuid AND status IN ('pending','in_progress'))
     + (SELECT count(*) FROM agent_platform_update_jobs
          WHERE server_id=$1::uuid AND status IN
             ('pending','in_progress','mutation_dispatched','outcome_unknown'))
`,serverID).Scan(&blockers)
	if err!=nil || blockers!=0 {
		return errEnrollmentOperatorGrantRefusedV1
	}
	return nil
}

// An explicit grant is a NEW and separately committed user operation; an
// earlier prepared_unattested record is insufficient. Live session, role,
// dedicated permission, Agent generation and quiet Manager state are checked
// under PostgreSQL row locks. Both the grant audit event and attributed
// audit_events record MUST commit in the same DB transaction.
func grantEnrollmentChallengeRehearsalV1(
	ctx context.Context, pool *pgxpool.Pool, preparationID, action string,
	operator auth.AuthenticatedUser,
) (enrollmentOperatorGrantV1,error) {
	return grantEnrollmentChallengeWithTTLV1(
		ctx,pool,preparationID,action,operator,60*time.Second,
	)
}

func grantEnrollmentChallengeWithTTLV1(
	ctx context.Context, pool *pgxpool.Pool, preparationID, action string,
	operator auth.AuthenticatedUser, ttl time.Duration,
) (enrollmentOperatorGrantV1,error) {
	deny:=func()(enrollmentOperatorGrantV1,error){
		return enrollmentOperatorGrantV1{},errEnrollmentOperatorGrantRefusedV1
	}
	if ctx==nil || ctx.Err()!=nil || pool==nil ||
		action!=explicitEnrollmentChallengeActionV1 ||
		!enrollmentGrantCanonicalUUIDV1.MatchString(preparationID) ||
		ttl<time.Millisecond || ttl>120*time.Second {
		return deny()
	}
	tx,err:=pool.BeginTx(ctx,pgx.TxOptions{IsoLevel:pgx.ReadCommitted})
	if err!=nil{return deny()}
	defer tx.Rollback(ctx)
	if verifyEnrollmentOperatorSessionTxV1(ctx,tx,operator)!=nil{return deny()}
	var p recoveryenroll.Preparation
	var generation int64
	var status string
	var fencing bool
	var events int64
	err=tx.QueryRow(ctx,`
SELECT p.id::text,p.agent_id::text,p.server_id::text,
       p.agent_credential_generation,p.phase,
       a.credential_generation,a.status,
       (a.capabilities @> '{"runtimeMutationFencingV1":true}'::jsonb),
       (SELECT count(*) FROM vpn_runtime_recovery_enrollment_preparation_events e
         WHERE e.preparation_id=p.id AND e.action='prepared_unattested')
FROM vpn_runtime_recovery_enrollment_preparations p
JOIN agents a ON a.id=p.agent_id AND a.server_id=p.server_id
JOIN servers s ON s.id=p.server_id
WHERE p.id=$1::uuid
FOR UPDATE OF a,s
`,preparationID).Scan(
		&p.ID,&p.AgentID,&p.ServerID,&p.AgentCredentialGeneration,
		&p.Phase,&generation,&status,&fencing,&events,
	)
	if err!=nil||p.Phase!=recoveryenroll.PhasePreparedUnattested||
		generation!=p.AgentCredentialGeneration||status=="disabled"||
		!fencing||events!=1||
		enrollmentOperatorGrantBlockersTxV1(ctx,tx,p.ServerID)!=nil {
		return deny()
	}
	var id string
	err=tx.QueryRow(ctx,`
INSERT INTO rg140_test_enrollment_challenge_grants(
 preparation_id,agent_id,server_id,credential_generation,
 actor_user_id,actor_session_id,granted_at,expires_at
)
SELECT $1::uuid,$2::uuid,$3::uuid,$4,$5::uuid,$6::uuid,
 stamp.granted_at,stamp.granted_at+($7::bigint*interval '1 millisecond')
FROM (SELECT clock_timestamp() AS granted_at) stamp
RETURNING id::text
`,p.ID,p.AgentID,p.ServerID,p.AgentCredentialGeneration,
		operator.ID,operator.SessionID,ttl.Milliseconds()).Scan(&id)
	if err!=nil{return deny()}
	_,err=tx.Exec(ctx,`
INSERT INTO audit_events(
 actor_user_id,actor_type,action,resource_type,resource_id,result,metadata
) VALUES(
 $1::uuid,'user','runtime_recovery.enrollment_challenge_granted_unattested',
 'agent',$2::uuid,'success',
 jsonb_build_object('session_id',$3::text,
   'preparation_id',$4::text,'grant_id',$5::text,
   'credential_generation',$6::bigint,'state','granted_unattested')
)
`,operator.ID,p.AgentID,operator.SessionID,p.ID,id,p.AgentCredentialGeneration)
	if err!=nil{return deny()}
	if err=tx.Commit(ctx);err!=nil{return deny()}
	return enrollmentOperatorGrantV1{
		ID:id,PreparationID:p.ID,AgentID:p.AgentID,ServerID:p.ServerID,
		Generation:p.AgentCredentialGeneration,ActorID:operator.ID,
		SessionID:operator.SessionID,Unattested:true,
	},nil
}

// A SECOND explicit invocation is required to issue the Manager challenge.
// It re-checks the SAME live super-admin/session/dedicated permission and
// the original immutable grant under locks. No challenge is returned before
// the attempt and attributed issuance audit commit together.
func issueGrantedEnrollmentChallengeRehearsalV1(
	ctx context.Context,pool *pgxpool.Pool,grantID string,
	operator auth.AuthenticatedUser,
) (enrollmentOneShotRehearsalIssueV1,error) {
	deny:=func()(enrollmentOneShotRehearsalIssueV1,error){
		return enrollmentOneShotRehearsalIssueV1{},errEnrollmentOperatorGrantRefusedV1
	}
	if ctx==nil||ctx.Err()!=nil||pool==nil||
		!enrollmentGrantCanonicalUUIDV1.MatchString(grantID) {
		return deny()
	}
	nonce:=make([]byte,32)
	if _,err:=rand.Read(nonce);err!=nil{return deny()}
	tx,err:=pool.BeginTx(ctx,pgx.TxOptions{IsoLevel:pgx.ReadCommitted})
	if err!=nil{return deny()}
	defer tx.Rollback(ctx)
	if verifyEnrollmentOperatorSessionTxV1(ctx,tx,operator)!=nil{return deny()}

	var grant enrollmentOperatorGrantV1
	var currentGeneration int64
	var agentStatus,phase string
	var fencing bool
	var grantEvents,preparationEvents int64
	err=tx.QueryRow(ctx,`
SELECT g.id::text,g.preparation_id::text,g.agent_id::text,
       g.server_id::text,g.credential_generation,
       g.actor_user_id::text,g.actor_session_id::text,
       a.credential_generation,a.status,
       (a.capabilities @> '{"runtimeMutationFencingV1":true}'::jsonb),
       p.phase,
       (SELECT count(*) FROM rg140_test_enrollment_challenge_grant_events e
         WHERE e.grant_id=g.id AND e.action='granted_unattested'),
       (SELECT count(*) FROM vpn_runtime_recovery_enrollment_preparation_events e
         WHERE e.preparation_id=p.id AND e.action='prepared_unattested')
FROM rg140_test_enrollment_challenge_grants g
JOIN vpn_runtime_recovery_enrollment_preparations p
 ON p.id=g.preparation_id AND p.agent_id=g.agent_id
 AND p.server_id=g.server_id AND p.agent_credential_generation=g.credential_generation
JOIN agents a ON a.id=g.agent_id AND a.server_id=g.server_id
JOIN servers s ON s.id=g.server_id
WHERE g.id=$1::uuid AND g.state='granted_unattested'
  AND clock_timestamp()<g.expires_at
FOR UPDATE OF g,a,s
`,grantID).Scan(
		&grant.ID,&grant.PreparationID,&grant.AgentID,&grant.ServerID,
		&grant.Generation,&grant.ActorID,&grant.SessionID,
		&currentGeneration,&agentStatus,&fencing,&phase,
		&grantEvents,&preparationEvents,
	)
	if err!=nil||grant.ActorID!=operator.ID||grant.SessionID!=operator.SessionID||
		currentGeneration!=grant.Generation||agentStatus=="disabled"||
		!fencing||phase!=recoveryenroll.PhasePreparedUnattested||
		grantEvents!=1||preparationEvents!=1||
		enrollmentOperatorGrantBlockersTxV1(ctx,tx,grant.ServerID)!=nil {
		return deny()
	}
	expected,err:=recoveryenroll.OfflineGenesisDigestUnattestedV1(
		recoveryenroll.Preparation{
			ID:grant.PreparationID,AgentID:grant.AgentID,
			ServerID:grant.ServerID,AgentCredentialGeneration:grant.Generation,
			Phase:phase,
		},
	)
	if err!=nil{return deny()}
	var attemptID string
	err=tx.QueryRow(ctx,`
INSERT INTO rg140_test_enrollment_attempts(
 preparation_id,agent_id,server_id,credential_generation,
 expected_genesis_sha256,nonce_sha256,issued_at,expires_at,grant_id
)
SELECT $1::uuid,$2::uuid,$3::uuid,$4,$5,$6,
       stamp.issued_at,stamp.issued_at+interval '30 seconds',$7::uuid
FROM (SELECT clock_timestamp() AS issued_at) stamp
WHERE EXISTS(SELECT 1 FROM rg140_test_enrollment_challenge_grants g
 WHERE g.id=$7::uuid AND clock_timestamp()<g.expires_at)
RETURNING id::text
`,grant.PreparationID,grant.AgentID,grant.ServerID,grant.Generation,
	expected,digestEnrollmentOneShotNonceV1(nonce),grant.ID).Scan(&attemptID)
	if err!=nil{return deny()}
	_,err=tx.Exec(ctx,`
INSERT INTO audit_events(
 actor_user_id,actor_type,action,resource_type,resource_id,result,metadata
) VALUES($1::uuid,'user','runtime_recovery.enrollment_challenge_issued_unattested',
 'agent',$2::uuid,'success',
 jsonb_build_object('session_id',$3::text,'grant_id',$4::text,
  'attempt_id',$5::text,'preparation_id',$6::text,'state','issued_unattested'))
`,operator.ID,grant.AgentID,operator.SessionID,grant.ID,attemptID,grant.PreparationID)
	if err!=nil{return deny()}
	if err=tx.Commit(ctx);err!=nil{return deny()}
	return enrollmentOneShotRehearsalIssueV1{
		ID:attemptID,Nonce:base64.RawURLEncoding.EncodeToString(nonce),
		ExpectedGenesisSHA256:expected,Unattested:true,
	},nil
}

