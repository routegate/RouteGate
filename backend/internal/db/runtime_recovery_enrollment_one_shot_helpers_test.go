package db

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"time"

	"github.com/ikaevus/routegate/backend/internal/recoveryenroll"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// TEST-ONLY transaction rehearsal. This file ends in _test.go and is never
// included in Manager binaries. Its schema exists only in disposable,
// isolated PostgreSQL fixtures. No new migrations, runtime enrollment,
// dispatcher binding, Agent Runner, HTTP route or recovery unlock are added.
var errEnrollmentOneShotRehearsalRefusedV1 = errors.New("offline enrollment challenge rehearsal refused")

const enrollmentOneShotRehearsalSchemaV1 = `
CREATE TABLE rg140_test_enrollment_attempts (
 id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
 preparation_id uuid NOT NULL UNIQUE
     REFERENCES vpn_runtime_recovery_enrollment_preparations(id) ON DELETE RESTRICT,
 agent_id uuid NOT NULL REFERENCES agents(id) ON DELETE RESTRICT,
 server_id uuid NOT NULL REFERENCES servers(id) ON DELETE RESTRICT,
 credential_generation bigint NOT NULL CHECK(credential_generation>0),
 expected_genesis_sha256 text NOT NULL
     CHECK(expected_genesis_sha256 ~ '^[0-9a-f]{64}$'),
 nonce_sha256 text NOT NULL UNIQUE CHECK(nonce_sha256 ~ '^[0-9a-f]{64}$'),
 issued_at timestamptz NOT NULL,
 expires_at timestamptz NOT NULL,
 state text NOT NULL DEFAULT 'issued_unattested'
     CHECK(state IN ('issued_unattested','reply_matched_unattested','reply_rejected')),
 resolved_at timestamptz,
 response_sha256 text CHECK(response_sha256 ~ '^[0-9a-f]{64}$'),
 CHECK(expires_at>issued_at AND expires_at<=issued_at+interval '60 seconds'),
 CHECK((state='issued_unattested' AND resolved_at IS NULL AND response_sha256 IS NULL)
    OR (state<>'issued_unattested' AND resolved_at IS NOT NULL AND response_sha256 IS NOT NULL))
);
CREATE TABLE rg140_test_enrollment_attempt_events (
 sequence bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
 attempt_id uuid NOT NULL REFERENCES rg140_test_enrollment_attempts(id) ON DELETE RESTRICT,
 state text NOT NULL CHECK(state IN ('issued_unattested','reply_matched_unattested','reply_rejected')),
 created_at timestamptz NOT NULL DEFAULT clock_timestamp()
);
CREATE FUNCTION rg140_test_enrollment_attempt_audit() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 INSERT INTO rg140_test_enrollment_attempt_events(attempt_id,state) VALUES(NEW.id,NEW.state);
 RETURN NEW;
END; $$;
CREATE TRIGGER rg140_test_enrollment_attempt_audit_trigger
 AFTER INSERT OR UPDATE ON rg140_test_enrollment_attempts
 FOR EACH ROW EXECUTE FUNCTION rg140_test_enrollment_attempt_audit();
`

type enrollmentOneShotRehearsalIssueV1 struct {
	ID string
	Nonce string
	ExpectedGenesisSHA256 string
	Unattested bool
}

type enrollmentOneShotRehearsalReplyV1 struct {
	State string
	Unattested bool
}

func digestEnrollmentOneShotNonceV1(raw []byte) string {
	h := sha256.Sum256(raw)
	return hex.EncodeToString(h[:])
}

