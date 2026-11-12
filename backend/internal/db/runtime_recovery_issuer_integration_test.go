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

func TestRuntimeRecoveryIssuerCommitsAuditBeforeReturningSecret(t *testing.T) {
	f:=newRecoveryLedgerFixture(t)
	defer f.done()
	issued,err:=recoveryauth.NewIssuer(f.pool).Issue(f.ctx,f.reservationID,verifiedRecoveryActor(f))
	if err!=nil{t.Fatal(err)}
	if issued.ID=="" || len(issued.Token)!=43 || issued.Commitment.Reservation.ID!=f.reservationID {
		t.Fatal("issuer returned incomplete commitment")
	}
	if issued.Commitment.ExpiresAt.Sub(issued.Commitment.IssuedAt)!=90*time.Second{
		t.Fatal("issuer did not bind exactly 90 seconds")
	}
	var state,contextHash,tokenHash string
	var issuedCount int
	err=f.pool.QueryRow(f.ctx,`
SELECT c.state,c.context_sha256,c.challenge_sha256,
 (SELECT count(*) FROM vpn_runtime_recovery_challenge_events e
  WHERE e.challenge_id=c.id AND e.action='issued')
FROM vpn_runtime_recovery_challenges c WHERE c.id=$1::uuid
`,issued.ID).Scan(&state,&contextHash,&tokenHash,&issuedCount)
	if err!=nil{t.Fatal(err)}
	if state!="issued"||issuedCount!=1||
		contextHash!=issued.Commitment.ContextSHA256||tokenHash!=issued.Commitment.ChallengeSHA256{
		t.Fatalf("commit or audit incomplete: state=%s event=%d",state,issuedCount)
	}
	if strings.Contains(contextHash,issued.Token)||strings.Contains(tokenHash,issued.Token) {
		t.Fatal("plaintext token persisted")
	}
	// Prove that the separately reviewed consumer accepts this exact issuer
	// commitment, rather than a synthetic direct-SQL fixture.
	if err:=recoveryauth.NewConsumer(f.pool).Consume(f.ctx,issued.ID,issued.Token,verifiedRecoveryActor(f));err!=nil {
		t.Fatal("issued token could not be consumed:",err)
	}
	assertRecoveryChallengeState(t,f,issued.ID,"consumed",1)
	if err:=recoveryauth.NewConsumer(f.pool).Consume(f.ctx,issued.ID,issued.Token,verifiedRecoveryActor(f));
		!errors.Is(err,recoveryauth.ErrChallengeRejected) {
		t.Fatal("issued secret replay accepted:",err)
	}
}

func TestRuntimeRecoveryIssuerConcurrentAttemptsOnlyOneDisclosed(t *testing.T) {
	f:=newRecoveryLedgerFixture(t)
	defer f.done()
	ctx,cancel:=context.WithTimeout(f.ctx,15*time.Second)
	defer cancel()
	start:=make(chan struct{})
	type outcome struct{issued recoveryauth.IssuedChallenge;err error}
	outputs:=make(chan outcome,2)
	var wg sync.WaitGroup
	for n:=0;n<2;n++{
		wg.Add(1)
		go func(){
			defer wg.Done()
			<-start
			issued,err:=recoveryauth.NewIssuer(f.pool).Issue(ctx,f.reservationID,verifiedRecoveryActor(f))
			outputs<-outcome{issued,err}
		}()
	}
	close(start)
	wg.Wait()
	close(outputs)
	success,denied:=0,0
	var challengeID string
	for result:=range outputs{
		switch{
		case result.err==nil:
			success++
			challengeID=result.issued.ID
			if result.issued.Token==""{t.Fatal("committed result had no secret")}
		case errors.Is(result.err,recoveryauth.ErrChallengeIssueRefused):
			denied++
			if result.issued.Token!=""||result.issued.ID!=""{
				t.Fatal("denied issuer disclosed uncommitted plaintext token")
			}
		default:t.Fatalf("unexpected concurrent issuance error: %v",result.err)
		}
	}
	if success!=1||denied!=1{t.Fatalf("double issue permitted: success=%d denied=%d",success,denied)}
	var total,events int
	err:=f.pool.QueryRow(f.ctx,`
SELECT (SELECT count(*) FROM vpn_runtime_recovery_challenges WHERE reservation_id=$1::uuid),
       (SELECT count(*) FROM vpn_runtime_recovery_challenge_events WHERE challenge_id=$2::uuid AND action='issued')
`,f.reservationID,challengeID).Scan(&total,&events)
	if err!=nil||total!=1||events!=1{t.Fatalf("duplicate committed issue: rows=%d events=%d err=%v",total,events,err)}
}

