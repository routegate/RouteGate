package recoveryauth

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/ikaevus/routegate/backend/internal/auth"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ErrChallengeIssueRefused does not reveal whether the hold, credential
// generation, human session, permission or unique ledger entry was invalid.
var ErrChallengeIssueRefused = errors.New("recovery challenge issuance refused")

// Issuer is an internal, off-by-default primitive. It does not authenticate
// HTTP clients or issue instructions to Agent. A caller MUST provide a
// principal from existing trusted human-session middleware.
type Issuer struct {
	pool *pgxpool.Pool
}

func NewIssuer(pool *pgxpool.Pool) *Issuer {
	return &Issuer{pool: pool}
}

// IssuedChallenge contains the plaintext, generated once with crypto/rand.
// Never persist or log Token. Delivering it to a real administrator remains
// out of scope; there is no recovery route/UI or Agent unlock mechanism.
type IssuedChallenge struct {
	ID         string
	Token      string
	Commitment Commitment
}

func issueRefusal(err error) error {
	var pg *pgconn.PgError
	if errors.Is(err, pgx.ErrNoRows) ||
		(errors.As(err, &pg) && (pg.Code == "23505" || pg.Code == "23503" || pg.Code == "P0143")) {
		return ErrChallengeIssueRefused
	}
	return err
}

// Issue prepares the secret only after verifying a current, still-held
// reservation and the human operator's live DB session. The 000163 INSERT
// trigger independently locks and rechecks all authorization rows and
// atomically appends its issued audit event. Only a successful COMMIT makes
// the freshly generated plaintext visible to the caller.
//
// A failed or ambiguous commit never returns the token. In that case the
// one-per-reservation ledger may remain consumed/unavailable by design;
// there is NO automatic retry, reissue, or Agent fence release.
func (i *Issuer) Issue(ctx context.Context, reservationID string, principal auth.AuthenticatedUser) (IssuedChallenge, error) {
	var empty IssuedChallenge
	if i == nil || i.pool == nil ||
		!canonicalID.MatchString(reservationID) ||
		!canonicalID.MatchString(principal.ID) ||
		!canonicalID.MatchString(principal.SessionID) ||
		!auth.CanAuthorizeRuntimeRecovery(principal) {
		return empty, ErrChallengeIssueRefused
	}

	tx, err := i.pool.Begin(ctx)
	if err != nil {
		return empty, fmt.Errorf("begin recovery challenge issue: %w", err)
	}
	defer tx.Rollback(ctx)

	// The reservation row is immutable, but its Agent may rotate or lose
	// fencing. A FOR SHARE lock on Agent prevents an identity update until
	// this transaction (including audit) commits.
	var reservation Reservation
	var generation int64
	var fenced bool
	var dbNow time.Time
	err = tx.QueryRow(ctx, `
SELECT r.id::text,r.server_id::text,r.agent_id::text,r.job_id::text,
       r.job_kind,r.agent_credential_generation,r.mutation_intent_sha256,
       r.result_receipt_sha256,r.runtime_witness_sha256,
       a.credential_generation,
       a.capabilities @> '{"runtimeMutationFencingV1":true}'::jsonb,
       date_trunc('second',clock_timestamp())
  FROM vpn_runtime_recovery_reservations r
  JOIN agents a ON a.id=r.agent_id AND a.server_id=r.server_id
 WHERE r.id=$1::uuid
 FOR SHARE OF r,a
`, reservationID).Scan(
		&reservation.ID,&reservation.ServerID,&reservation.AgentID,
		&reservation.JobID,&reservation.JobKind,&reservation.AgentCredentialGeneration,
		&reservation.MutationIntentSHA256,&reservation.ResultReceiptSHA256,
		&reservation.RuntimeWitnessSHA256,&generation,&fenced,&dbNow,
	)
	if err != nil {
		return empty, issueRefusal(err)
	}
	if !reservation.valid() || !fenced || generation != reservation.AgentCredentialGeneration {
		return empty, ErrChallengeIssueRefused
	}

	// Validate live human role/session/grant inside this same transaction.
	// The database function obtains row locks, and the insert trigger
	// repeats the guard even if authorization changes at the boundary.
	_, err = tx.Exec(ctx,
		`SELECT routegate_recovery_challenge_actor_guard($1::uuid,$2::uuid)`,
		principal.ID, principal.SessionID)
	if err != nil {
		return empty, issueRefusal(err)
	}

	token, commitment, err := BuildChallenge(principal, reservation, dbNow)
	if err != nil {
		return empty, ErrChallengeIssueRefused
	}
	var id string
	err = tx.QueryRow(ctx, `
INSERT INTO vpn_runtime_recovery_challenges
    (reservation_id,operator_user_id,operator_session_id,
     context_sha256,challenge_sha256,issued_at,expires_at)
VALUES ($1::uuid,$2::uuid,$3::uuid,$4,$5,$6,$7)
RETURNING id::text
`, reservation.ID,principal.ID,principal.SessionID,
		commitment.ContextSHA256,commitment.ChallengeSHA256,
		commitment.IssuedAt,commitment.ExpiresAt).Scan(&id)
	if err != nil {
		return empty, issueRefusal(err)
	}

	// The insert's issued audit event is part of the same transaction.
	// Do not return the token until both have durably committed.
	if err := tx.Commit(ctx); err != nil {
		return empty, fmt.Errorf("commit recovery challenge issue: %w", err)
	}
	return IssuedChallenge{ID:id,Token:token,Commitment:commitment},nil
}
