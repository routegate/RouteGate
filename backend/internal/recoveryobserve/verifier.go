// Package recoveryobserve stages read-only Manager checks for Agent runtime
// evidence. This package has NO HTTP route and NO runtime-fence release API.
package recoveryobserve

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var ErrObservationRefused = errors.New("recovery observation refused")

var (
	canonicalUUID = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
	canonicalHash = regexp.MustCompile(`^[0-9a-f]{64}$`)
	canonicalInvocation = regexp.MustCompile(`^[0-9a-f]{32}$`)
)

type AgentTransportIdentity struct {
	AgentID string
	ServerID string
}

// This is the JSON v1 schema of the Agent's read-only reply. It is deliberately
// duplicated across separate Go modules: an explicit wire contract is safer
// than importing an Agent runtime implementation into Manager.
type RuntimeGeneration struct {
	InvocationID string `json:"invocationId"`
	PID int `json:"pid"`
	StartedMonotonic string `json:"startedMonotonic"`
}
type RuntimeWitness struct {
	SchemaVersion int `json:"schemaVersion"`
	RuntimeHash string `json:"runtimeHash"`
	Generation RuntimeGeneration `json:"generation"`
	PreviousGeneration RuntimeGeneration `json:"previousGeneration"`
	ListenerPort int `json:"listenerPort"`
}
type RuntimeObservation struct {
	SchemaVersion int `json:"schemaVersion"`
	TaskID string `json:"taskId"`
	AgentID string `json:"agentId"`
	ServerID string `json:"serverId"`
	MarkerHash string `json:"markerHash"`
	ReceiptHash string `json:"receiptHash"`
	EnvelopeHash string `json:"envelopeHash"`
	Witness RuntimeWitness `json:"witness"`
}
type ObservationReply struct {
	SchemaVersion int `json:"schemaVersion"`
	ObservationID string `json:"observationId"`
	ReservationID string `json:"reservationId"`
	AttemptID string `json:"attemptId"`
	AttemptEpoch int64 `json:"attemptEpoch"`
	Nonce string `json:"nonce"`
	AgentCredentialGeneration int64 `json:"agentCredentialGeneration"`
	ObservedAt time.Time `json:"observedAt"`
	Observation RuntimeObservation `json:"observation"`
}

// RecordedReply is a durable fact about receipt/format matching ONLY.
// A matching echo is NOT Agent authentication, attestation, revocation,
// authorization, or a reason to clear mutation-inflight.json.
type RecordedReply struct {
	ObservationID string
	State string
}

type Verifier struct { pool *pgxpool.Pool }
func NewVerifier(pool *pgxpool.Pool) *Verifier { return &Verifier{pool:pool} }

type expectedRow struct {
	state,attemptID,reservationID,agentID,serverID,taskID string
	expectedMarker,expectedReceipt,expectedWitness,nonceHash string
	attemptEpoch,generation,currentGeneration int64
	fenced bool
	issuedAt,expiresAt,now time.Time
}

func validGeneration(v RuntimeGeneration) bool {
	n,err:=strconv.ParseUint(v.StartedMonotonic,10,64)
	return canonicalInvocation.MatchString(v.InvocationID) &&
		v.InvocationID != strings.Repeat("0",32) && v.PID>0 && err==nil && n>0
}
func matchesReply(reply ObservationReply, nonce string, principal AgentTransportIdentity, row expectedRow) bool {
	o:=reply.Observation
	w:=o.Witness
	if reply.SchemaVersion!=1 || o.SchemaVersion!=1 || w.SchemaVersion!=1 ||
		reply.ObservationID=="" || reply.ReservationID!=row.reservationID ||
		reply.AttemptID!=row.attemptID || reply.AttemptEpoch!=row.attemptEpoch ||
		reply.Nonce!=nonce || reply.AgentCredentialGeneration!=row.generation ||
		o.TaskID!=row.taskID || o.AgentID!=row.agentID || o.ServerID!=row.serverID ||
		principal.AgentID!=row.agentID || principal.ServerID!=row.serverID ||
		o.MarkerHash!=row.expectedMarker || o.ReceiptHash!=row.expectedReceipt ||
		!canonicalHash.MatchString(o.EnvelopeHash) ||
		!canonicalHash.MatchString(w.RuntimeHash) ||
		!validGeneration(w.Generation) || !validGeneration(w.PreviousGeneration) ||
		w.Generation.InvocationID==w.PreviousGeneration.InvocationID ||
		w.ListenerPort<1 || w.ListenerPort>65535 ||
		reply.ObservedAt.Before(row.issuedAt.Add(-2*time.Second)) ||
		!reply.ObservedAt.Before(row.expiresAt) ||
		reply.ObservedAt.After(row.now.Add(2*time.Second)) {
		return false
	}
	payload,err:=json.Marshal(w)
	if err!=nil { return false }
	digest:=sha256.Sum256(payload)
	return subtle.ConstantTimeCompare([]byte(hex.EncodeToString(digest[:])),[]byte(row.expectedWitness))==1
}

