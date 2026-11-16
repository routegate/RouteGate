package db

import (
	"context"
	"errors"
	"sync"
	"strings"
	"testing"
	"time"
)

type enrollmentOperatorGrantFixtureV1 struct {
	authorizedPreparationFixtureV1
	preparationID string
}

// Prepared in a separate committed transaction; the second permission and
// both additional ledgers are installed only in this disposable DB schema.
func newEnrollmentOperatorGrantFixtureV1(t *testing.T) enrollmentOperatorGrantFixtureV1 {
	t.Helper()
	f:=newAuthorizedPreparationFixtureV1(t)
	prepared,err:=f.prepare.Prepare(f.ctx,f.agentID,f.generation,f.operator)
	if err!=nil{f.done();t.Fatal("prepare synthetic Agent:",err)}
	if _,err=f.pool.Exec(f.ctx,enrollmentOneShotRehearsalSchemaV1);
		err!=nil{f.done();t.Fatal("install disposable nonce ledger:",err)}
	if _,err=f.pool.Exec(f.ctx,enrollmentOperatorGrantRehearsalSchemaV1);
		err!=nil{f.done();t.Fatal("install disposable human approval ledger:",err)}
	if _,err=f.pool.Exec(f.ctx,`
INSERT INTO permissions(code,name)
VALUES($1,'RG-140 disposable challenge issuance')
ON CONFLICT(code) DO NOTHING
`,enrollmentChallengeIssuePermissionV1);err!=nil{f.done();t.Fatal(err)}
	if _,err=f.pool.Exec(f.ctx,`
INSERT INTO role_permissions(role_id,permission_id)
SELECT r.id,p.id FROM roles r,permissions p
WHERE r.code='super_admin' AND p.code=$1
ON CONFLICT DO NOTHING
`,enrollmentChallengeIssuePermissionV1);err!=nil{f.done();t.Fatal(err)}
	return enrollmentOperatorGrantFixtureV1{f,prepared.ID}
}

func requireOperatorGrantRefusedV1(t *testing.T,err error){
	t.Helper()
	if !errors.Is(err,errEnrollmentOperatorGrantRefusedV1){
		t.Fatalf("expected explicit human grant refusal, got %v",err)
	}
}

func grantForFixtureV1(t *testing.T,f enrollmentOperatorGrantFixtureV1) enrollmentOperatorGrantV1{
	t.Helper()
	g,err:=grantEnrollmentChallengeRehearsalV1(
		f.ctx,f.pool,f.preparationID,explicitEnrollmentChallengeActionV1,f.operator,
	)
	if err!=nil{t.Fatal(err)}
	return g
}

func operatorGrantCountsV1(t *testing.T,f enrollmentOperatorGrantFixtureV1)(int,int,int,int){
	t.Helper()
	var grants,grantEvents,attempts,issuedAudits int
	err:=f.pool.QueryRow(f.ctx,`
SELECT
 (SELECT count(*) FROM rg140_test_enrollment_challenge_grants),
 (SELECT count(*) FROM rg140_test_enrollment_challenge_grant_events),
 (SELECT count(*) FROM rg140_test_enrollment_attempts),
 (SELECT count(*) FROM audit_events
    WHERE action IN (
     'runtime_recovery.enrollment_challenge_granted_unattested',
     'runtime_recovery.enrollment_challenge_issued_unattested'))
`).Scan(&grants,&grantEvents,&attempts,&issuedAudits)
	if err!=nil{t.Fatal(err)}
	return grants,grantEvents,attempts,issuedAudits
}

