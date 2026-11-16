package recoveryobserve

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const recoveryEvidenceProofDomainV1 = "routegate/rg140/agent-token-bound-evidence/v1"

// ErrTokenBoundEvidenceRefused is intentionally generic: callers cannot
// distinguish bad token, wrong Agent, stale reservation, nonce or digest.
var ErrTokenBoundEvidenceRefused = errors.New("agent token-bound evidence refused")

// TokenBoundEvidenceV1 mirrors the Agent v1 JSON wire contract verbatim.
// It has NO direct source-trust or Agent-runtime attestation semantics.
type TokenBoundEvidenceV1 struct {
	SchemaVersion             int               `json:"schemaVersion"`
	ObservationID             string            `json:"observationId"`
	ReservationID             string            `json:"reservationId"`
	AttemptID                 string            `json:"attemptId"`
	AttemptEpoch              int64             `json:"attemptEpoch"`
	Nonce                     string            `json:"nonce"`
	AgentCredentialGeneration int64             `json:"agentCredentialGeneration"`
	Evidence                  EvidenceDigestsV1 `json:"evidence"`
}

func validAgentBearerV1(token string) bool {
	if !strings.HasPrefix(token, "rg_agent_") || len(token) != len("rg_agent_")+43 {
		return false
	}
	suffix := strings.TrimPrefix(token, "rg_agent_")
	raw, err := base64.RawURLEncoding.DecodeString(suffix)
	return err == nil && len(raw) == 32 && base64.RawURLEncoding.EncodeToString(raw) == suffix
}

func validTokenBoundEvidenceV1(p TokenBoundEvidenceV1) bool {
	e:=p.Evidence
	if p.SchemaVersion!=1 || e.SchemaVersion!=1 ||
		!canonicalUUID.MatchString(p.ObservationID) ||
		!canonicalUUID.MatchString(p.ReservationID) ||
		!canonicalUUID.MatchString(p.AttemptID) ||
		!canonicalUUID.MatchString(e.TaskID) ||
		!canonicalUUID.MatchString(e.AgentID) ||
		!canonicalUUID.MatchString(e.ServerID) ||
		p.AttemptEpoch<=0 || p.AgentCredentialGeneration<=0 ||
		!canonicalHash.MatchString(e.MarkerSHA256) ||
		!canonicalHash.MatchString(e.ReceiptSHA256) ||
		!canonicalHash.MatchString(e.EnvelopeSHA256) ||
		!canonicalHash.MatchString(e.WitnessSHA256){
		return false
	}
	raw,err:=base64.RawURLEncoding.DecodeString(p.Nonce)
	return err==nil && len(raw)==32 && base64.RawURLEncoding.EncodeToString(raw)==p.Nonce
}

// TokenProofMACV1 is a transport-independent HMAC format verifier.
// It is NOT Agent authentication on its own: the same bearer token must be
// independently compared against the active Agent row in PostgreSQL.
func TokenProofMACV1(token string,p TokenBoundEvidenceV1)(string,error){
	if !validAgentBearerV1(token)||!validTokenBoundEvidenceV1(p){
		return "",ErrTokenBoundEvidenceRefused
	}
	raw,err:=json.Marshal(p)
	if err!=nil{return "",ErrTokenBoundEvidenceRefused}
	mac:=hmac.New(sha256.New,[]byte(token))
	_,_=mac.Write([]byte(recoveryEvidenceProofDomainV1))
	_,_=mac.Write([]byte{0})
	_,_=mac.Write(raw)
	return hex.EncodeToString(mac.Sum(nil)),nil
}

