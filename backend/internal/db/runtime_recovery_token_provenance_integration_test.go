package db

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/ikaevus/routegate/backend/internal/recoveryobserve"
)

func tokenBoundObservationFixture(t *testing.T,expiry time.Duration) (
	recoveryLedgerFixture,string,string,recoveryobserve.TokenBoundEvidenceV1,
){
	t.Helper()
	f,id,nonce,_,reply:=observationFixture(t,expiry)
	witnessJSON,err:=json.Marshal(reply.Observation.Witness)
	if err!=nil{f.done();t.Fatal(err)}
	claim:=recoveryobserve.TokenBoundEvidenceV1{
		SchemaVersion:1,ObservationID:id,ReservationID:reply.ReservationID,
		AttemptID:reply.AttemptID,AttemptEpoch:reply.AttemptEpoch,
		Nonce:nonce,AgentCredentialGeneration:reply.AgentCredentialGeneration,
		Evidence:recoveryobserve.EvidenceDigestsV1{
			SchemaVersion:1,TaskID:reply.Observation.TaskID,
			AgentID:reply.Observation.AgentID,ServerID:reply.Observation.ServerID,
			MarkerSHA256:reply.Observation.MarkerHash,
			ReceiptSHA256:reply.Observation.ReceiptHash,
			EnvelopeSHA256:reply.Observation.EnvelopeHash,
			WitnessSHA256:nonceHash(witnessJSON),
		},
	}
	mac,err:=recoveryobserve.TokenProofMACV1(recoveryFixtureAgentToken,claim)
	if err!=nil{f.done();t.Fatal(err)}
	return f,id,mac,claim
}

func TestRuntimeRecoveryTokenProvenanceCurrentAgentCanMatchPendingNonce(t *testing.T){
	f,id,mac,claim:=tokenBoundObservationFixture(t,30*time.Second)
	defer f.done()
	if err:=recoveryobserve.VerifyCurrentAgentTokenEvidenceV1(
		f.ctx,f.pool,recoveryFixtureAgentToken,mac,claim,
	);err!=nil{t.Fatal(err)}
	// This is a READ-ONLY preflight. It must not consume the nonce or
	// change an attempt/reservation, authorize a runtime release, or
	// produce a new task.
	nonceAuditState(t,f,id,"issued",1)
	var version int64
	if err:=f.pool.QueryRow(f.ctx,
		`SELECT credential_generation FROM agents WHERE id=$1::uuid`,
		f.agentID).Scan(&version);err!=nil||version!=f.generation{
		t.Fatalf("Agent generation changed during read-only check: %d %v",version,err)
	}
}

