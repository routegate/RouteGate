package db

import (
	"strings"
	"testing"

	"github.com/ikaevus/routegate/backend/internal/recoveryenroll"
)

// This file ONLY verifies atomic PostgreSQL audit attribution in isolated
// test schemas. Direct fixture SQL is deliberately used to prove that a
// future caller cannot bypass audit by choosing a different Go code path.
// Database-superuser forgery remains outside this exercise's trust model.
func insertDirectEnrollmentGrantForAuditV1(t *testing.T,f enrollmentOperatorGrantFixtureV1) string {
	t.Helper()
	var id string
	err:=f.pool.QueryRow(f.ctx,`
INSERT INTO rg140_test_enrollment_challenge_grants(
 preparation_id,agent_id,server_id,credential_generation,
 actor_user_id,actor_session_id,granted_at,expires_at)
SELECT $1::uuid,$2::uuid,$3::uuid,$4,$5::uuid,$6::uuid,
 stamp.granted_at,stamp.granted_at+interval '30 seconds'
FROM (SELECT clock_timestamp() AS granted_at) stamp
RETURNING id::text
`,f.preparationID,f.agentID,f.serverID,f.generation,
		f.operator.ID,f.operator.SessionID).Scan(&id)
	if err!=nil{t.Fatal("fixture direct grant denied: ",err)}
	return id
}

func insertDirectEnrollmentAttemptForAuditV1(
	t *testing.T, f enrollmentOperatorGrantFixtureV1, grantID string,
) (string,error){
	t.Helper()
	digest,err:=recoveryenroll.OfflineGenesisDigestUnattestedV1(
		recoveryenroll.Preparation{
			ID:f.preparationID,AgentID:f.agentID,ServerID:f.serverID,
			AgentCredentialGeneration:f.generation,
			Phase:recoveryenroll.PhasePreparedUnattested,
		},
	)
	if err!=nil{t.Fatal(err)}
	var id string
	err=f.pool.QueryRow(f.ctx,`
INSERT INTO rg140_test_enrollment_attempts(
 preparation_id,agent_id,server_id,credential_generation,
 expected_genesis_sha256,nonce_sha256,issued_at,expires_at,grant_id
)
SELECT $1::uuid,$2::uuid,$3::uuid,$4,$5,$6,
 stamp.issued_at,stamp.issued_at+interval '15 seconds',$7::uuid
FROM (SELECT clock_timestamp() AS issued_at) stamp
RETURNING id::text
`,f.preparationID,f.agentID,f.serverID,f.generation,
		digest,strings.Repeat("3",64),grantID).Scan(&id)
	return id,err
}

