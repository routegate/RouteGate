package db

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ikaevus/routegate/backend/internal/auth"
	"github.com/ikaevus/routegate/backend/internal/recoveryauth"
)

func verifiedRecoveryActor(f recoveryLedgerFixture) auth.AuthenticatedUser {
	return auth.AuthenticatedUser{
		UserProfile: auth.UserProfile{
			ID: f.operatorID, UserType: auth.UserTypeHuman, Status: "active",
			Roles: []string{auth.RoleSuperAdmin},
			Permissions: []string{auth.PermissionRuntimeRecoveryAuthorize},
		},
		SessionID: f.sessionID,
	}
}

func createVerifiedConsumerChallenge(t *testing.T, f recoveryLedgerFixture, change func(*recoveryauth.Commitment)) (string,string) {
	t.Helper()
	var reservation recoveryauth.Reservation
	err:=f.pool.QueryRow(f.ctx,`
SELECT id::text,server_id::text,agent_id::text,job_id::text,job_kind,
       agent_credential_generation,mutation_intent_sha256,
       result_receipt_sha256,runtime_witness_sha256
  FROM vpn_runtime_recovery_reservations WHERE id=$1::uuid
`,f.reservationID).Scan(
		&reservation.ID,&reservation.ServerID,&reservation.AgentID,&reservation.JobID,
		&reservation.JobKind,&reservation.AgentCredentialGeneration,
		&reservation.MutationIntentSHA256,&reservation.ResultReceiptSHA256,
		&reservation.RuntimeWitnessSHA256,
	)
	if err!=nil{t.Fatal(err)}
	var dbNow time.Time
	if err=f.pool.QueryRow(f.ctx,`SELECT date_trunc('second',clock_timestamp())`).Scan(&dbNow);err!=nil{t.Fatal(err)}
	token,commitment,err:=recoveryauth.BuildChallenge(verifiedRecoveryActor(f),reservation,dbNow)
	if err!=nil{t.Fatal(err)}
	if change!=nil{change(&commitment)}
	var id string
	err=f.pool.QueryRow(f.ctx,`
INSERT INTO vpn_runtime_recovery_challenges
  (reservation_id,operator_user_id,operator_session_id,
   context_sha256,challenge_sha256,issued_at,expires_at)
VALUES($1::uuid,$2::uuid,$3::uuid,$4,$5,$6,$7)
RETURNING id::text
`,reservation.ID,f.operatorID,f.sessionID,commitment.ContextSHA256,
		commitment.ChallengeSHA256,commitment.IssuedAt,commitment.ExpiresAt).Scan(&id)
	if err!=nil{t.Fatal("persist isolated commitment",err)}
	return id,token
}

func assertRecoveryChallengeState(t *testing.T,f recoveryLedgerFixture,id,want string,consumedEvents int) {
	t.Helper()
	var state string
	var eventCount int
	err:=f.pool.QueryRow(f.ctx,`
SELECT c.state,
  (SELECT count(*) FROM vpn_runtime_recovery_challenge_events e
    WHERE e.challenge_id=c.id AND e.action='consumed')
FROM vpn_runtime_recovery_challenges c WHERE c.id=$1::uuid
`,id).Scan(&state,&eventCount)
	if err!=nil||state!=want||eventCount!=consumedEvents{
		t.Fatalf("unexpected challenge state=%q consumed events=%d err=%v; want %q/%d",state,eventCount,err,want,consumedEvents)
	}
	var holdCount int
	err=f.pool.QueryRow(f.ctx,`SELECT count(*) FROM vpn_runtime_recovery_reservations WHERE id=$1::uuid`,f.reservationID).Scan(&holdCount)
	if err!=nil||holdCount!=1{t.Fatalf("recovery reservation was released: count=%d err=%v",holdCount,err)}
}

func TestRuntimeRecoveryVerifiedConsumerAcceptsOnlyMatchingSecretOnce(t *testing.T) {
	f:=newRecoveryLedgerFixture(t);defer f.done()
	id,token:=createVerifiedConsumerChallenge(t,f,nil)
	c:=recoveryauth.NewConsumer(f.pool)
	principal:=verifiedRecoveryActor(f)

	if err:=c.Consume(f.ctx,id,strings.Repeat("0",len(token)),principal);!errors.Is(err,recoveryauth.ErrChallengeRejected){
		t.Fatalf("wrong secret was not rejected: %v",err)
	}
	assertRecoveryChallengeState(t,f,id,"issued",0)
	if err:=c.Consume(f.ctx,id,token,principal);err!=nil{t.Fatal(err)}
	assertRecoveryChallengeState(t,f,id,"consumed",1)
	if err:=c.Consume(f.ctx,id,token,principal);!errors.Is(err,recoveryauth.ErrChallengeRejected){
		t.Fatalf("replayed secret was not rejected: %v",err)
	}
	assertRecoveryChallengeState(t,f,id,"consumed",1)
}