func TestRuntimeRecoveryEnrollmentOperatorGrantV1RequiresSeparateApprovalAndAudit(t *testing.T){
	f:=newEnrollmentOperatorGrantFixtureV1(t)
	defer f.done()

	// Preparation alone cannot issue an authenticated challenge.
	_,err:=issueGrantedEnrollmentChallengeRehearsalV1(
		f.ctx,f.pool,"",f.operator,
	)
	requireOperatorGrantRefusedV1(t,err)
	_,err=grantEnrollmentChallengeRehearsalV1(
		f.ctx,f.pool,f.preparationID,"prepare_only",f.operator,
	)
	requireOperatorGrantRefusedV1(t,err)

	grant:=grantForFixtureV1(t,f)
	if !grant.Unattested||grant.ActorID!=f.operator.ID||
		grant.SessionID!=f.operator.SessionID||grant.PreparationID!=f.preparationID||
		grant.Generation!=f.generation||grant.AgentID!=f.agentID||grant.ServerID!=f.serverID {
		t.Fatalf("operator grant not scoped to current Manager Agent: %+v",grant)
	}
	g,e,a,au:=operatorGrantCountsV1(t,f)
	if g!=1||e!=1||a!=0||au!=1 {
		t.Fatalf("grant not committed and audited before challenge: %d/%d/%d/%d",g,e,a,au)
	}
	issued,err:=issueGrantedEnrollmentChallengeRehearsalV1(
		f.ctx,f.pool,grant.ID,f.operator,
	)
	if err!=nil||!issued.Unattested||issued.ID==""||issued.Nonce==""{
		t.Fatalf("separate authorized issue failed: %+v %v",issued,err)
	}
	g,e,a,au=operatorGrantCountsV1(t,f)
	if g!=1||e!=1||a!=1||au!=2{
		t.Fatalf("challenge and attributed audit were not atomic: %d/%d/%d/%d",g,e,a,au)
	}
	var referencedGrantID,operatorSession string
	err=f.pool.QueryRow(f.ctx,`
SELECT o.grant_id::text,g.actor_session_id::text
 FROM rg140_test_enrollment_attempts o
 JOIN rg140_test_enrollment_challenge_grants g ON g.id=o.grant_id
 WHERE o.id=$1::uuid
`,issued.ID).Scan(&referencedGrantID,&operatorSession)
	if err!=nil||referencedGrantID!=grant.ID||operatorSession!=f.operator.SessionID{
		t.Fatalf("Manager challenge not pinned to durable human grant: %v",err)
	}
	_,err=issueGrantedEnrollmentChallengeRehearsalV1(
		f.ctx,f.pool,grant.ID,f.operator,
	)
	requireOperatorGrantRefusedV1(t,err)
	reply:=signedEnrollmentRehearsalReplyV1(t,enrollmentOneShotFixtureV1{f.recoveryReservationFixture},issued)
	mac:=signedEnrollmentRehearsalMACV1(t,recoveryFixtureAgentToken,reply)
	observed,err:=consumeSignedEnrollmentProbeRehearsalV1(f.ctx,f.pool,recoveryFixtureAgentToken,reply,mac)
	if err!=nil||!observed.Unattested||observed.State!="reply_matched_unattested"{
		t.Fatalf("fresh human approval was promoted to actual enrollment: %+v %v",observed,err)
	}
}

func TestRuntimeRecoveryEnrollmentOperatorGrantV1RefusesForgedOrRevokedSessions(t *testing.T){
	for _,mode:=range []string{"wrong_action","wrong_session","revoked_session",
		"expired_session","disabled_human","removed_role","removed_issue_permission"}{
		t.Run(mode,func(t *testing.T){
			f:=newEnrollmentOperatorGrantFixtureV1(t);defer f.done()
			action:=explicitEnrollmentChallengeActionV1
			operator:=f.operator
			var err error
			switch mode{
			case "wrong_action":action="prepare_only"
			case "wrong_session":operator.SessionID="aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
			case "revoked_session":
				_,err=f.pool.Exec(f.ctx,`UPDATE auth_sessions SET revoked_at=clock_timestamp() WHERE id=$1::uuid`,operator.SessionID)
			case "expired_session":
				_,err=f.pool.Exec(f.ctx,`UPDATE auth_sessions SET expires_at=clock_timestamp()-interval '1 second' WHERE id=$1::uuid`,operator.SessionID)
			case "disabled_human":
				_,err=f.pool.Exec(f.ctx,`UPDATE users SET status='disabled' WHERE id=$1::uuid`,operator.ID)
			case "removed_role":
				_,err=f.pool.Exec(f.ctx,`DELETE FROM user_roles
 WHERE user_id=$1::uuid AND role_id=(SELECT id FROM roles WHERE code='super_admin')`,operator.ID)
			case "removed_issue_permission":
				_,err=f.pool.Exec(f.ctx,`DELETE FROM role_permissions
 WHERE role_id=(SELECT id FROM roles WHERE code='super_admin')
 AND permission_id=(SELECT id FROM permissions WHERE code=$1)`,enrollmentChallengeIssuePermissionV1)
			}
			if err!=nil{t.Fatal(err)}
			got,err:=grantEnrollmentChallengeRehearsalV1(
				f.ctx,f.pool,f.preparationID,action,operator,
			)
			requireOperatorGrantRefusedV1(t,err)
			if got!=(enrollmentOperatorGrantV1{}){
				t.Fatal("unauthorized human received grant",got)
			}
			g,e,a,au:=operatorGrantCountsV1(t,f)
			if g!=0||e!=0||a!=0||au!=0 {
				t.Fatalf("unauthorized preparation became enrollment permission: %d/%d/%d/%d",g,e,a,au)
			}
		})
	}
}

