package recoveryauth

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/ikaevus/routegate/backend/internal/auth"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ErrChallengeRejected deliberately does not reveal whether an ID, session,
// digest, evidence or expiry caused the refusal.
var ErrChallengeRejected = errors.New("recovery challenge was not accepted")

// Consumer only consumes previously issued, bound challenges. It does NOT
// issue challenges, approve a recovery or unlock an Agent.
type Consumer struct {
	pool *pgxpool.Pool
}

func NewConsumer(pool *pgxpool.Pool) *Consumer {
	return &Consumer{pool: pool}
}

// Consume atomically verifies a previously persisted secret commitment,
// rechecks the authenticated human's current database authority, transitions
// exactly once and commits the audit event before returning success.
//
// A caller MUST provide a principal established by existing trusted session
// middleware. This internal method is not an HTTP/API authentication mechanism.
// Any ambiguous database/commit failure must NEVER trigger an Agent unlock.
func (c *Consumer) Consume(ctx context.Context, challengeID, token string, principal auth.AuthenticatedUser) error {
	if c == nil || c.pool == nil ||
		!canonicalID.MatchString(challengeID) ||
		!canonicalID.MatchString(principal.ID) ||
		!canonicalID.MatchString(principal.SessionID) ||
		!auth.CanAuthorizeRuntimeRecovery(principal) {
		return ErrChallengeRejected
	}
	tx, err := c.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin challenge consumption: %w", err)
	}
	defer tx.Rollback(ctx)

	var record struct {
		commitment Commitment
		state      string
		now        time.Time
	}
	q := &record.commitment
	// Lock the challenge row BEFORE the proof check. A second contender
	// cannot observe "issued" after the first transaction commits.
	var credentialGeneration int64
	var fenced bool
	err = tx.QueryRow(ctx, `
SELECT c.state,c.operator_user_id::text,c.operator_session_id::text,
       c.context_sha256,c.challenge_sha256,c.issued_at,c.expires_at,
       r.id::text,r.server_id::text,r.agent_id::text,r.job_id::text,r.job_kind,
       r.agent_credential_generation,r.mutation_intent_sha256,
       r.result_receipt_sha256,r.runtime_witness_sha256,
       a.credential_generation,
       a.capabilities @> '{"runtimeMutationFencingV1":true}'::jsonb,
       clock_timestamp()
  FROM vpn_runtime_recovery_challenges c
  JOIN vpn_runtime_recovery_reservations r ON r.id=c.reservation_id
  JOIN agents a ON a.id=r.agent_id AND a.server_id=r.server_id
 WHERE c.id=$1::uuid
 FOR UPDATE OF c
`, challengeID).Scan(
		&record.state,&q.OperatorID,&q.SessionID,
		&q.ContextSHA256,&q.ChallengeSHA256,&q.IssuedAt,&q.ExpiresAt,
		&q.Reservation.ID,&q.Reservation.ServerID,&q.Reservation.AgentID,
		&q.Reservation.JobID,&q.Reservation.JobKind,
		&q.Reservation.AgentCredentialGeneration,&q.Reservation.MutationIntentSHA256,
		&q.Reservation.ResultReceiptSHA256,&q.Reservation.RuntimeWitnessSHA256,
		&credentialGeneration,&fenced,&record.now,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrChallengeRejected
	}
	if err != nil {
		return fmt.Errorf("read challenge binding: %w", err)
	}
	q.SchemaVersion = 1
	if record.state != "issued" || !fenced ||
		credentialGeneration != q.Reservation.AgentCredentialGeneration ||
		!q.IssuedAt.Equal(q.IssuedAt.UTC().Truncate(time.Second)) ||
		!q.ExpiresAt.Equal(q.ExpiresAt.UTC().Truncate(time.Second)) {
		return ErrChallengeRejected
	}

	// An authenticated request may have cached roles. This DB query is
	// authoritative; the 000163 UPDATE trigger independently rechecks and
	// locks the active session and permission grants until transaction commit.
	var stillAuthorized bool
	if err = tx.QueryRow(ctx, `
SELECT EXISTS (
 SELECT 1 FROM users u
 JOIN auth_sessions s ON s.user_id=u.id
 JOIN user_roles ur ON ur.user_id=u.id
 JOIN roles role ON role.id=ur.role_id
 JOIN role_permissions rp ON rp.role_id=role.id
 JOIN permissions perm ON perm.id=rp.permission_id
 WHERE u.id=$1::uuid AND s.id=$2::uuid
   AND s.revoked_at IS NULL AND s.expires_at>clock_timestamp()
   AND u.user_type='human' AND u.status='active'
   AND role.code='super_admin' AND perm.code='runtime_recovery:authorize'
)
`, principal.ID, principal.SessionID).Scan(&stillAuthorized); err != nil {
		return fmt.Errorf("verify current recovery authority: %w", err)
	}
	if !stillAuthorized ||
		!MatchesBoundChallenge(token,*q,principal,q.Reservation,record.now) {
		return ErrChallengeRejected
	}

	// Both binding hashes and the exact still-held reservation are checked
	// again while committing the one allowed transition. The trigger also
	// writes the issued->consumed audit event in this same transaction.
	result, err := tx.Exec(ctx, `
UPDATE vpn_runtime_recovery_challenges
   SET state='consumed',consumed_at=clock_timestamp()
 WHERE id=$1::uuid AND state='issued'
   AND reservation_id=$2::uuid
   AND operator_user_id=$3::uuid AND operator_session_id=$4::uuid
   AND context_sha256=$5 AND challenge_sha256=$6
   AND issued_at=$7 AND expires_at=$8
   AND clock_timestamp()>=issued_at AND clock_timestamp()<expires_at
`,challengeID,q.Reservation.ID,principal.ID,principal.SessionID,
		q.ContextSHA256,q.ChallengeSHA256,q.IssuedAt,q.ExpiresAt)
	if err != nil {
		// A concurrent session/role revocation is an authorization refusal.
		// All other database errors are ambiguous and must fail closed.
		return fmt.Errorf("commit recovery challenge consumption: %w", err)
	}
	if result.RowsAffected()!=1 {
		return ErrChallengeRejected
	}
	if err = tx.Commit(ctx); err != nil {
		return fmt.Errorf("persist recovery challenge and audit: %w", err)
	}
	return nil
}