func TestRuntimeRecoveryVerifiedConsumerRaceExactlyOneWinner(t *testing.T){
	f:=newRecoveryLedgerFixture(t);defer f.done()
	id,token:=createVerifiedConsumerChallenge(t,f,nil)
	principal:=verifiedRecoveryActor(f)
	ctx,cancel:=context.WithTimeout(f.ctx,12*time.Second);defer cancel()
	type outcome struct{err error}
	results:=make(chan outcome,2)
	start:=make(chan struct{})
	var wg sync.WaitGroup
	for i:=0;i<2;i++{
		wg.Add(1)
		go func(){
			defer wg.Done()
			<-start
			c:=recoveryauth.NewConsumer(f.pool)
			results<-outcome{c.Consume(ctx,id,token,principal)}
		}()
	}
	close(start);wg.Wait();close(results)
	winners,denied:=0,0
	for x:=range results{
		switch {
		case x.err==nil:winners++
		case errors.Is(x.err,recoveryauth.ErrChallengeRejected):denied++
		default:t.Fatalf("unexpected DB error: %v",x.err)
		}
	}
	if winners!=1||denied!=1{t.Fatalf("same secret spent more than once: winners=%d denied=%d",winners,denied)}
	assertRecoveryChallengeState(t,f,id,"consumed",1)
}

func TestRuntimeRecoveryVerifiedConsumerRejectsTamperedEvidenceAndActor(t *testing.T) {
	for _,tc:=range []struct{
		name string
		change func(*recoveryauth.Commitment)
		principal func(*auth.AuthenticatedUser)
	}{
		{name:"bad_binding_hash",change:func(c *recoveryauth.Commitment){c.ContextSHA256=strings.Repeat("f",64)}},
		{name:"bad_secret_hash",change:func(c *recoveryauth.Commitment){c.ChallengeSHA256=strings.Repeat("e",64)}},
		{name:"different_authenticated_session",principal:func(u *auth.AuthenticatedUser){u.SessionID="11111111-1111-4111-8111-111111111111"}},
		{name:"different_authenticated_operator",principal:func(u *auth.AuthenticatedUser){u.ID="22222222-2222-4222-8222-222222222222"}},
		{name:"lost_role_in_request",principal:func(u *auth.AuthenticatedUser){u.Roles=[]string{auth.RoleAdmin}}},
	}{
		t.Run(tc.name,func(t *testing.T){
			f:=newRecoveryLedgerFixture(t);defer f.done()
			id,token:=createVerifiedConsumerChallenge(t,f,tc.change)
			principal:=verifiedRecoveryActor(f)
			if tc.principal!=nil{tc.principal(&principal)}
			err:=recoveryauth.NewConsumer(f.pool).Consume(f.ctx,id,token,principal)
			if !errors.Is(err,recoveryauth.ErrChallengeRejected){t.Fatalf("tampered evidence accepted or unexpected error: %v",err)}
			assertRecoveryChallengeState(t,f,id,"issued",0)
		})
	}
}

func TestRuntimeRecoveryVerifiedConsumerRechecksCurrentHumanAuthority(t *testing.T) {
	tests:=[]struct{name,sql string}{
		{"session_revoked",`UPDATE auth_sessions SET revoked_at=clock_timestamp() WHERE id=$1::uuid`},
		{"session_expired",`UPDATE auth_sessions SET expires_at=clock_timestamp()-interval '2 seconds' WHERE id=$1::uuid`},
		{"user_disabled",`UPDATE users SET status='disabled' WHERE id=$1::uuid`},
		{"role_removed",`DELETE FROM user_roles WHERE user_id=$1::uuid AND role_id=(SELECT id FROM roles WHERE code='super_admin')`},
	}
	for _,tc:=range tests{
		t.Run(tc.name,func(t *testing.T){
			f:=newRecoveryLedgerFixture(t);defer f.done()
			id,token:=createVerifiedConsumerChallenge(t,f,nil)
			target:=f.operatorID
			if strings.HasPrefix(tc.name,"session"){target=f.sessionID}
			if _,err:=f.pool.Exec(f.ctx,tc.sql,target);err!=nil{t.Fatal(err)}
			err:=recoveryauth.NewConsumer(f.pool).Consume(f.ctx,id,token,verifiedRecoveryActor(f))
			if !errors.Is(err,recoveryauth.ErrChallengeRejected){
				t.Fatalf("removed DB authority accepted or unexpected error: %v",err)
			}
			assertRecoveryChallengeState(t,f,id,"issued",0)
		})
	}
}

func TestRuntimeRecoveryVerifiedConsumerAuditFailureRollsBackConsumption(t *testing.T) {
	f:=newRecoveryLedgerFixture(t);defer f.done()
	id,token:=createVerifiedConsumerChallenge(t,f,nil)
	// Simulate a failure to append the required audit event. The state
	// transition must roll back rather than silently accept authorization.
	_,err:=f.pool.Exec(f.ctx,`
CREATE FUNCTION rg140_reject_consumed_challenge_audit() RETURNS trigger LANGUAGE plpgsql AS 'BEGIN IF NEW.action = ''consumed'' THEN RAISE EXCEPTION ''injected recovery audit disk failure''; END IF; RETURN NEW; END';
`)
	if err!=nil{t.Fatal(err)}
	_,err=f.pool.Exec(f.ctx,`
CREATE TRIGGER rg140_reject_consumed_audit
 BEFORE INSERT ON vpn_runtime_recovery_challenge_events
 FOR EACH ROW EXECUTE FUNCTION rg140_reject_consumed_challenge_audit();
`)
	if err!=nil{t.Fatal(err)}
	err=recoveryauth.NewConsumer(f.pool).Consume(f.ctx,id,token,verifiedRecoveryActor(f))
	if err==nil{t.Fatal("consumer accepted without durable audit")}
	assertRecoveryChallengeState(t,f,id,"issued",0)
}