// VerifyCurrentAgentTokenEvidenceV1 is a READ-ONLY database preflight.
// It binds one token holder to an Agent/Server ID, the SAME current
// Agent credential generation, pending immutable recovery attempt and
// one currently issued short-lived observation nonce.
//
// It NEVER consumes a nonce, authenticates the host OS/process, approves
// an operator, or releases mutation-inflight.json. A later authenticated
// transport must re-check all of this inside its actual nonce-consumption
// transaction: a successful read-only preflight immediately becomes stale.
func VerifyCurrentAgentTokenEvidenceV1(ctx context.Context,pool *pgxpool.Pool,token,claimedMAC string,p TokenBoundEvidenceV1)error{
	if pool==nil || ctx.Err()!=nil || !validAgentBearerV1(token) ||
		!validTokenBoundEvidenceV1(p) || !canonicalHash.MatchString(claimedMAC){
		return ErrTokenBoundEvidenceRefused
	}
	expectedMAC,err:=TokenProofMACV1(token,p)
	if err!=nil || subtle.ConstantTimeCompare([]byte(expectedMAC),[]byte(claimedMAC))!=1{
		return ErrTokenBoundEvidenceRefused
	}
	tokenDigest:=sha256.Sum256([]byte(token))
	nonceRaw,_:=base64.RawURLEncoding.DecodeString(p.Nonce)
	nonceDigest:=sha256.Sum256(nonceRaw)
	tx,err:=pool.Begin(ctx)
	if err!=nil{return fmt.Errorf("begin token-bound evidence preflight: %w",err)}
	defer tx.Rollback(ctx)

	var db struct{
		observID,attemptID,reservationID,agentID,serverID,taskID string
		nonceSHA,markerSHA,receiptSHA,witnessSHA string
		agentTokenSHA,state,attemptState string
		epoch,generation,currentGeneration int64
		fenced bool
		issued,expires,now time.Time
	}
	err=tx.QueryRow(ctx,`
SELECT n.id::text,n.attempt_id::text,n.reservation_id::text,
       n.agent_id::text,n.server_id::text,n.task_id::text,
       n.nonce_sha256,n.expected_marker_sha256,
       n.expected_receipt_sha256,n.expected_runtime_witness_sha256,
       a.token_hash,n.state,p.state,
       n.attempt_epoch,n.agent_credential_generation,a.credential_generation,
       a.capabilities @> '{"runtimeMutationFencingV1":true}'::jsonb,
       n.issued_at,n.expires_at,clock_timestamp()
  FROM vpn_runtime_recovery_observation_nonces n
  JOIN vpn_runtime_recovery_attempts p ON p.id=n.attempt_id
     AND p.reservation_id=n.reservation_id AND p.server_id=n.server_id
     AND p.agent_id=n.agent_id AND p.attempt_epoch=n.attempt_epoch
  JOIN agents a ON a.id=n.agent_id AND a.server_id=n.server_id
 WHERE n.id=$1::uuid AND a.token_hash=$2
 FOR SHARE OF n,p,a
`,p.ObservationID,hex.EncodeToString(tokenDigest[:])).Scan(
		&db.observID,&db.attemptID,&db.reservationID,&db.agentID,&db.serverID,
		&db.taskID,&db.nonceSHA,&db.markerSHA,&db.receiptSHA,&db.witnessSHA,
		&db.agentTokenSHA,&db.state,&db.attemptState,
		&db.epoch,&db.generation,&db.currentGeneration,
		&db.fenced,&db.issued,&db.expires,&db.now)
	if errors.Is(err,pgx.ErrNoRows){return ErrTokenBoundEvidenceRefused}
	if err!=nil{return fmt.Errorf("check current token-bound evidence: %w",err)}
	matches:=db.state=="issued" && db.attemptState=="awaiting_fresh_observation" &&
		db.fenced && db.currentGeneration==db.generation &&
		db.observID==p.ObservationID && db.attemptID==p.AttemptID &&
		db.reservationID==p.ReservationID && db.agentID==p.Evidence.AgentID &&
		db.serverID==p.Evidence.ServerID && db.taskID==p.Evidence.TaskID &&
		db.epoch==p.AttemptEpoch && db.generation==p.AgentCredentialGeneration &&
		db.now.After(db.issued) && db.now.Before(db.expires) &&
		subtle.ConstantTimeCompare([]byte(db.agentTokenSHA),[]byte(hex.EncodeToString(tokenDigest[:])))==1 &&
		subtle.ConstantTimeCompare([]byte(db.nonceSHA),[]byte(hex.EncodeToString(nonceDigest[:])))==1 &&
		subtle.ConstantTimeCompare([]byte(db.markerSHA),[]byte(p.Evidence.MarkerSHA256))==1 &&
		subtle.ConstantTimeCompare([]byte(db.receiptSHA),[]byte(p.Evidence.ReceiptSHA256))==1 &&
		subtle.ConstantTimeCompare([]byte(db.witnessSHA),[]byte(p.Evidence.WitnessSHA256))==1
	if !matches{return ErrTokenBoundEvidenceRefused}
	if err=tx.Commit(ctx);err!=nil{return fmt.Errorf("end token-bound evidence preflight: %w",err)}
	return nil
}
