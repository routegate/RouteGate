package revocations

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ErrTaskIdentityAlreadyReserved means the single immutable identity was
// previously consumed. It is NOT a signal to send the task with an earlier
// task ID, reissue a new ID, or retry the lost network operation.
var ErrTaskIdentityAlreadyReserved = errors.New("revocation task identity already reserved; no retry authorized")

// ErrTaskIdentityOutcomeUnknown means PostgreSQL might have committed while
// its acknowledgement was lost. Readback/reconciliation is required; never
// assume that this task ID is unused or that a new task may be created.
var ErrTaskIdentityOutcomeUnknown = errors.New("revocation task identity reservation outcome unknown")

const CredentialRemovalTaskKind = "vless_credential_removal_v1"

// ReservedTaskIdentity is only a database identity, not an executable Agent
// job, a dispatch authorization or a proof of current VPN runtime state.
type ReservedTaskIdentity struct {
	TaskID       string    `json:"taskId"`
	ClaimID      string    `json:"claimId"`
	OperationID  string    `json:"operationId"`
	State        string    `json:"state"`
	ReservedAt   time.Time `json:"reservedAt"`
}

type ReserveTaskIdentityInput struct {
	OperationID           string
	ClaimID               string
	ExpectedCandidateHash string
}

// AuthorizedTaskIdentityReservation is intentionally unmounted from HTTP,
// agent_operation_jobs and delivery. A later route MUST validate the current
// session with auth.Middleware and separately authorize any actual execution.
type AuthorizedTaskIdentityReservation struct{ pool *pgxpool.Pool }

func NewAuthorizedTaskIdentityReservation(pool *pgxpool.Pool) *AuthorizedTaskIdentityReservation {
	return &AuthorizedTaskIdentityReservation{pool: pool}
}

// Reserve consumes exactly one database-generated task identity bound to a
// pre-existing immutable outcome_unknown claim. It never sends or queues a
// task, performs runtime work, or changes VPN accounts. The future dispatcher
// must NOT automatically retry after any ambiguous COMMIT.
func (s *AuthorizedTaskIdentityReservation) Reserve(ctx context.Context, in ReserveTaskIdentityInput) (ReservedTaskIdentity, error) {
	deny := func() (ReservedTaskIdentity, error) { return ReservedTaskIdentity{}, ErrPreflight }
	if s == nil || s.pool == nil || !evidenceID.MatchString(in.OperationID) ||
		!evidenceID.MatchString(in.ClaimID) || !validRemovalHash(in.ExpectedCandidateHash) {
		return deny()
	}
	actor, err := authenticatedRevocationActor(ctx)
	if err != nil {
		return deny()
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return deny()
	}
	defer tx.Rollback(ctx)

	var owner, parentOwner, confirmer, operation, candidateHash, claimState, parentState string
	err = tx.QueryRow(ctx, `
		SELECT c.claimed_by,p.requested_by,cf.confirmed_by,c.operation_id::text,
		       c.candidate_hash,c.state,p.state
		FROM vpn_credential_revocation_dispatch_claims c
		JOIN vpn_credential_revocations p ON p.id=c.operation_id
		JOIN vpn_credential_revocation_confirmations cf ON cf.operation_id=p.id
		WHERE c.claim_id=$1::uuid AND c.operation_id=$2::uuid
	`, in.ClaimID, in.OperationID).Scan(
		&owner,&parentOwner,&confirmer,&operation,&candidateHash,&claimState,&parentState)
	if err != nil || owner != actor || parentOwner != actor || confirmer != actor ||
		candidateHash != in.ExpectedCandidateHash || claimState != DispatchClaimOutcomeUnknown ||
		parentState != "prepared" || operation != in.OperationID {
		return deny()
	}

	var result ReservedTaskIdentity
	err = tx.QueryRow(ctx, `
		INSERT INTO vpn_credential_revocation_task_reservations(claim_id)
		VALUES ($1::uuid)
		ON CONFLICT (claim_id) DO NOTHING
		RETURNING task_id::text,claim_id::text,operation_id::text,state,reserved_at
	`, in.ClaimID).Scan(
		&result.TaskID,&result.ClaimID,&result.OperationID,&result.State,&result.ReservedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		// The current or a concurrent transaction already reserved this claim.
		// Returning its task ID would allow a caller to mistake a read for
		// permission to resend after a lost acknowledgement.
		return ReservedTaskIdentity{}, ErrTaskIdentityAlreadyReserved
	}
	if err != nil {
		return deny()
	}
	if err := tx.Commit(ctx); err != nil {
		return ReservedTaskIdentity{}, ErrTaskIdentityOutcomeUnknown
	}
	return result, nil
}