// RecordOneReply consumes one previously recorded nonce. Only a transport
// adapter that ALREADY authenticated the Agent may supply principal. There
// is currently NO such caller; this is an offline verification primitive.
//
// When the correct nonce is presented, even an invalid or stale response
// becomes a durable one-time refusal. It cannot be replayed to obtain a
// positive match. A wrong nonce does not consume anything.
//
// A matched reply remains "reply_matched_unattested" because neither the
// nonce echo nor these hashes cryptographically authenticate a remote Agent.
func (v *Verifier) RecordOneReply(ctx context.Context, id, nonce string, principal AgentTransportIdentity, reply ObservationReply) (RecordedReply,error) {
	if v==nil || v.pool==nil || !canonicalUUID.MatchString(id) ||
		!canonicalUUID.MatchString(principal.AgentID) ||
		!canonicalUUID.MatchString(principal.ServerID) {
		return RecordedReply{},ErrObservationRefused
	}
	rawNonce,err:=base64.RawURLEncoding.DecodeString(nonce)
	if err!=nil || len(rawNonce)!=32 || base64.RawURLEncoding.EncodeToString(rawNonce)!=nonce{
		return RecordedReply{},ErrObservationRefused
	}
	nonceDigest:=sha256.Sum256(rawNonce)
	tx,err:=v.pool.Begin(ctx)
	if err!=nil{return RecordedReply{},fmt.Errorf("begin observation result: %w",err)}
	defer tx.Rollback(ctx)
	var row expectedRow
	err=tx.QueryRow(ctx,`
SELECT n.state,n.attempt_id::text,n.reservation_id::text,n.agent_id::text,
       n.server_id::text,n.task_id::text,n.attempt_epoch,n.agent_credential_generation,
       n.expected_marker_sha256,n.expected_receipt_sha256,
       n.expected_runtime_witness_sha256,n.nonce_sha256,
       n.issued_at,n.expires_at,a.credential_generation,
       a.capabilities @> '{"runtimeMutationFencingV1":true}'::jsonb,
       clock_timestamp()
FROM vpn_runtime_recovery_observation_nonces n
JOIN agents a ON a.id=n.agent_id AND a.server_id=n.server_id
WHERE n.id=$1::uuid
FOR UPDATE OF n
`,id).Scan(&row.state,&row.attemptID,&row.reservationID,&row.agentID,
		&row.serverID,&row.taskID,&row.attemptEpoch,&row.generation,
		&row.expectedMarker,&row.expectedReceipt,&row.expectedWitness,
		&row.nonceHash,&row.issuedAt,&row.expiresAt,&row.currentGeneration,
		&row.fenced,&row.now)
	if errors.Is(err,pgx.ErrNoRows){return RecordedReply{},ErrObservationRefused}
	if err!=nil{return RecordedReply{},fmt.Errorf("load recovery observation nonce: %w",err)}
	if row.state!="issued" || !row.fenced || row.currentGeneration!=row.generation ||
		row.now.Before(row.issuedAt) || !row.now.Before(row.expiresAt) ||
		principal.AgentID!=row.agentID || principal.ServerID!=row.serverID ||
		subtle.ConstantTimeCompare([]byte(hex.EncodeToString(nonceDigest[:])),[]byte(row.nonceHash))!=1 {
		return RecordedReply{},ErrObservationRefused
	}
	valid:=reply.ObservationID==id && matchesReply(reply,nonce,principal,row)
	state:="reply_rejected"
	if valid{state="reply_matched_unattested"}
	responseData,err:=json.Marshal(reply)
	if err!=nil{return RecordedReply{},ErrObservationRefused}
	responseHash:=sha256.Sum256(responseData)
	tag,err:=tx.Exec(ctx,`
UPDATE vpn_runtime_recovery_observation_nonces
SET state=$2,resolved_at=clock_timestamp(),response_sha256=$3
WHERE id=$1::uuid AND state='issued'
  AND nonce_sha256=$4 AND agent_id=$5::uuid AND server_id=$6::uuid
  AND agent_credential_generation=$7 AND clock_timestamp()<expires_at
`,id,state,hex.EncodeToString(responseHash[:]),row.nonceHash,
		principal.AgentID,principal.ServerID,row.generation)
	if err!=nil {return RecordedReply{},fmt.Errorf("persist observation response: %w",err)}
	if tag.RowsAffected()!=1 {return RecordedReply{},ErrObservationRefused}
	if err=tx.Commit(ctx);err!=nil{return RecordedReply{},fmt.Errorf("commit observation response: %w",err)}
	if !valid{return RecordedReply{},ErrObservationRefused}
	return RecordedReply{ObservationID:id,State:state},nil
}