func TestRuntimeRecoveryIssuerRechecksHumanSessionAndRole(t *testing.T) {
	for _,tc:=range []struct{
		name,sql string
		session bool
	}{
		{"revoked_session",`UPDATE auth_sessions SET revoked_at=clock_timestamp() WHERE id=$1::uuid`,true},
		{"expired_session",`UPDATE auth_sessions SET expires_at=clock_timestamp()-interval '1 second' WHERE id=$1::uuid`,true},
		{"disabled_human",`UPDATE users SET status='disabled' WHERE id=$1::uuid`,false},
		{"removed_super_admin",`DELETE FROM user_roles WHERE user_id=$1::uuid AND role_id=(SELECT id FROM roles WHERE code='super_admin')`,false},
	}{
		t.Run(tc.name,func(t *testing.T){
			f:=newRecoveryLedgerFixture(t)
			defer f.done()
			id:=f.operatorID
			if tc.session{id=f.sessionID}
			if _,err:=f.pool.Exec(f.ctx,tc.sql,id);err!=nil{t.Fatal(err)}
			result,err:=recoveryauth.NewIssuer(f.pool).Issue(f.ctx,f.reservationID,verifiedRecoveryActor(f))
			if !errors.Is(err,recoveryauth.ErrChallengeIssueRefused)||result.Token!="" {
				t.Fatalf("invalid human authority yielded token or unexpected error: %+v %v",result.Commitment,err)
			}
			var count int
			if err:=f.pool.QueryRow(f.ctx,`SELECT count(*) FROM vpn_runtime_recovery_challenges`).Scan(&count);err!=nil||count!=0 {
				t.Fatalf("unauthorized challenge recorded: count=%d err=%v",count,err)
			}
		})
	}
}

func TestRuntimeRecoveryIssuerRejectsInvalidReservationAndStalePrincipal(t *testing.T) {
	f:=newRecoveryLedgerFixture(t)
	defer f.done()
	wrong:=verifiedRecoveryActor(f)
	wrong.Roles=[]string{auth.RoleAdmin}
	for _,tc:=range []struct{
		name,reservation string
		principal auth.AuthenticatedUser
	}{
		{"unknown_reservation","11111111-1111-4111-8111-111111111111",verifiedRecoveryActor(f)},
		{"malformed_reservation","not-a-uuid",verifiedRecoveryActor(f)},
		{"missing_session",f.reservationID,auth.AuthenticatedUser{}},
		{"stale_admin",f.reservationID,wrong},
	}{
		t.Run(tc.name,func(t *testing.T){
			result,err:=recoveryauth.NewIssuer(f.pool).Issue(f.ctx,tc.reservation,tc.principal)
			if !errors.Is(err,recoveryauth.ErrChallengeIssueRefused)||result.Token!=""{
				t.Fatalf("issuer accepted invalid reservation/actor: err=%v",err)
			}
		})
	}
	var count int
	if err:=f.pool.QueryRow(f.ctx,`SELECT count(*) FROM vpn_runtime_recovery_challenges`).Scan(&count);err!=nil||count!=0{
		t.Fatalf("unsafe invalid attempt was persisted: count=%d err=%v",count,err)
	}
}

func TestRuntimeRecoveryIssuerAuditWriteFailureNeverReturnsSecret(t *testing.T) {
	f:=newRecoveryLedgerFixture(t)
	defer f.done()
	// Test database only: a later audit failure must atomically abort the
	// challenge INSERT and the issuer must not return a plaintext token.
	_,err:=f.pool.Exec(f.ctx,`
CREATE FUNCTION rg140_reject_issued_challenge_audit()
RETURNS trigger LANGUAGE plpgsql AS
'BEGIN IF NEW.action = ''issued'' THEN
 RAISE EXCEPTION ''injected issue audit failure'';
 END IF; RETURN NEW; END';
`)
	if err!=nil{t.Fatal(err)}
	_,err=f.pool.Exec(f.ctx,`
CREATE TRIGGER rg140_reject_issued_audit
BEFORE INSERT ON vpn_runtime_recovery_challenge_events
FOR EACH ROW EXECUTE FUNCTION rg140_reject_issued_challenge_audit();
`)
	if err!=nil{t.Fatal(err)}
	result,err:=recoveryauth.NewIssuer(f.pool).Issue(f.ctx,f.reservationID,verifiedRecoveryActor(f))
	if err==nil||result.Token!=""||result.ID!=""{
		t.Fatal("plaintext token exposed before successful issue+audit commit")
	}
	var rows,events int
	if err:=f.pool.QueryRow(f.ctx,`
SELECT (SELECT count(*) FROM vpn_runtime_recovery_challenges),
       (SELECT count(*) FROM vpn_runtime_recovery_challenge_events)
`).Scan(&rows,&events);err!=nil||rows!=0||events!=0{
		t.Fatalf("failed audit left a challenge: rows=%d events=%d err=%v",rows,events,err)
	}
}

func TestRuntimeRecoveryIssuerCannotReissueAfterConsumption(t *testing.T) {
	f:=newRecoveryLedgerFixture(t)
	defer f.done()
	i:=recoveryauth.NewIssuer(f.pool)
	first,err:=i.Issue(f.ctx,f.reservationID,verifiedRecoveryActor(f))
	if err!=nil{t.Fatal(err)}
	if err:=recoveryauth.NewConsumer(f.pool).Consume(f.ctx,first.ID,first.Token,verifiedRecoveryActor(f));err!=nil{t.Fatal(err)}
	result,err:=i.Issue(f.ctx,f.reservationID,verifiedRecoveryActor(f))
	if !errors.Is(err,recoveryauth.ErrChallengeIssueRefused)||result.Token!=""{
		t.Fatalf("reissued challenge after consumption: err=%v",err)
	}
	assertRecoveryChallengeState(t,f,first.ID,"consumed",1)
}
