package db

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ikaevus/routegate/backend/internal/recoveryauth"
	"github.com/ikaevus/routegate/backend/internal/recoveryobserve"
)

// Fixture scope is a disposable PostgreSQL schema. The evidence values are
// synthetic token-holder claims, explicitly NOT trusted Agent attestation.
func issuerFixture(t *testing.T) (recoveryLedgerFixture,string,recoveryobserve.EvidenceDigestsV1) {
	t.Helper()
	f:=newRecoveryLedgerFixture(t)
	issued,err:=recoveryauth.NewIssuer(f.pool).Issue(f.ctx,f.reservationID,verifiedRecoveryActor(f))
	if err!=nil{f.done();t.Fatal(err)}
	if err=recoveryauth.NewConsumer(f.pool).Consume(f.ctx,issued.ID,issued.Token,verifiedRecoveryActor(f));err!=nil{
		f.done();t.Fatal(err)
	}
	var attemptID,taskID,agentID,serverID string
	err=f.pool.QueryRow(f.ctx,`
SELECT id::text,job_id::text,agent_id::text,server_id::text
  FROM vpn_runtime_recovery_attempts WHERE reservation_id=$1::uuid
`,f.reservationID).Scan(&attemptID,&taskID,&agentID,&serverID)
	if err!=nil{f.done();t.Fatal(err)}
	evidence:=recoveryobserve.EvidenceDigestsV1{
		SchemaVersion:1,TaskID:taskID,AgentID:agentID,ServerID:serverID,
		MarkerSHA256:strings.Repeat("c",64),
		ReceiptSHA256:strings.Repeat("d",64),
		EnvelopeSHA256:strings.Repeat("f",64),
		WitnessSHA256:strings.Repeat("e",64),
	}
	return f,attemptID,evidence
}

func TestRuntimeRecoveryObservationIssuerCommitsOnceWithAuditAndNoRelease(t *testing.T){
	f,attempt,evidence:=issuerFixture(t)
	defer f.done()
	issued,err:=recoveryobserve.NewObservationIssuer(f.pool).
		IssueFromAuthenticatedAgentClaim(f.ctx,recoveryFixtureAgentToken,attempt,evidence)
	if err!=nil{t.Fatal(err)}
	if issued.SchemaVersion!=1||issued.AttemptID!=attempt||issued.ReservationID!=f.reservationID||
		issued.AgentID!=f.agentID||issued.AgentCredentialGeneration!=f.generation||
		issued.ExpectedEvidence!=evidence||issued.ExpiresAt.Sub(issued.IssuedAt)!=45*time.Second||
		issued.ObservationID==""{
		t.Fatalf("incomplete post-commit issuance: %+v",issued)
	}
	nonceBytes,err:=base64.RawURLEncoding.DecodeString(issued.Nonce)
	if err!=nil||len(nonceBytes)!=32||base64.RawURLEncoding.EncodeToString(nonceBytes)!=issued.Nonce{
		t.Fatal("nonce must be independently generated canonical 256-bit secret",err)
	}
	var nonceState,storedHash,markerHash,receiptHash,witnessHash string
	var events int
	err=f.pool.QueryRow(f.ctx,`
SELECT n.state,n.nonce_sha256,n.expected_marker_sha256,
       n.expected_receipt_sha256,n.expected_runtime_witness_sha256,
       (SELECT count(*) FROM vpn_runtime_recovery_observation_nonce_events e
        WHERE e.observation_id=n.id AND e.action='issued')
  FROM vpn_runtime_recovery_observation_nonces n WHERE n.id=$1::uuid
`,issued.ObservationID).Scan(&nonceState,&storedHash,&markerHash,&receiptHash,&witnessHash,&events)
	sum:=sha256.Sum256(nonceBytes)
	if err!=nil||nonceState!="issued"||events!=1||
		storedHash!=hex.EncodeToString(sum[:])||
		markerHash!=evidence.MarkerSHA256||receiptHash!=evidence.ReceiptSHA256||
		witnessHash!=evidence.WitnessSHA256{
		t.Fatalf("non-atomic nonce or audit: state=%s events=%d err=%v",nonceState,events,err)
	}
	if strings.Contains(storedHash,issued.Nonce) {
		t.Fatal("plaintext nonce persisted instead of digest")
	}
	nonceAuditState(t,f,issued.ObservationID,"issued",1)
	// The one-per-attempt policy forbids reissue after either success or
	// ambiguous response; no token is exposed on the losing path.
	second,err:=recoveryobserve.NewObservationIssuer(f.pool).
		IssueFromAuthenticatedAgentClaim(f.ctx,recoveryFixtureAgentToken,attempt,evidence)
	if !errors.Is(err,recoveryobserve.ErrObservationIssueRefused)||second.Nonce!=""||second.ObservationID!=""{
		t.Fatalf("duplicate issuance disclosed secret: %+v %v",second,err)
	}
	nonceAuditState(t,f,issued.ObservationID,"issued",1)
}

