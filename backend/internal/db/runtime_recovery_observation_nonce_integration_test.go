package db

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ikaevus/routegate/backend/internal/recoveryauth"
	"github.com/ikaevus/routegate/backend/internal/recoveryobserve"
	"github.com/jackc/pgx/v5/pgconn"
)

func nonceHash(v []byte) string {
	s:=sha256.Sum256(v)
	return hex.EncodeToString(s[:])
}

func observationFixture(t *testing.T, expiresAfter time.Duration) (recoveryLedgerFixture,string,string,recoveryobserve.AgentTransportIdentity,recoveryobserve.ObservationReply) {
	t.Helper()
	f:=newRecoveryLedgerFixture(t)
	issued,err:=recoveryauth.NewIssuer(f.pool).Issue(f.ctx,f.reservationID,verifiedRecoveryActor(f))
	if err!=nil { f.done();t.Fatal(err)}
	if err=recoveryauth.NewConsumer(f.pool).Consume(f.ctx,issued.ID,issued.Token,verifiedRecoveryActor(f));err!=nil{
		f.done();t.Fatal(err)
	}
	var attemptID,reservationID,serverID,agentID,taskID string
	var epoch,generation int64
	err=f.pool.QueryRow(f.ctx,`
SELECT id::text,reservation_id::text,server_id::text,agent_id::text,job_id::text,
       attempt_epoch,agent_credential_generation
FROM vpn_runtime_recovery_attempts WHERE reservation_id=$1::uuid
`,f.reservationID).Scan(&attemptID,&reservationID,&serverID,&agentID,&taskID,&epoch,&generation)
	if err!=nil {f.done();t.Fatal(err)}
	witness:=recoveryobserve.RuntimeWitness{
		SchemaVersion:1,RuntimeHash:strings.Repeat("e",64),
		Generation:recoveryobserve.RuntimeGeneration{InvocationID:strings.Repeat("b",32),PID:311,StartedMonotonic:"700"},
		PreviousGeneration:recoveryobserve.RuntimeGeneration{InvocationID:strings.Repeat("a",32),PID:310,StartedMonotonic:"600"},
		ListenerPort:443,
	}
	witnessData,err:=json.Marshal(witness)
	if err!=nil {f.done();t.Fatal(err)}
	marker:=strings.Repeat("c",64)
	receipt:=strings.Repeat("d",64)
	nonceBytes:=bytes.Repeat([]byte{0x92},32)
	nonce:=base64.RawURLEncoding.EncodeToString(nonceBytes)
	var observationID string
	var issuedAt time.Time
	err=f.pool.QueryRow(f.ctx,`
INSERT INTO vpn_runtime_recovery_observation_nonces(
 attempt_id,reservation_id,server_id,agent_id,task_id,attempt_epoch,
 agent_credential_generation,expected_marker_sha256,expected_receipt_sha256,
 expected_runtime_witness_sha256,nonce_sha256,issued_at,expires_at
)
VALUES($1::uuid,$2::uuid,$3::uuid,$4::uuid,$5::uuid,$6,$7,$8,$9,$10,$11,
       clock_timestamp(),clock_timestamp()+($12::bigint * interval '1 millisecond'))
RETURNING id::text,issued_at
`,attemptID,reservationID,serverID,agentID,taskID,epoch,generation,marker,receipt,
		nonceHash(witnessData),nonceHash(nonceBytes),expiresAfter.Milliseconds()).Scan(&observationID,&issuedAt)
	if err!=nil {f.done();t.Fatal("insert isolated dormant nonce",err)}
	principal:=recoveryobserve.AgentTransportIdentity{AgentID:agentID,ServerID:serverID}
	reply:=recoveryobserve.ObservationReply{
		SchemaVersion:1,ObservationID:observationID,
		ReservationID:reservationID,AttemptID:attemptID,AttemptEpoch:epoch,
		Nonce:nonce,AgentCredentialGeneration:generation,
		ObservedAt:issuedAt,
		Observation:recoveryobserve.RuntimeObservation{
			SchemaVersion:1,TaskID:taskID,AgentID:agentID,ServerID:serverID,
			MarkerHash:marker,ReceiptHash:receipt,EnvelopeHash:strings.Repeat("f",64),
			Witness:witness,
		},
	}
	return f,observationID,nonce,principal,reply
}