func TestRuntimeRecoveryEnrollmentDatabaseEnforcedAuditV1DirectSQLCannotSkipAttribution(t *testing.T){
	f:=newEnrollmentOperatorGrantFixtureV1(t)
	defer f.done()
	grantID:=insertDirectEnrollmentGrantForAuditV1(t,f)

	// A privileged direct SQL grant insert, bypassing Go, STILL produces
	// BOTH mandatory audit records. This does not assert genuine human
	// provenance against an already compromised Manager database.
	var grantEvent,grantAudit int
	err:=f.pool.QueryRow(f.ctx,`
SELECT
 (SELECT count(*) FROM rg140_test_enrollment_challenge_grant_events
    WHERE grant_id=$1::uuid),
 (SELECT count(*) FROM audit_events
    WHERE action='runtime_recovery.enrollment_challenge_granted_unattested'
      AND actor_user_id=$2::uuid AND resource_id=$3::uuid
      AND metadata->>'session_id'=$4
      AND metadata->>'grant_id'=$1::text)
`,grantID,f.operator.ID,f.agentID,f.operator.SessionID).Scan(&grantEvent,&grantAudit)
	if err!=nil||grantEvent!=1||grantAudit!=1 {
		t.Fatalf("direct grant bypassed mandatory attribution: %d/%d %v",
			grantEvent,grantAudit,err)
	}
	attemptID,err:=insertDirectEnrollmentAttemptForAuditV1(t,f,grantID)
	if err!=nil{t.Fatal("direct issued challenge rejected despite valid grant:",err)}
	var attemptEvent,issueAudit int
	err=f.pool.QueryRow(f.ctx,`
SELECT
 (SELECT count(*) FROM rg140_test_enrollment_attempt_events
    WHERE attempt_id=$1::uuid),
 (SELECT count(*) FROM audit_events
    WHERE action='runtime_recovery.enrollment_challenge_issued_unattested'
      AND actor_user_id=$2::uuid AND resource_id=$3::uuid
      AND metadata->>'session_id'=$4
      AND metadata->>'grant_id'=$5
      AND metadata->>'attempt_id'=$1::text)
`,attemptID,f.operator.ID,f.agentID,f.operator.SessionID,grantID).Scan(
		&attemptEvent,&issueAudit)
	if err!=nil||attemptEvent!=1||issueAudit!=1 {
		t.Fatalf("direct challenge bypassed mandatory attributed audit: %d/%d %v",
			attemptEvent,issueAudit,err)
	}
	g,e,a,au:=operatorGrantCountsV1(t,f)
	if g!=1||e!=1||a!=1||au!=2 {
		t.Fatalf("direct SQL history not exactly once audited: %d/%d/%d/%d",
			g,e,a,au)
	}
}

func TestRuntimeRecoveryEnrollmentDatabaseEnforcedAuditV1DirectGrantAuditFaultRollsBack(t *testing.T){
	f:=newEnrollmentOperatorGrantFixtureV1(t)
	defer f.done()
	injectEnrollmentOperatorGrantAuditFailureV1(
		t,f,"runtime_recovery.enrollment_challenge_granted_unattested",
	)
	var id string
	err:=f.pool.QueryRow(f.ctx,`
INSERT INTO rg140_test_enrollment_challenge_grants(
 preparation_id,agent_id,server_id,credential_generation,
 actor_user_id,actor_session_id,granted_at,expires_at)
SELECT $1::uuid,$2::uuid,$3::uuid,$4,$5::uuid,$6::uuid,
 stamp.granted_at,stamp.granted_at+interval '30 seconds'
FROM (SELECT clock_timestamp() AS granted_at) stamp
RETURNING id::text
`,f.preparationID,f.agentID,f.serverID,f.generation,
		f.operator.ID,f.operator.SessionID).Scan(&id)
	if err==nil||id!=""{
		t.Fatalf("direct grant committed without its attributed audit: %q %v",id,err)
	}
	g,e,a,au:=operatorGrantCountsV1(t,f)
	if g!=0||e!=0||a!=0||au!=0 {
		t.Fatalf("failed direct grant audit left partial state: %d/%d/%d/%d",g,e,a,au)
	}
}

func TestRuntimeRecoveryEnrollmentDatabaseEnforcedAuditV1DirectIssueAuditFaultRollsBack(t *testing.T){
	f:=newEnrollmentOperatorGrantFixtureV1(t)
	defer f.done()
	grantID:=insertDirectEnrollmentGrantForAuditV1(t,f)
	injectEnrollmentOperatorGrantAuditFailureV1(
		t,f,"runtime_recovery.enrollment_challenge_issued_unattested",
	)
	id,err:=insertDirectEnrollmentAttemptForAuditV1(t,f,grantID)
	if err==nil||id!=""{
		t.Fatalf("direct challenge committed without audit: %q %v",id,err)
	}
	g,e,a,au:=operatorGrantCountsV1(t,f)
	if g!=1||e!=1||a!=0||au!=1 {
		t.Fatalf("failed direct issue audit left partial state: %d/%d/%d/%d",g,e,a,au)
	}
}

// No test here authorizes enrollment, changes normal Agent job paths,
// or promotes an attempt beyond the original 'issued_unattested' state.