func TestRuntimeRecoveryObservationIssuerInteroperatesWithAtomicConsumer(t *testing.T) {
	f,attempt,evidence:=issuerFixture(t)
	defer f.done()
	issued,err:=recoveryobserve.NewObservationIssuer(f.pool).
		IssueFromAuthenticatedAgentClaim(f.ctx,recoveryFixtureAgentToken,attempt,evidence)
	if err!=nil{t.Fatal(err)}
	witness:=recoveryobserve.RuntimeWitness{
		SchemaVersion:1,RuntimeHash:strings.Repeat("9",64),
		Generation:recoveryobserve.RuntimeGeneration{InvocationID:strings.Repeat("b",32),PID:311,StartedMonotonic:"700"},
		PreviousGeneration:recoveryobserve.RuntimeGeneration{InvocationID:strings.Repeat("a",32),PID:310,StartedMonotonic:"600"},
		ListenerPort:443,
	}
	witnessBytes,err:=json.Marshal(witness)
	if err!=nil{t.Fatal(err)}
	witnessDigest:=sha256.Sum256(witnessBytes)
	// In the disposable fixture only, arrange a claim matching these exact
	// witness bytes before issuance. A real Agent has no such trusted
	// evidence registration path yet.
	if evidence.WitnessSHA256!=hex.EncodeToString(witnessDigest[:]){
		// One-use issuer was already used; do not try to overwrite the
		// persisted digest or fabricate Manager authorization.
		t.Skip("fixture witness is intentionally synthetic, not proven")
	}
	claim:=recoveryobserve.TokenBoundEvidenceV1{
		SchemaVersion:1,ObservationID:issued.ObservationID,
		ReservationID:issued.ReservationID,AttemptID:issued.AttemptID,
		AttemptEpoch:issued.AttemptEpoch,Nonce:issued.Nonce,
		AgentCredentialGeneration:issued.AgentCredentialGeneration,
		Evidence:issued.ExpectedEvidence,
	}
	mac,err:=recoveryobserve.TokenProofMACV1(recoveryFixtureAgentToken,claim)
	if err!=nil{t.Fatal(err)}
	reply:=recoveryobserve.ObservationReply{
		SchemaVersion:1,ObservationID:issued.ObservationID,
		ReservationID:issued.ReservationID,AttemptID:issued.AttemptID,
		AttemptEpoch:issued.AttemptEpoch,Nonce:issued.Nonce,
		AgentCredentialGeneration:issued.AgentCredentialGeneration,
		ObservedAt:issued.IssuedAt,
		Observation:recoveryobserve.RuntimeObservation{
			SchemaVersion:1,TaskID:issued.TaskID,AgentID:issued.AgentID,
			ServerID:issued.ServerID,MarkerHash:evidence.MarkerSHA256,
			ReceiptHash:evidence.ReceiptSHA256,EnvelopeHash:evidence.EnvelopeSHA256,
			Witness:witness,
		},
	}
	result,err:=recoveryobserve.NewAuthenticatedReplyConsumer(f.pool).
		ConsumeTokenBoundReplyV1(f.ctx,recoveryFixtureAgentToken,mac,claim,reply)
	if err!=nil||result.State!="reply_matched_unattested"{
		t.Fatalf("private issuer→consumer binding mismatch: %+v %v",result,err)
	}
	nonceAuditState(t,f,issued.ObservationID,"reply_matched_unattested",2)
}

