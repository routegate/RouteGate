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
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// AuthenticatedReplyConsumer is strictly a private Manager database primitive.
// There is no HTTP handler, nonce issuer, Agent dispatcher, or recovery
// unlock caller. Successful matching is explicitly NOT remote attestation.
type AuthenticatedReplyConsumer struct { pool *pgxpool.Pool }

func NewAuthenticatedReplyConsumer(pool *pgxpool.Pool) *AuthenticatedReplyConsumer {
	return &AuthenticatedReplyConsumer{pool:pool}
}

// ConsumeTokenBoundReplyV1 performs the *current* Agent-token check, nonce
// validation, pending-attempt binding, read-only runtime-response format
// comparison, one-time state transition AND append-only audit in one DB
// transaction. Unlike a separate read-only preflight followed by
// Verifier.RecordOneReply, no authentication decision crosses a commit.
//
// The caller must source token from trusted Agent bearer middleware, not
// from an arbitrary JSON field. This method has no network-facing caller.
// A holder of a stolen token may fabricate claims. Even a matched result
// remains "reply_matched_unattested"; it MUST NOT release an Agent fence.
//
// Incorrect bearer/MAC/nonces or claims inconsistent with the DB leave the
// challenge issued (no DoS via guessed bearer). Once a current valid bearer
// and exact HMAC+nonce+attempt have been verified, a malformed read-only
// reply is permanently recorded as reply_rejected. Its plaintext is never
// stored; a mandatory audit trigger participates in the same transaction.
func (c *AuthenticatedReplyConsumer) ConsumeTokenBoundReplyV1(
	ctx context.Context, token, claimedMAC string,
	claim TokenBoundEvidenceV1, reply ObservationReply,
) (RecordedReply,error) {
	denied:=func()(RecordedReply,error){return RecordedReply{},ErrTokenBoundEvidenceRefused}
	if c==nil || c.pool==nil || ctx.Err()!=nil ||
		!validAgentBearerV1(token) || !validTokenBoundEvidenceV1(claim) ||
		!canonicalHash.MatchString(claimedMAC) {
		return denied()
	}
	expectedMAC,err:=TokenProofMACV1(token,claim)
	if err!=nil || subtle.ConstantTimeCompare([]byte(expectedMAC),[]byte(claimedMAC))!=1 {
		return denied()
	}
	rawNonce,err:=base64.RawURLEncoding.DecodeString(claim.Nonce)
	if err!=nil || len(rawNonce)!=32 {return denied()}
	nonceDigest:=sha256.Sum256(rawNonce)
	tokenDigest:=sha256.Sum256([]byte(token))
	tokenHash:=hex.EncodeToString(tokenDigest[:])
	nonceHash:=hex.EncodeToString(nonceDigest[:])

	tx,err:=c.pool.Begin(ctx)
	if err!=nil{return RecordedReply{},fmt.Errorf("begin authenticated observation consume: %w",err)}
	defer tx.Rollback(ctx)

	// Critical lock boundary: the current Agent token/generation, immutable
	// attempt and one-use nonce are read under locks in the SAME transaction
	// that commits the reply. No optimistic preflight result is trusted.
	var row expectedRow
	var attemptState,tokenHashInDB string
	var attemptGeneration int64
	err=tx.QueryRow(ctx,`
SELECT n.state,n.attempt_id::text,n.reservation_id::text,n.agent_id::text,
       n.server_id::text,n.task_id::text,n.attempt_epoch,n.agent_credential_generation,
       n.expected_marker_sha256,n.expected_receipt_sha256,
       n.expected_runtime_witness_sha256,n.nonce_sha256,
       n.issued_at,n.expires_at,a.credential_generation,
       a.capabilities @> '{"runtimeMutationFencingV1":true}'::jsonb,
       clock_timestamp(),p.state,a.token_hash,p.agent_credential_generation
FROM vpn_runtime_recovery_observation_nonces n
JOIN vpn_runtime_recovery_attempts p ON p.id=n.attempt_id
 AND p.reservation_id=n.reservation_id AND p.server_id=n.server_id
 AND p.agent_id=n.agent_id AND p.attempt_epoch=n.attempt_epoch
 AND p.job_id=n.task_id
JOIN vpn_runtime_recovery_reservations r ON r.id=p.reservation_id
 AND r.server_id=p.server_id AND r.agent_id=p.agent_id
 AND r.job_id=p.job_id AND r.agent_credential_generation=p.agent_credential_generation
JOIN agents a ON a.id=n.agent_id AND a.server_id=n.server_id
WHERE n.id=$1::uuid AND a.token_hash=$2
FOR UPDATE OF n
FOR SHARE OF a
FOR KEY SHARE OF p,r
`,claim.ObservationID,tokenHash).Scan(
		&row.state,&row.attemptID,&row.reservationID,&row.agentID,
		&row.serverID,&row.taskID,&row.attemptEpoch,&row.generation,
		&row.expectedMarker,&row.expectedReceipt,&row.expectedWitness,
		&row.nonceHash,&row.issuedAt,&row.expiresAt,&row.currentGeneration,
		&row.fenced,&row.now,&attemptState,&tokenHashInDB,&attemptGeneration,
	)
	if errors.Is(err,pgx.ErrNoRows){return denied()}
	if err!=nil{return RecordedReply{},fmt.Errorf("load authenticated observation state: %w",err)}
	if row.state!="issued" || attemptState!="awaiting_fresh_observation" ||
		!row.fenced || row.generation!=row.currentGeneration ||
		row.generation!=attemptGeneration || row.agentID!=claim.Evidence.AgentID ||
		row.serverID!=claim.Evidence.ServerID || row.taskID!=claim.Evidence.TaskID ||
		row.reservationID!=claim.ReservationID || row.attemptID!=claim.AttemptID ||
		row.attemptEpoch!=claim.AttemptEpoch ||
		row.generation!=claim.AgentCredentialGeneration ||
		row.now.Before(row.issuedAt) || !row.now.Before(row.expiresAt) ||
		subtle.ConstantTimeCompare([]byte(tokenHashInDB),[]byte(tokenHash))!=1 ||
		subtle.ConstantTimeCompare([]byte(row.nonceHash),[]byte(nonceHash))!=1 ||
		subtle.ConstantTimeCompare([]byte(row.expectedMarker),[]byte(claim.Evidence.MarkerSHA256))!=1 ||
		subtle.ConstantTimeCompare([]byte(row.expectedReceipt),[]byte(claim.Evidence.ReceiptSHA256))!=1 ||
		subtle.ConstantTimeCompare([]byte(row.expectedWitness),[]byte(claim.Evidence.WitnessSHA256))!=1 {
		return denied()
	}

	// The claim itself must commit to the same original envelope bytes that
	// appear in the reply (the 000165 ledger intentionally has no envelope
	// digest). The other three digests were just checked against PostgreSQL.
	principal:=AgentTransportIdentity{AgentID:row.agentID,ServerID:row.serverID}
	replyMatches:=reply.ObservationID==claim.ObservationID &&
		matchesReply(reply,claim.Nonce,principal,row) &&
		MatchesEvidenceDigestsV1(claim.Evidence,reply.Observation)

	state:="reply_rejected"
	if replyMatches{state="reply_matched_unattested"}
	replyBytes,err:=json.Marshal(reply)
	if err!=nil {return denied()}
	replyDigest:=sha256.Sum256(replyBytes)
	tag,err:=tx.Exec(ctx,`
UPDATE vpn_runtime_recovery_observation_nonces n
SET state=$2,resolved_at=clock_timestamp(),response_sha256=$3
WHERE n.id=$1::uuid AND n.state='issued'
  AND n.nonce_sha256=$4 AND n.agent_id=$5::uuid AND n.server_id=$6::uuid
  AND n.attempt_id=$7::uuid AND n.attempt_epoch=$8
  AND n.agent_credential_generation=$9 AND clock_timestamp()<n.expires_at
  AND EXISTS (
     SELECT 1 FROM agents a WHERE a.id=n.agent_id AND a.server_id=n.server_id
       AND a.token_hash=$10
       AND a.credential_generation=n.agent_credential_generation
       AND a.capabilities @> '{"runtimeMutationFencingV1":true}'::jsonb
  )
`,claim.ObservationID,state,hex.EncodeToString(replyDigest[:]),nonceHash,
		row.agentID,row.serverID,row.attemptID,row.attemptEpoch,row.generation,tokenHash)
	if err!=nil {
		// The audit trigger is mandatory; any error aborts the transaction.
		// No token or reply bytes are ever included in the error message.
		return RecordedReply{},fmt.Errorf("persist authenticated observation response: %w",err)
	}
	if tag.RowsAffected()!=1{return denied()}
	if err:=tx.Commit(ctx);err!=nil{
		return RecordedReply{},fmt.Errorf("commit authenticated observation response: %w",err)
	}
	if !replyMatches{return denied()}
	return RecordedReply{ObservationID:claim.ObservationID,State:state},nil
}

// Prevent implicitly treating a successfully recorded response as a future
// authorization. There is intentionally no Boolean Approved/Unlocked field.
var _ time.Time