func TestRuntimeRecoveryEnrollmentOperatorGrantV1RechecksFreshPermissionAtIssue(t *testing.T){
	for _,mode:=range []string{"session_revoked","issue_permission_removed",
		"agent_generation_rotated","new_pending_job","manager_recovery_hold"}{
		t.Run(mode,func(t *testing.T){
			f:=newEnrollmentOperatorGrantFixtureV1(t);defer f.done()
			grant:=grantForFixtureV1(t,f)
			var err error
			switch mode {
			case "session_revoked":
				_,err=f.pool.Exec(f.ctx,`UPDATE auth_sessions SET revoked_at=clock_timestamp() WHERE id=$1::uuid`,f.operator.SessionID)
			case "issue_permission_removed":
				_,err=f.pool.Exec(f.ctx,`DELETE FROM role_permissions
 WHERE role_id=(SELECT id FROM roles WHERE code='super_admin')
 AND permission_id=(SELECT id FROM permissions WHERE code=$1)`,enrollmentChallengeIssuePermissionV1)
			case "agent_generation_rotated":
				_,err=f.pool.Exec(f.ctx,`UPDATE agents SET
 credential_generation=credential_generation+1,
 last_authenticated_heartbeat_at=NULL,
 last_authenticated_heartbeat_generation=NULL
 WHERE id=$1::uuid`,f.agentID)
			case "new_pending_job":
				_,err=f.pool.Exec(f.ctx,`INSERT INTO config_apply_jobs(
 server_id,agent_id,config_version_id,action,status)
 VALUES($1::uuid,$2::uuid,$3::uuid,'apply','pending')`,f.serverID,f.agentID,f.configVersionID)
			case "manager_recovery_hold":
				err=f.reserve(f.ctx,f.generation,"config_apply")
			}
			if err!=nil{t.Fatal(err)}
			got,err:=issueGrantedEnrollmentChallengeRehearsalV1(
				f.ctx,f.pool,grant.ID,f.operator,
			)
			requireOperatorGrantRefusedV1(t,err)
			if got!=(enrollmentOneShotRehearsalIssueV1{}){
				t.Fatal("unsafe grant issued challenge receipt",got)
			}
			g,e,a,au:=operatorGrantCountsV1(t,f)
			if g!=1||e!=1||a!=0||au!=1 {
				t.Fatalf("stale operator approval issued a challenge: %d/%d/%d/%d",g,e,a,au)
			}
		})
	}
}