func TestRuntimeRecoveryTokenProvenanceRefusesWrongBearerAndClaim(t *testing.T){
	for name,change:=range map[string]func(*recoveryobserve.TokenBoundEvidenceV1){
		"wrong_reservation":func(c *recoveryobserve.TokenBoundEvidenceV1){c.ReservationID=c.AttemptID},
		"wrong_attempt":func(c *recoveryobserve.TokenBoundEvidenceV1){c.AttemptID=c.ReservationID},
		"wrong_observation":func(c *recoveryobserve.TokenBoundEvidenceV1){c.ObservationID=c.AttemptID},
		"wrong_nonce":func(c *recoveryobserve.TokenBoundEvidenceV1){
			c.Nonce=base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{0x11},32))
		},
		"wrong_epoch":func(c *recoveryobserve.TokenBoundEvidenceV1){c.AttemptEpoch++},
		"wrong_generation":func(c *recoveryobserve.TokenBoundEvidenceV1){c.AgentCredentialGeneration++},
		"wrong_agent":func(c *recoveryobserve.TokenBoundEvidenceV1){c.Evidence.AgentID=c.Evidence.ServerID},
		"wrong_server":func(c *recoveryobserve.TokenBoundEvidenceV1){c.Evidence.ServerID=c.Evidence.AgentID},
		"wrong_task":func(c *recoveryobserve.TokenBoundEvidenceV1){c.Evidence.TaskID=c.AttemptID},
		"wrong_marker":func(c *recoveryobserve.TokenBoundEvidenceV1){c.Evidence.MarkerSHA256=strings.Repeat("1",64)},
		"wrong_receipt":func(c *recoveryobserve.TokenBoundEvidenceV1){c.Evidence.ReceiptSHA256=strings.Repeat("1",64)},
		"wrong_witness":func(c *recoveryobserve.TokenBoundEvidenceV1){c.Evidence.WitnessSHA256=strings.Repeat("1",64)},
	}{
		t.Run(name,func(t *testing.T){
			f,id,_,claim:=tokenBoundObservationFixture(t,30*time.Second)
			defer f.done()
			change(&claim)
			// The token holder is deliberately allowed to recompute a
			// valid HMAC over malicious data: PostgreSQL MUST still refuse.
			mac,err:=recoveryobserve.TokenProofMACV1(recoveryFixtureAgentToken,claim)
			if err!=nil{t.Fatal(err)}
			err=recoveryobserve.VerifyCurrentAgentTokenEvidenceV1(f.ctx,f.pool,recoveryFixtureAgentToken,mac,claim)
			if !errors.Is(err,recoveryobserve.ErrTokenBoundEvidenceRefused){
				t.Fatalf("Agent-token holder bypassed immutable DB binding: %v",err)
			}
			nonceAuditState(t,f,id,"issued",1)
		})
	}
	t.Run("foreign_valid_bearer",func(t *testing.T){
		f,id,_,claim:=tokenBoundObservationFixture(t,30*time.Second)
		defer f.done()
		other:="rg_agent_"+base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{0x81},32))
		mac,err:=recoveryobserve.TokenProofMACV1(other,claim)
		if err!=nil{t.Fatal(err)}
		err=recoveryobserve.VerifyCurrentAgentTokenEvidenceV1(f.ctx,f.pool,other,mac,claim)
		if !errors.Is(err,recoveryobserve.ErrTokenBoundEvidenceRefused){
			t.Fatalf("unregistered Agent bearer accepted: %v",err)
		}
		nonceAuditState(t,f,id,"issued",1)
	})
	t.Run("tampered_mac",func(t *testing.T){
		f,id,mac,claim:=tokenBoundObservationFixture(t,30*time.Second)
		defer f.done()
		mac=strings.Repeat("a",64)
		if err:=recoveryobserve.VerifyCurrentAgentTokenEvidenceV1(f.ctx,f.pool,recoveryFixtureAgentToken,mac,claim);
			!errors.Is(err,recoveryobserve.ErrTokenBoundEvidenceRefused) {
			t.Fatalf("untrusted HMAC accepted: %v",err)
		}
		nonceAuditState(t,f,id,"issued",1)
	})
}

func TestRuntimeRecoveryTokenProvenanceRejectsExpiredOrResolvedNonce(t *testing.T){
	t.Run("expired",func(t *testing.T){
		f,id,mac,claim:=tokenBoundObservationFixture(t,550*time.Millisecond)
		defer f.done()
		time.Sleep(800*time.Millisecond)
		err:=recoveryobserve.VerifyCurrentAgentTokenEvidenceV1(f.ctx,f.pool,recoveryFixtureAgentToken,mac,claim)
		if !errors.Is(err,recoveryobserve.ErrTokenBoundEvidenceRefused){
			t.Fatalf("expired observation bound to token: %v",err)
		}
		nonceAuditState(t,f,id,"issued",1)
	})
	t.Run("already_resolved",func(t *testing.T){
		f,id,mac,claim:=tokenBoundObservationFixture(t,30*time.Second)
		defer f.done()
		// Consume a correct nonce through the existing isolated verifier:
		// it is *not* an HTTP route or a release signal.
		var state string
		err:=f.pool.QueryRow(f.ctx,`SELECT state FROM vpn_runtime_recovery_observation_nonces WHERE id=$1::uuid`,id).Scan(&state)
		if err!=nil||state!="issued"{t.Fatal("expected issued fixture",err)}
		if _,err=f.pool.Exec(f.ctx,`
UPDATE vpn_runtime_recovery_observation_nonces
 SET state='reply_rejected',resolved_at=clock_timestamp(),response_sha256=repeat('a',64)
 WHERE id=$1::uuid AND state='issued'
`,id);err!=nil{t.Fatal(err)}
		err=recoveryobserve.VerifyCurrentAgentTokenEvidenceV1(f.ctx,f.pool,recoveryFixtureAgentToken,mac,claim)
		if !errors.Is(err,recoveryobserve.ErrTokenBoundEvidenceRefused){
			t.Fatalf("already-resolved nonce was accepted: %v",err)
		}
		nonceAuditState(t,f,id,"reply_rejected",2)
	})
}

// Token-bound evidence may not be passed directly to a future runtime unlock.
// A valid preflight is inherently stale once its read lock is released.
// An authenticated nonce-consumption transaction and fresh under-lock Agent
// re-observation must be built in a separate reviewed recovery protocol.