// Only the previously prepared Manager identity is selected. This test-only
// issuer has no live super-admin session/HTTP/Agent transport. An authenticated
// operator initiation must be added and checked separately before production.
func issueEnrollmentOneShotRehearsalV1(
	ctx context.Context, pool *pgxpool.Pool, agentID string,
) (enrollmentOneShotRehearsalIssueV1, error) {
	deny := func() (enrollmentOneShotRehearsalIssueV1, error) {
		return enrollmentOneShotRehearsalIssueV1{}, errEnrollmentOneShotRehearsalRefusedV1
	}
	if ctx == nil || ctx.Err()!=nil || pool == nil || agentID=="" {
		return deny()
	}
	// Secure source and unique nonce; only its digest is stored in the DB.
	nonce := make([]byte, 32)
	if _,err := rand.Read(nonce); err!=nil { return deny() }
	nonceDigest := digestEnrollmentOneShotNonceV1(nonce)

	tx,err := pool.BeginTx(ctx,pgx.TxOptions{IsoLevel:pgx.ReadCommitted})
	if err!=nil {return deny()}
	defer tx.Rollback(ctx)

	var p recoveryenroll.Preparation
	var liveGeneration int64
	var agentStatus string
	var hasFencing bool
	var auditCount int64
	err=tx.QueryRow(ctx,`
SELECT p.id::text,p.agent_id::text,p.server_id::text,
       p.agent_credential_generation,p.phase,
       a.credential_generation,a.status,
       (a.capabilities @> '{"runtimeMutationFencingV1":true}'::jsonb),
       (SELECT count(*) FROM vpn_runtime_recovery_enrollment_preparation_events e
          WHERE e.preparation_id=p.id AND e.action='prepared_unattested')
FROM vpn_runtime_recovery_enrollment_preparations p
JOIN agents a ON a.id=p.agent_id AND a.server_id=p.server_id
JOIN servers s ON s.id=p.server_id
WHERE p.agent_id=$1::uuid
FOR UPDATE OF a,s
`,agentID).Scan(
		&p.ID,&p.AgentID,&p.ServerID,&p.AgentCredentialGeneration,&p.Phase,
		&liveGeneration,&agentStatus,&hasFencing,&auditCount,
	)
	if err!=nil || p.AgentID!=agentID || p.AgentCredentialGeneration!=liveGeneration ||
		agentStatus=="disabled" || !hasFencing || auditCount!=1 {
		return deny()
	}
	// The existing job admission paths serialize their changes on server
	// records. This snapshot is still NOT a live host lock after commit.
	var blockers int64
	err=tx.QueryRow(ctx,`
SELECT
 (SELECT count(*) FROM vpn_runtime_recovery_reservations WHERE server_id=$1::uuid)
 +(SELECT count(*) FROM vpn_runtime_pre_dispatch_checkpoints WHERE server_id=$1::uuid)
 +(SELECT count(*) FROM config_apply_jobs WHERE server_id=$1::uuid
     AND status IN ('pending','in_progress'))
 +(SELECT count(*) FROM agent_operation_jobs WHERE server_id=$1::uuid
     AND status IN ('pending','in_progress'))
 +(SELECT count(*) FROM agent_platform_update_jobs WHERE server_id=$1::uuid
     AND status IN ('pending','in_progress','mutation_dispatched','outcome_unknown'))
`,p.ServerID).Scan(&blockers)
	if err!=nil || blockers!=0 || ctx.Err()!=nil {return deny()}
	expected,err:=recoveryenroll.OfflineGenesisDigestUnattestedV1(p)
	if err!=nil {return deny()}

	var id string
	err=tx.QueryRow(ctx,`
INSERT INTO rg140_test_enrollment_attempts(
 preparation_id,agent_id,server_id,credential_generation,
 expected_genesis_sha256,nonce_sha256,issued_at,expires_at
)
SELECT $1::uuid,$2::uuid,$3::uuid,$4,$5,$6,
       clock_timestamp(),clock_timestamp()+interval '30 seconds'
RETURNING id::text
`,p.ID,p.AgentID,p.ServerID,p.AgentCredentialGeneration,expected,nonceDigest).Scan(&id)
	if err!=nil || id=="" {return deny()}
	if err=tx.Commit(ctx);err!=nil {return deny()}
	return enrollmentOneShotRehearsalIssueV1{
		ID:id,Nonce:base64.RawURLEncoding.EncodeToString(nonce),
		ExpectedGenesisSHA256:expected,Unattested:true,
	},nil
}