func TestRuntimeRecoveryEnrollmentOperatorGrantV1ConcurrentGrantAndIssueSingleWinner(t *testing.T){
	f:=newEnrollmentOperatorGrantFixtureV1(t)
	defer f.done()
	ctx,cancel:=context.WithTimeout(f.ctx,12*time.Second)
	defer cancel()
	start:=make(chan struct{})
	results:=make(chan error,2)
	var wg sync.WaitGroup
	for i:=0;i<2;i++{
		wg.Add(1)
		go func(){
			defer wg.Done()
			<-start
			_,err:=grantEnrollmentChallengeRehearsalV1(
				ctx,f.pool,f.preparationID,explicitEnrollmentChallengeActionV1,f.operator,
			)
			results<-err
		}()
	}
	close(start);wg.Wait();close(results)
	ok,no:=0,0
	for err:=range results{
		if err==nil{ok++}else if errors.Is(err,errEnrollmentOperatorGrantRefusedV1){
			no++
		}else{t.Fatal(err)}
	}
	if ok!=1||no!=1{t.Fatalf("duplicate explicit grant: %d/%d",ok,no)}
	var grantID string
	if err:=f.pool.QueryRow(f.ctx,`SELECT id::text FROM rg140_test_enrollment_challenge_grants`).Scan(&grantID);err!=nil{t.Fatal(err)}
	start2:=make(chan struct{})
	results2:=make(chan error,2)
	for i:=0;i<2;i++{
		wg.Add(1)
		go func(){
			defer wg.Done();<-start2
			_,err:=issueGrantedEnrollmentChallengeRehearsalV1(
				ctx,f.pool,grantID,f.operator,
			)
			results2<-err
		}()
	}
	close(start2);wg.Wait();close(results2)
	ok,no=0,0
	for err:=range results2{
		if err==nil{ok++}else if errors.Is(err,errEnrollmentOperatorGrantRefusedV1){no++}else{t.Fatal(err)}
	}
	if ok!=1||no!=1{t.Fatalf("duplicate issued challenge: %d/%d",ok,no)}
	g,e,a,au:=operatorGrantCountsV1(t,f)
	if g!=1||e!=1||a!=1||au!=2 {
		t.Fatalf("racing human approvals made incomplete history: %d/%d/%d/%d",g,e,a,au)
	}
}

func injectEnrollmentOperatorGrantAuditFailureV1(t *testing.T,f enrollmentOperatorGrantFixtureV1,action string){
	t.Helper()
	if action!="runtime_recovery.enrollment_challenge_granted_unattested" &&
		action!="runtime_recovery.enrollment_challenge_issued_unattested"{
		t.Fatal("unknown test-only audit fault action")
	}
	_,err:=f.pool.Exec(f.ctx,`CREATE FUNCTION rg140_fail_grant_attributed_audit() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF NEW.action='`+action+`' THEN
  RAISE EXCEPTION 'injected human enrollment audit failure';
 END IF;
 RETURN NEW;
END; $$`)
	if err!=nil{t.Fatal(err)}
	_,err=f.pool.Exec(f.ctx,`CREATE TRIGGER rg140_fail_grant_attributed_audit
BEFORE INSERT ON audit_events
FOR EACH ROW EXECUTE FUNCTION rg140_fail_grant_attributed_audit()`)
	if err!=nil{t.Fatal(err)}
}

func TestRuntimeRecoveryEnrollmentOperatorGrantV1AuditFailureCannotCreateGrantOrIssue(t *testing.T){
	for _,mode:=range []string{"grant","issue"}{
		t.Run(mode,func(t *testing.T){
			f:=newEnrollmentOperatorGrantFixtureV1(t);defer f.done()
			var grant enrollmentOperatorGrantV1
			if mode=="issue"{grant=grantForFixtureV1(t,f)}
			if mode=="grant"{
				injectEnrollmentOperatorGrantAuditFailureV1(t,f,
					"runtime_recovery.enrollment_challenge_granted_unattested")
				got,err:=grantEnrollmentChallengeRehearsalV1(
					f.ctx,f.pool,f.preparationID,explicitEnrollmentChallengeActionV1,f.operator,
				)
				requireOperatorGrantRefusedV1(t,err)
				if got!=(enrollmentOperatorGrantV1{}){t.Fatal("failed audit leaked grant",got)}
				g,e,a,au:=operatorGrantCountsV1(t,f)
				if g!=0||e!=0||a!=0||au!=0{t.Fatalf("partial grant/audit survived: %d/%d/%d/%d",g,e,a,au)}
				return
			}
			injectEnrollmentOperatorGrantAuditFailureV1(t,f,
				"runtime_recovery.enrollment_challenge_issued_unattested")
			got,err:=issueGrantedEnrollmentChallengeRehearsalV1(
				f.ctx,f.pool,grant.ID,f.operator,
			)
			requireOperatorGrantRefusedV1(t,err)
			if got!=(enrollmentOneShotRehearsalIssueV1{}){t.Fatal("failed audit leaked nonce",got)}
			g,e,a,au:=operatorGrantCountsV1(t,f)
			if g!=1||e!=1||a!=0||au!=1{t.Fatalf("partial issuance survived: %d/%d/%d/%d",g,e,a,au)}
		})
	}
}