func nonceAuditState(t *testing.T,f recoveryLedgerFixture,id,expected string,events int) {
	t.Helper()
	var state string
	var count int
	err:=f.pool.QueryRow(f.ctx,`
SELECT n.state,(SELECT count(*) FROM vpn_runtime_recovery_observation_nonce_events e
    WHERE e.observation_id=n.id)
FROM vpn_runtime_recovery_observation_nonces n WHERE n.id=$1::uuid
`,id).Scan(&state,&count)
	if err!=nil || state!=expected || count!=events {
		t.Fatalf("nonce state=%q audit events=%d err=%v want %s/%d",state,count,err,expected,events)
	}
	var holds,attempts int
	err=f.pool.QueryRow(f.ctx,`
SELECT (SELECT count(*) FROM vpn_runtime_recovery_reservations WHERE id=$1::uuid),
       (SELECT count(*) FROM vpn_runtime_recovery_attempts WHERE reservation_id=$1::uuid)
`,f.reservationID).Scan(&holds,&attempts)
	if err!=nil||holds!=1||attempts!=1 {t.Fatalf("nonce reply released protected state: %d/%d %v",holds,attempts,err)}
}

func requireNonceGuard(t *testing.T,err error) {
	t.Helper()
	var pg *pgconn.PgError
	if !errors.As(err,&pg)||pg.Code!="P0145" {
		t.Fatalf("expected P0145 immutable/replay guard, got %v",err)
	}
}

func TestRuntimeRecoveryObservationNonceMatchedReplyIsUnattestedAndSingleUse(t *testing.T) {
	f,id,nonce,principal,reply:=observationFixture(t,30*time.Second)
	defer f.done()
	verifier:=recoveryobserve.NewVerifier(f.pool)
	received,err:=verifier.RecordOneReply(f.ctx,id,nonce,principal,reply)
	if err!=nil||received.State!="reply_matched_unattested"||received.ObservationID!=id {
		t.Fatalf("read-only matching record failed: %+v %v",received,err)
	}
	nonceAuditState(t,f,id,"reply_matched_unattested",2)
	if _,err=verifier.RecordOneReply(f.ctx,id,nonce,principal,reply);!errors.Is(err,recoveryobserve.ErrObservationRefused) {
		t.Fatalf("replayed same nonce was accepted: %v",err)
	}
	nonceAuditState(t,f,id,"reply_matched_unattested",2)
	var storedDigest string
	if err=f.pool.QueryRow(f.ctx,`SELECT response_sha256 FROM vpn_runtime_recovery_observation_nonces WHERE id=$1::uuid`,id).Scan(&storedDigest);err!=nil{
		t.Fatal(err)
	}
	if len(storedDigest)!=64 || strings.Contains(storedDigest,nonce){
		t.Fatal("plaintext nonce was persisted or digest is missing")
	}
}

func TestRuntimeRecoveryObservationNonceWrongNonceDoesNotConsume(t *testing.T) {
	f,id,_,principal,reply:=observationFixture(t,30*time.Second)
	defer f.done()
	verifier:=recoveryobserve.NewVerifier(f.pool)
	badNonce:=base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{0x11},32))
	if _,err:=verifier.RecordOneReply(f.ctx,id,badNonce,principal,reply);!errors.Is(err,recoveryobserve.ErrObservationRefused){
		t.Fatalf("unknown nonce accepted: %v",err)
	}
	nonceAuditState(t,f,id,"issued",1)
}

