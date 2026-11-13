package db

import (
	"context"
	"errors"

	"github.com/ikaevus/routegate/backend/internal/recoveryenroll"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// OFFLINE TEST-ONLY evidence inspection. No nonce/token/private key ever
// leaves this readback, and no attempt can be retried, reissued, resumed,
// enrolled, dispatched or unlocked by calling it.
var errEnrollmentManualReconciliationRefusedV1 = errors.New("offline enrollment manual reconciliation refused")

type enrollmentManualReconciliationV1 struct {
	GrantID           string
	AttemptID         string // empty if no one-shot challenge committed
	PreparationID     string
	AgentID           string
	ServerID          string
	Generation        int64
	HistoricalState   string
	Unattested        bool
	ManualReviewOnly  bool
}

const enrollmentManualGrantOnlyStateV1 = "grant_only_unattested"

// This REPEATABLE READ READ ONLY transaction inspects *durable* Manager
// history after a simulated process restart or lost acknowledgement. It
// deliberately DOES NOT check that a past grant is still current: a revoked
// operator's historical attempt must remain reviewable but cannot become
// live authority. The original challenge nonce is never recoverable.
func inspectEnrollmentManualReconciliationOfflineV1(
	ctx context.Context, pool *pgxpool.Pool, preparationID string,
) (enrollmentManualReconciliationV1, error) {
	deny := func() (enrollmentManualReconciliationV1, error) {
		return enrollmentManualReconciliationV1{}, errEnrollmentManualReconciliationRefusedV1
	}
	if ctx == nil || ctx.Err() != nil || pool == nil ||
		!enrollmentGrantCanonicalUUIDV1.MatchString(preparationID) {
		return deny()
	}
	tx, err := pool.BeginTx(ctx, pgx.TxOptions{
		IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly,
	})
	if err != nil { return deny() }
	defer tx.Rollback(ctx)

	var history enrollmentManualReconciliationV1
	var actorID, sessionID, phase string
	var prepGeneration int64
	err = tx.QueryRow(ctx, `
SELECT g.id::text,g.preparation_id::text,g.agent_id::text,
       g.server_id::text,g.credential_generation,
       g.actor_user_id::text,g.actor_session_id::text,
       p.phase,p.agent_credential_generation
  FROM rg140_test_enrollment_challenge_grants g
  JOIN vpn_runtime_recovery_enrollment_preparations p
    ON p.id=g.preparation_id AND p.agent_id=g.agent_id
   AND p.server_id=g.server_id
 WHERE g.preparation_id=$1::uuid
`, preparationID).Scan(
		&history.GrantID,&history.PreparationID,
		&history.AgentID,&history.ServerID,&history.Generation,
		&actorID,&sessionID,&phase,&prepGeneration,
	)
	if err != nil || history.PreparationID != preparationID ||
		history.Generation <= 0 || history.Generation != prepGeneration ||
		phase != recoveryenroll.PhasePreparedUnattested {
		return deny()
	}
	var grantEvents, grantAttribution, preparationEvents int64
	err = tx.QueryRow(ctx, `
SELECT
 (SELECT count(*) FROM rg140_test_enrollment_challenge_grant_events
    WHERE grant_id=$1::uuid AND action='granted_unattested'),
 (SELECT count(*) FROM audit_events
    WHERE action='runtime_recovery.enrollment_challenge_granted_unattested'
      AND actor_user_id=$2::uuid AND resource_id=$3::uuid
      AND metadata->>'grant_id'=$1
      AND metadata->>'session_id'=$4),
 (SELECT count(*) FROM vpn_runtime_recovery_enrollment_preparation_events
    WHERE preparation_id=$5::uuid AND action='prepared_unattested')
`,history.GrantID,actorID,history.AgentID,sessionID,preparationID).Scan(
		&grantEvents,&grantAttribution,&preparationEvents,
	)
	if err != nil || grantEvents != 1 || grantAttribution != 1 ||
		preparationEvents != 1 { return deny() }

	var state, responseDigest, prep, agent, server, nonceDigest, genesisDigest string
	var generation int64
	err = tx.QueryRow(ctx, `
SELECT id::text,state,COALESCE(response_sha256,''),
       preparation_id::text,agent_id::text,server_id::text,
       credential_generation,nonce_sha256,expected_genesis_sha256
 FROM rg140_test_enrollment_attempts
 WHERE grant_id=$1::uuid
`,history.GrantID).Scan(
		&history.AttemptID,&state,&responseDigest,
		&prep,&agent,&server,&generation,&nonceDigest,&genesisDigest,
	)
	if errors.Is(err,pgx.ErrNoRows) {
		// Crash before the first challenge committed: the existing grant
		// may be historically valid, but an observer NEVER issues a new
		// challenge or returns authorization to resume automatically.
		var unexpectedAudit int64
		err = tx.QueryRow(ctx, `
SELECT count(*) FROM audit_events
 WHERE action='runtime_recovery.enrollment_challenge_issued_unattested'
   AND metadata->>'grant_id'=$1
`,history.GrantID).Scan(&unexpectedAudit)
		if err != nil || unexpectedAudit != 0 { return deny() }
		history.HistoricalState = enrollmentManualGrantOnlyStateV1
		history.Unattested, history.ManualReviewOnly = true,true
		if err = tx.Commit(ctx); err != nil { return deny() }
		return history,nil
	}
	if err != nil || prep != history.PreparationID ||
		agent != history.AgentID || server != history.ServerID ||
		generation != history.Generation || len(nonceDigest) != 64 ||
		len(genesisDigest) != 64 { return deny() }

	var issuedEvents, matchedEvents, rejectedEvents, totalEvents, issuedAudit int64
	err = tx.QueryRow(ctx, `
SELECT
 (SELECT count(*) FROM rg140_test_enrollment_attempt_events
    WHERE attempt_id=$1::uuid AND state='issued_unattested'),
 (SELECT count(*) FROM rg140_test_enrollment_attempt_events
    WHERE attempt_id=$1::uuid AND state='reply_matched_unattested'),
 (SELECT count(*) FROM rg140_test_enrollment_attempt_events
    WHERE attempt_id=$1::uuid AND state='reply_rejected'),
 (SELECT count(*) FROM rg140_test_enrollment_attempt_events
    WHERE attempt_id=$1::uuid),
 (SELECT count(*) FROM audit_events
    WHERE action='runtime_recovery.enrollment_challenge_issued_unattested'
      AND actor_user_id=$2::uuid AND resource_id=$3::uuid
      AND metadata->>'attempt_id'=$1
      AND metadata->>'grant_id'=$4
      AND metadata->>'session_id'=$5)
`,history.AttemptID,actorID,history.AgentID,history.GrantID,sessionID).Scan(
		&issuedEvents,&matchedEvents,&rejectedEvents,&totalEvents,&issuedAudit,
	)
	if err != nil || issuedEvents != 1 || issuedAudit != 1 { return deny() }
	switch state {
	case "issued_unattested":
		if totalEvents != 1 || matchedEvents != 0 ||
			rejectedEvents != 0 || responseDigest != "" { return deny() }
	case "reply_matched_unattested":
		if totalEvents != 2 || matchedEvents != 1 ||
			rejectedEvents != 0 || len(responseDigest) != 64 { return deny() }
	case "reply_rejected":
		if totalEvents != 2 || matchedEvents != 0 ||
			rejectedEvents != 1 || len(responseDigest) != 64 { return deny() }
	default:
		return deny()
	}
	history.HistoricalState = state
	history.Unattested,history.ManualReviewOnly = true,true
	if err = tx.Commit(ctx); err != nil { return deny() }
	return history,nil
}