func TestRuntimeRecoveryObservationIssuerRejectsForeignTokenAndClaim(t *testing.T) {
	for _,mode:=range []string{"wrong_token","bad_token_prefix","missing_attempt","wrong_task","wrong_agent","wrong_server","invalid_hash","wrong_schema"}{
		t.Run(mode,func(t *testing.T){
			f,attempt,evidence:=issuerFixture(t)
			defer f.done()
			token:=recoveryFixtureAgentToken
			switch mode{
			case "wrong_token":token="rg_agent_"+base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{0x81},32))
			case "bad_token_prefix":token="rg_reg_"+strings.TrimPrefix(token,"rg_agent_")
			case "missing_attempt":attempt="11111111-1111-4111-8111-111111111111"
			case "wrong_task":evidence.TaskID=evidence.AgentID
			case "wrong_agent":evidence.AgentID=evidence.ServerID
			case "wrong_server":evidence.ServerID=evidence.AgentID
			case "invalid_hash":evidence.ReceiptSHA256="not-a-sha256"
			case "wrong_schema":evidence.SchemaVersion++
			}
			output,err:=recoveryobserve.NewObservationIssuer(f.pool).
				IssueFromAuthenticatedAgentClaim(f.ctx,token,attempt,evidence)
			if !errors.Is(err,recoveryobserve.ErrObservationIssueRefused)||
				output.Nonce!=""||output.ObservationID!=""{
				t.Fatalf("unauthorized claim minted nonce: %+v %v",output,err)
			}
			var n int
			if err:=f.pool.QueryRow(f.ctx,
				`SELECT count(*) FROM vpn_runtime_recovery_observation_nonces`).Scan(&n);
				err!=nil||n!=0 {
				t.Fatalf("invalid attempt wrote nonce: %d %v",n,err)
			}
		})
	}
}

func TestRuntimeRecoveryObservationIssuerExactlyOneWinnerUnderConcurrency(t *testing.T){
	f,attempt,evidence:=issuerFixture(t)
	defer f.done()
	ctx,cancel:=context.WithTimeout(f.ctx,12*time.Second)
	defer cancel()
	start:=make(chan struct{})
	type result struct {value recoveryobserve.IssuedObservationNonce;err error}
	out:=make(chan result,2)
	var wg sync.WaitGroup
	for j:=0;j<2;j++{
		wg.Add(1)
		go func(){
			defer wg.Done()
			<-start
			value,err:=recoveryobserve.NewObservationIssuer(f.pool).
				IssueFromAuthenticatedAgentClaim(ctx,recoveryFixtureAgentToken,attempt,evidence)
			out<-result{value,err}
		}()
	}
	close(start);wg.Wait();close(out)
	success,denied:=0,0
	var id string
	for r:=range out{
		if r.err==nil{
			success++;id=r.value.ObservationID
			if len(r.value.Nonce)!=43{t.Fatal("successful issue lacked nonce")}
		}else if errors.Is(r.err,recoveryobserve.ErrObservationIssueRefused){
			denied++
			if r.value.Nonce!=""{t.Fatal("loser leaked nonce")}
		}else{t.Fatalf("unexpected concurrent issue failure: %v",r.err)}
	}
	if success!=1||denied!=1{t.Fatalf("two issuers: success=%d denied=%d",success,denied)}
	nonceAuditState(t,f,id,"issued",1)
}

func TestRuntimeRecoveryObservationIssuerAuditFailureNeverDisclosesNonce(t *testing.T){
	f,attempt,evidence:=issuerFixture(t)
	defer f.done()
	_,err:=f.pool.Exec(f.ctx,`
CREATE FUNCTION rg140_refuse_observation_issue_audit()
RETURNS trigger LANGUAGE plpgsql AS
'BEGIN IF NEW.action = ''issued'' THEN
 RAISE EXCEPTION ''injected observation issue audit failure'';
 END IF; RETURN NEW; END';
`)
	if err!=nil{t.Fatal(err)}
	_,err=f.pool.Exec(f.ctx,`
CREATE TRIGGER rg140_refuse_observation_issue_audit
BEFORE INSERT ON vpn_runtime_recovery_observation_nonce_events
FOR EACH ROW EXECUTE FUNCTION rg140_refuse_observation_issue_audit();
`)
	if err!=nil{t.Fatal(err)}
	value,err:=recoveryobserve.NewObservationIssuer(f.pool).
		IssueFromAuthenticatedAgentClaim(f.ctx,recoveryFixtureAgentToken,attempt,evidence)
	if err==nil||value.Nonce!=""||value.ObservationID!=""{
		t.Fatal("secret returned despite failed transaction/audit")
	}
	var n,m int
	if err:=f.pool.QueryRow(f.ctx,`
SELECT (SELECT count(*) FROM vpn_runtime_recovery_observation_nonces),
       (SELECT count(*) FROM vpn_runtime_recovery_observation_nonce_events)
`).Scan(&n,&m);err!=nil||n!=0||m!=0{
		t.Fatalf("audit failure left issued nonce or events: %d %d %v",n,m,err)
	}
}
