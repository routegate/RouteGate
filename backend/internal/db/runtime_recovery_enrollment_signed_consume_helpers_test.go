package db

import (
	"context"
	"crypto/subtle"
	"encoding/json"

	"github.com/ikaevus/routegate/backend/internal/recoveryenroll"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// RG-140: TEST-ONLY, compiled exclusively into Go tests. This exercise
// consumes a Manager-issued nonce and verifies the ENTIRE Agent reply MAC
// while holding the same PostgreSQL row locks used to commit the one-use
// state and audit. This method is not connected to any HTTP route or Agent
// polling and does not attest a host or grant VPN mutation rights.
func consumeSignedEnrollmentProbeRehearsalV1(
	ctx context.Context, pool *pgxpool.Pool,
	token string, reply recoveryenroll.EnrollmentProbeReplyV1, replyMAC string,
) (enrollmentOneShotRehearsalReplyV1, error) {
	deny := func() (enrollmentOneShotRehearsalReplyV1, error) {
		return enrollmentOneShotRehearsalReplyV1{}, errEnrollmentOneShotRehearsalRefusedV1
	}
	if ctx == nil || ctx.Err() != nil || pool == nil {
		return deny()
	}
	tx, err := pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil { return deny() }
	defer tx.Rollback(ctx)

	// Caller fields are NEVER used to choose an Agent credential or
	// preparation independently: the Manager's immutable issued attempt
	// selects the entire tuple, and the current Agent row supplies token
	// provenance. Both row locks remain held until the resolution commits.
	var (
		attemptPreparation, attemptAgent, attemptServer string
		nonceSHA, expectedGenesis, currentTokenSHA string
		status string
		issuedGeneration, liveGeneration, issuedEventCount int64
		fencing bool
	)
	err = tx.QueryRow(ctx, `
SELECT o.preparation_id::text,o.agent_id::text,o.server_id::text,
       o.nonce_sha256,o.expected_genesis_sha256,a.token_hash,
       o.credential_generation,a.credential_generation,a.status,
       (a.capabilities @> '{"runtimeMutationFencingV1":true}'::jsonb),
       (SELECT count(*) FROM rg140_test_enrollment_attempt_events e
         WHERE e.attempt_id=o.id AND e.state='issued_unattested')
FROM rg140_test_enrollment_attempts o
JOIN agents a ON a.id=o.agent_id AND a.server_id=o.server_id
JOIN servers s ON s.id=o.server_id
JOIN vpn_runtime_recovery_enrollment_preparations p
  ON p.id=o.preparation_id AND p.agent_id=o.agent_id
 AND p.server_id=o.server_id
 AND p.agent_credential_generation=o.credential_generation
 AND p.phase='prepared_unattested'
WHERE o.id=$1::uuid AND o.state='issued_unattested'
 AND clock_timestamp() < o.expires_at
FOR UPDATE OF o,a,s
`,reply.AttemptID).Scan(
		&attemptPreparation,&attemptAgent,&attemptServer,
		&nonceSHA,&expectedGenesis,&currentTokenSHA,
		&issuedGeneration,&liveGeneration,&status,&fencing,&issuedEventCount,
	)
	if err != nil || issuedGeneration != liveGeneration ||
		status == "disabled" || !fencing || issuedEventCount != 1 ||
		attemptPreparation != reply.ManagerPreparationID ||
		attemptAgent != reply.AgentID || attemptServer != reply.ServerID ||
		issuedGeneration != reply.AgentCredentialGeneration ||
		subtle.ConstantTimeCompare(
			[]byte(currentTokenSHA),
			[]byte(digestEnrollmentOneShotNonceV1([]byte(token))),
		) != 1 {
		return deny()
	}

	// Pure MAC verification is performed INSIDE the locked transaction.
	// This rejects altered but otherwise structurally valid signed fields,
	// bad bearer, noncanonical nonce/hash encodings, and unknown MACs.
	if !recoveryenroll.VerifyEnrollmentProbeReplyMACOfflineV1(token,reply,replyMAC) {
		return deny()
	}
	nonceRaw, err := decodeEnrollmentProbeNonceRehearsalV1(reply.Nonce)
	if err != nil || subtle.ConstantTimeCompare(
		[]byte(nonceSHA),
		[]byte(digestEnrollmentOneShotNonceV1(nonceRaw)),
	) != 1 { return deny() }

	var blockers int64
	err = tx.QueryRow(ctx, `
SELECT (SELECT count(*) FROM vpn_runtime_recovery_reservations WHERE server_id=$1::uuid)
     + (SELECT count(*) FROM vpn_runtime_pre_dispatch_checkpoints WHERE server_id=$1::uuid)
     + (SELECT count(*) FROM config_apply_jobs WHERE server_id=$1::uuid
         AND status IN ('pending','in_progress'))
     + (SELECT count(*) FROM agent_operation_jobs WHERE server_id=$1::uuid
         AND status IN ('pending','in_progress'))
     + (SELECT count(*) FROM agent_platform_update_jobs WHERE server_id=$1::uuid
         AND status IN ('pending','in_progress','mutation_dispatched','outcome_unknown'))
`,attemptServer).Scan(&blockers)
	if err != nil || blockers != 0 || ctx.Err() != nil { return deny() }

	// Once current bearer/nonce and full MAC are authenticated, a digest
	// mismatch permanently consumes the challenge as rejected. Even a
	// successful digest comparison remains only historical UNATTESTED data.
	state := "reply_rejected"
	if subtle.ConstantTimeCompare(
		[]byte(expectedGenesis), []byte(reply.LocalGenesisSHA256),
	) == 1 {
		state = "reply_matched_unattested"
	}
	encoded, err := json.Marshal(reply)
	if err != nil { return deny() }
	responseDigest := digestEnrollmentOneShotNonceV1(append(
		append([]byte("routegate/rg140/enrollment-probe-consumed/v1\x00"),encoded...),
		[]byte("\x00"+replyMAC)...,
	))
	tag, err := tx.Exec(ctx, `
UPDATE rg140_test_enrollment_attempts
   SET state=$2,resolved_at=clock_timestamp(),response_sha256=$3
 WHERE id=$1::uuid AND state='issued_unattested'
   AND clock_timestamp() < expires_at
`, reply.AttemptID,state,responseDigest)
	if err != nil || tag.RowsAffected() != 1 { return deny() }
	// The test-only AFTER UPDATE trigger inserts one audit event into the
	// same transaction; a failed audit cancels the state change.
	if err := tx.Commit(ctx); err != nil { return deny() }
	return enrollmentOneShotRehearsalReplyV1{State:state,Unattested:true},nil
}