func TestRuntimeRecoveryObservationNonceWrongEvidenceConsumesAsRejection(t *testing.T) {
	for _,mode:=range []string{"marker","receipt","witness","attempt","generation","process","agent_identity","server_identity","observed_at"}{
		t.Run(mode,func(t *testing.T){
			f,id,nonce,principal,reply:=observationFixture(t,30*time.Second)
			defer f.done()
			switch mode {
			case "marker":reply.Observation.MarkerHash=strings.Repeat("1",64)
			case "receipt":reply.Observation.ReceiptHash=strings.Repeat("1",64)
			case "witness":reply.Observation.Witness.ListenerPort=444
			case "attempt":reply.AttemptEpoch++
			case "generation":reply.AgentCredentialGeneration++
			case "process":reply.Observation.Witness.Generation.InvocationID=reply.Observation.Witness.PreviousGeneration.InvocationID
			case "agent_identity":reply.Observation.AgentID=reply.ObservationID
			case "server_identity":reply.Observation.ServerID=reply.ObservationID
			case "observed_at":reply.ObservedAt=reply.ObservedAt.Add(-5*time.Minute)
			}
			verifier:=recoveryobserve.NewVerifier(f.pool)
			if _,err:=verifier.RecordOneReply(f.ctx,id,nonce,principal,reply);!errors.Is(err,recoveryobserve.ErrObservationRefused){
				t.Fatalf("invalid reply not refused: %v",err)
			}
			nonceAuditState(t,f,id,"reply_rejected",2)
			if _,err:=verifier.RecordOneReply(f.ctx,id,nonce,principal,reply);!errors.Is(err,recoveryobserve.ErrObservationRefused){
				t.Fatalf("rejected nonce was reused: %v",err)
			}
		})
	}
}

func TestRuntimeRecoveryObservationNonceParallelConsumptionExactlyOne(t *testing.T) {
	f,id,nonce,principal,reply:=observationFixture(t,30*time.Second)
	defer f.done()
	ctx,cancel:=context.WithTimeout(f.ctx,12*time.Second)
	defer cancel()
	start:=make(chan struct{})
	outputs:=make(chan error,2)
	var wg sync.WaitGroup
	for k:=0;k<2;k++{
		wg.Add(1)
		go func(){
			defer wg.Done()
			<-start
			_,err:=recoveryobserve.NewVerifier(f.pool).RecordOneReply(ctx,id,nonce,principal,reply)
			outputs<-err
		}()
	}
	close(start);wg.Wait();close(outputs)
	success,denied:=0,0
	for err:=range outputs{
		if err==nil{success++}else if errors.Is(err,recoveryobserve.ErrObservationRefused){denied++}else{
			t.Fatalf("unexpected concurrent DB failure: %v",err)
		}
	}
	if success!=1||denied!=1{t.Fatalf("nonce replay admitted: success=%d denied=%d",success,denied)}
	nonceAuditState(t,f,id,"reply_matched_unattested",2)
}

func TestRuntimeRecoveryObservationNonceExpiryAndImmutableAudit(t *testing.T) {
	f,id,nonce,principal,reply:=observationFixture(t,700*time.Millisecond)
	defer f.done()
	time.Sleep(1*time.Second)
	if _,err:=recoveryobserve.NewVerifier(f.pool).RecordOneReply(f.ctx,id,nonce,principal,reply);!errors.Is(err,recoveryobserve.ErrObservationRefused) {
		t.Fatalf("expired nonce accepted: %v",err)
	}
	nonceAuditState(t,f,id,"issued",1)
	_,err:=f.pool.Exec(f.ctx,`DELETE FROM vpn_runtime_recovery_observation_nonces WHERE id=$1::uuid`,id)
	requireNonceGuard(t,err)
	_,err=f.pool.Exec(f.ctx,`UPDATE vpn_runtime_recovery_observation_nonce_events SET action='issued' WHERE observation_id=$1::uuid`,id)
	requireNonceGuard(t,err)
	_,err=f.pool.Exec(f.ctx,`TRUNCATE vpn_runtime_recovery_observation_nonces CASCADE`)
	requireNonceGuard(t,err)
	_,err=f.pool.Exec(f.ctx,`TRUNCATE vpn_runtime_recovery_observation_nonce_events`)
	requireNonceGuard(t,err)
	down,err:=os.ReadFile("../../migrations/000165_runtime_recovery_observation_nonce.down.sql")
	if err!=nil{t.Fatal(err)}
	if _,err=f.pool.Exec(f.ctx,string(down));err==nil{
		t.Fatal("downgrade silently erased retained nonce history")
	}
	nonceAuditState(t,f,id,"issued",1)
}