func TestRuntimeRecoveryEnrollmentOperatorGrantV1ExpiredApprovalCannotIssue(t *testing.T) {
	f:=newEnrollmentOperatorGrantFixtureV1(t)
	defer f.done()
	grant,err:=grantEnrollmentChallengeWithTTLV1(
		f.ctx,f.pool,f.preparationID,explicitEnrollmentChallengeActionV1,
		f.operator,5*time.Millisecond,
	)
	if err!=nil{t.Fatal(err)}
	// Only the database clock controls expiry; no rewriting of historical
	// grant rows and no fake timestamp mutation is performed.
	time.Sleep(40*time.Millisecond)
	got,err:=issueGrantedEnrollmentChallengeRehearsalV1(
		f.ctx,f.pool,grant.ID,f.operator,
	)
	requireOperatorGrantRefusedV1(t,err)
	if got!=(enrollmentOneShotRehearsalIssueV1{}){
		t.Fatal("expired human approval returned a nonce",got)
	}
	g,e,a,au:=operatorGrantCountsV1(t,f)
	if g!=1||e!=1||a!=0||au!=1{
		t.Fatalf("expired grant still issued nonce: %d/%d/%d/%d",g,e,a,au)
	}
	for _,ttl:=range []time.Duration{0,-time.Second,121*time.Second}{
		_,err:=grantEnrollmentChallengeWithTTLV1(
			f.ctx,f.pool,f.preparationID,explicitEnrollmentChallengeActionV1,
			f.operator,ttl,
		)
		requireOperatorGrantRefusedV1(t,err)
	}
}

func TestRuntimeRecoveryEnrollmentOperatorGrantV1GrantAndEventsAreAppendOnly(t *testing.T){
	f:=newEnrollmentOperatorGrantFixtureV1(t)
	defer f.done()
	grant:=grantForFixtureV1(t,f)
	for _,sql:=range []string{
		`UPDATE rg140_test_enrollment_challenge_grants
 SET state='granted_unattested' WHERE id=$1::uuid`,
		`DELETE FROM rg140_test_enrollment_challenge_grants WHERE id=$1::uuid`,
		`UPDATE rg140_test_enrollment_challenge_grant_events
 SET action='granted_unattested'`,
		`DELETE FROM rg140_test_enrollment_challenge_grant_events`,
		`TRUNCATE rg140_test_enrollment_challenge_grants CASCADE`,
		`TRUNCATE rg140_test_enrollment_challenge_grant_events`,
	}{
		var err error
		if strings.Contains(sql,"$1") {
			_,err=f.pool.Exec(f.ctx,sql,grant.ID)
		}else{
			_,err=f.pool.Exec(f.ctx,sql)
		}
		if err==nil{
			t.Fatalf("immutable grant history accepted destructive operation: %s",sql)
		}
	}
	g,e,a,au:=operatorGrantCountsV1(t,f)
	if g!=1||e!=1||a!=0||au!=1{
		t.Fatalf("grant history changed after destructive attempt: %d/%d/%d/%d",g,e,a,au)
	}
}