// Single-consume rehearsal: valid existing bearer identity + 32-byte nonce
// are checked while holding Manager DB rows. Even a matched reply remains
// historical UNATTESTED evidence, not trusted host authentication.
// Wrong bearer/nonce or stale Manager generation refuses WITHOUT consumption;
// a correct challenge with wrong digest consumes as a permanent rejection.
// No retries after ambiguous transaction commits are authorized.
func consumeEnrollmentOneShotRehearsalV1(
	ctx context.Context, pool *pgxpool.Pool,
	attemptID,nonceEncoded,bearerToken,claimedGenesisDigest string,
) (enrollmentOneShotRehearsalReplyV1,error) {
	deny:=func()(enrollmentOneShotRehearsalReplyV1,error){
		return enrollmentOneShotRehearsalReplyV1{},errEnrollmentOneShotRehearsalRefusedV1
	}
	if ctx==nil || ctx.Err()!=nil || pool==nil ||
		attemptID=="" || len(bearerToken)==0 || len(claimedGenesisDigest)!=64 {
		return deny()
	}
	raw,err:=base64.RawURLEncoding.DecodeString(nonceEncoded)
	if err!=nil || len(raw)!=32 ||
		base64.RawURLEncoding.EncodeToString(raw)!=nonceEncoded {
		return deny()
	}
	nonceHash:=digestEnrollmentOneShotNonceV1(raw)
	tokenHash:=digestEnrollmentOneShotNonceV1([]byte(bearerToken))

	tx,err:=pool.BeginTx(ctx,pgx.TxOptions{IsoLevel:pgx.ReadCommitted})
	if err!=nil{return deny()}
	defer tx.Rollback(ctx)

	var recordedNonceHash,recordedGenesisHash,actualTokenHash string
	var agentStatus string
	var generation,liveGeneration,eventCount int64
	var hasFencing bool
	err=tx.QueryRow(ctx,`
SELECT o.nonce_sha256,o.expected_genesis_sha256,
       a.token_hash,o.credential_generation,a.credential_generation,
       a.status,(a.capabilities @> '{"runtimeMutationFencingV1":true}'::jsonb),
       (SELECT count(*) FROM rg140_test_enrollment_attempt_events e
         WHERE e.attempt_id=o.id AND e.state='issued_unattested')
FROM rg140_test_enrollment_attempts o
JOIN agents a ON a.id=o.agent_id AND a.server_id=o.server_id
JOIN servers s ON s.id=o.server_id
JOIN vpn_runtime_recovery_enrollment_preparations p
  ON p.id=o.preparation_id AND p.agent_id=o.agent_id
  AND p.server_id=o.server_id AND p.agent_credential_generation=o.credential_generation
  AND p.phase='prepared_unattested'
WHERE o.id=$1::uuid AND o.state='issued_unattested'
  AND clock_timestamp()<o.expires_at
FOR UPDATE OF o,a,s
`,attemptID).Scan(
		&recordedNonceHash,&recordedGenesisHash,&actualTokenHash,
		&generation,&liveGeneration,&agentStatus,&hasFencing,&eventCount,
	)
	if err!=nil || generation!=liveGeneration || agentStatus=="disabled" ||
		!hasFencing || eventCount!=1 ||
		subtle.ConstantTimeCompare([]byte(recordedNonceHash),[]byte(nonceHash))!=1 ||
		subtle.ConstantTimeCompare([]byte(actualTokenHash),[]byte(tokenHash))!=1 {
		return deny()
	}
	var blockers int64
	err=tx.QueryRow(ctx,`
SELECT
 (SELECT count(*) FROM vpn_runtime_recovery_reservations r
    JOIN rg140_test_enrollment_attempts o ON o.server_id=r.server_id
    WHERE o.id=$1::uuid)
 +(SELECT count(*) FROM vpn_runtime_pre_dispatch_checkpoints c
    JOIN rg140_test_enrollment_attempts o ON o.server_id=c.server_id
    WHERE o.id=$1::uuid)
 +(SELECT count(*) FROM config_apply_jobs j
    JOIN rg140_test_enrollment_attempts o ON o.server_id=j.server_id
    WHERE o.id=$1::uuid AND j.status IN ('pending','in_progress'))
 +(SELECT count(*) FROM agent_operation_jobs j
    JOIN rg140_test_enrollment_attempts o ON o.server_id=j.server_id
    WHERE o.id=$1::uuid AND j.status IN ('pending','in_progress'))
 +(SELECT count(*) FROM agent_platform_update_jobs j
    JOIN rg140_test_enrollment_attempts o ON o.server_id=j.server_id
    WHERE o.id=$1::uuid AND j.status IN
     ('pending','in_progress','mutation_dispatched','outcome_unknown'))
`,attemptID).Scan(&blockers)
	if err!=nil || blockers!=0 || ctx.Err()!=nil {return deny()}

	state:="reply_rejected"
	if subtle.ConstantTimeCompare([]byte(recordedGenesisHash),[]byte(claimedGenesisDigest))==1 {
		state="reply_matched_unattested"
	}
	replyBytes:=[]byte("routegate/rg140/offline-enrollment-reply/v1\x00"+attemptID+"\x00"+claimedGenesisDigest)
	replyHash:=digestEnrollmentOneShotNonceV1(replyBytes)
	tag,err:=tx.Exec(ctx,`
UPDATE rg140_test_enrollment_attempts
SET state=$2,resolved_at=clock_timestamp(),response_sha256=$3
WHERE id=$1::uuid AND state='issued_unattested'
`,attemptID,state,replyHash)
	if err!=nil || tag.RowsAffected()!=1 {return deny()}
	if err=tx.Commit(ctx);err!=nil{return deny()}
	return enrollmentOneShotRehearsalReplyV1{State:state,Unattested:true},nil
}

// The test returns a zero receipt on any ambiguous failure; the caller must
// inspect historical Manager ledger, never infer enrollment/authorization.
var _ = time.Second