func TestRuntimeRecoveryObservationNonceAuditFailureRollsBackResponse(t *testing.T) {
	f,id,nonce,principal,reply:=observationFixture(t,30*time.Second)
	defer f.done()
	_,err:=f.pool.Exec(f.ctx,`
CREATE FUNCTION rg140_reject_observation_reply_event()
RETURNS trigger LANGUAGE plpgsql AS
'BEGIN IF NEW.action = ''reply_matched_unattested'' THEN
 RAISE EXCEPTION ''injected recovery observation audit failure'';
 END IF; RETURN NEW; END';
`)
	if err!=nil{t.Fatal(err)}
	_,err=f.pool.Exec(f.ctx,`
CREATE TRIGGER rg140_reject_observation_reply_event
BEFORE INSERT ON vpn_runtime_recovery_observation_nonce_events
FOR EACH ROW EXECUTE FUNCTION rg140_reject_observation_reply_event();
`)
	if err!=nil{t.Fatal(err)}
	if _,err:=recoveryobserve.NewVerifier(f.pool).RecordOneReply(f.ctx,id,nonce,principal,reply);err==nil {
		t.Fatal("nonce recorded without durable audit event")
	}
	nonceAuditState(t,f,id,"issued",1)
}

func TestRuntimeRecoveryObservationNonceRejectsWrongAttemptAndDuplicateRequest(t *testing.T) {
	f,id,_,_,_:=observationFixture(t,30*time.Second)
	defer f.done()
	_,err:=f.pool.Exec(f.ctx,`
INSERT INTO vpn_runtime_recovery_observation_nonces (
 attempt_id,reservation_id,server_id,agent_id,task_id,attempt_epoch,
 agent_credential_generation,expected_marker_sha256,expected_receipt_sha256,
 expected_runtime_witness_sha256,nonce_sha256,issued_at,expires_at
)
SELECT attempt_id,reservation_id,server_id,agent_id,task_id,attempt_epoch+1,
       agent_credential_generation,expected_marker_sha256,expected_receipt_sha256,
       expected_runtime_witness_sha256,repeat('1',64),clock_timestamp(),
       clock_timestamp()+interval '30 seconds'
FROM vpn_runtime_recovery_observation_nonces WHERE id=$1::uuid
`,id)
	requireNonceGuard(t,err)

	// A second otherwise correct request for the same durable attempt must
	// not mint a fresh nonce, even if the first one expires or is rejected.
	_,err=f.pool.Exec(f.ctx,`
INSERT INTO vpn_runtime_recovery_observation_nonces (
 attempt_id,reservation_id,server_id,agent_id,task_id,attempt_epoch,
 agent_credential_generation,expected_marker_sha256,expected_receipt_sha256,
 expected_runtime_witness_sha256,nonce_sha256,issued_at,expires_at
)
SELECT attempt_id,reservation_id,server_id,agent_id,task_id,attempt_epoch,
       agent_credential_generation,expected_marker_sha256,expected_receipt_sha256,
       expected_runtime_witness_sha256,repeat('2',64),clock_timestamp(),
       clock_timestamp()+interval '30 seconds'
FROM vpn_runtime_recovery_observation_nonces WHERE id=$1::uuid
`,id)
	var pg *pgconn.PgError
	if !errors.As(err,&pg)||pg.Code!="23505" {
		t.Fatalf("second nonce for one attempt was admitted: %v",err)
	}
	nonceAuditState(t,f,id,"issued",1)
}