func TestRuntimeRecoveryEnrollmentOperatorGrantV1LegacyIssuerCannotBypassGrant(t *testing.T) {
	f:=newEnrollmentOperatorGrantFixtureV1(t)
	defer f.done()

	// ADR-0037 intentionally has an unauthenticated lower-level issuer for
	// its own disposable fixture. Once the operator grant schema is present,
	// the database INSERT gate must refuse that issuer, before and after
	// an actual human grant. A wrapper-only permission check is insufficient.
	_,err:=issueEnrollmentOneShotRehearsalV1(f.ctx,f.pool,f.agentID)
	requireOneShotRefusedV1(t,err)
	g,e,a,au:=operatorGrantCountsV1(t,f)
	if g!=0||e!=0||a!=0||au!=0 {
		t.Fatalf("unguarded issuer created Manager history: %d/%d/%d/%d",g,e,a,au)
	}

	grant:=grantForFixtureV1(t,f)
	_,err=issueEnrollmentOneShotRehearsalV1(f.ctx,f.pool,f.agentID)
	requireOneShotRefusedV1(t,err)
	g,e,a,au=operatorGrantCountsV1(t,f)
	if g!=1||e!=1||a!=0||au!=1 {
		t.Fatalf("low-level issuer spent or bypassed a fresh grant: %d/%d/%d/%d",g,e,a,au)
	}
	issued,err:=issueGrantedEnrollmentChallengeRehearsalV1(
		f.ctx,f.pool,grant.ID,f.operator,
	)
	if err!=nil||issued.ID==""||issued.Nonce=="" {
		t.Fatalf("grant-gated issuance broken after bypass refusal: %+v %v",issued,err)
	}
	g,e,a,au=operatorGrantCountsV1(t,f)
	if g!=1||e!=1||a!=1||au!=2 {
		t.Fatalf("gated issuance did not commit exactly once: %d/%d/%d/%d",g,e,a,au)
	}
}

func TestRuntimeRecoveryEnrollmentOperatorGrantV1DatabaseGateRechecksCurrentHuman(t *testing.T){
	for _,mode:=range []string{"revoked_session","expired_grant","removed_permission","wrong_generation"} {
		t.Run(mode,func(t *testing.T){
			f:=newEnrollmentOperatorGrantFixtureV1(t)
			defer f.done()
			var grant enrollmentOperatorGrantV1
			var err error
			if mode=="expired_grant"{
				grant,err=grantEnrollmentChallengeWithTTLV1(
					f.ctx,f.pool,f.preparationID,explicitEnrollmentChallengeActionV1,
					f.operator,5*time.Millisecond,
				)
				if err!=nil{t.Fatal(err)}
				time.Sleep(40*time.Millisecond)
			}else{
				grant=grantForFixtureV1(t,f)
			}
			switch mode {
			case "revoked_session":
				_,err=f.pool.Exec(f.ctx,`UPDATE auth_sessions
 SET revoked_at=clock_timestamp() WHERE id=$1::uuid`,f.operator.SessionID)
			case "removed_permission":
				_,err=f.pool.Exec(f.ctx,`DELETE FROM role_permissions
WHERE role_id=(SELECT id FROM roles WHERE code='super_admin')
AND permission_id=(SELECT id FROM permissions WHERE code=$1)`,
					enrollmentChallengeIssuePermissionV1)
			}
			if err!=nil{t.Fatal(err)}
			generation:=f.generation
			if mode=="wrong_generation"{generation++}
			// Direct SQL bypass simulation: the trigger MUST NOT trust a
			// caller-supplied grant_id without checking session, expiry,
			// permission and the immutable tuple.
			_,err=f.pool.Exec(f.ctx,`
INSERT INTO rg140_test_enrollment_attempts(
 preparation_id,agent_id,server_id,credential_generation,
 expected_genesis_sha256,nonce_sha256,issued_at,expires_at,grant_id)
VALUES(
 $1::uuid,$2::uuid,$3::uuid,$4,$5,$6,
 clock_timestamp(),clock_timestamp()+interval '30 seconds',$7::uuid)
`,f.preparationID,f.agentID,f.serverID,generation,
				strings.Repeat("1",64),strings.Repeat("2",64),grant.ID)
			if err==nil{
				t.Fatal("database accepted challenge INSERT without a current matching authorized grant")
			}
			g,e,a,au:=operatorGrantCountsV1(t,f)
			if g!=1||e!=1||a!=0||au!=1{
				t.Fatalf("database bypass changed Manager grant history: %d/%d/%d/%d",g,e,a,au)
			}
		})
	}
}
