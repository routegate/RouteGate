package revocations

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// ErrDispatchClaimAlreadyExists is NOT a retry signal. A prior claim may
// have been sent and may have restarted a shared VPN process. The outcome
// must be reconciled with authenticated Agent evidence, never resent.
var ErrDispatchClaimAlreadyExists = errors.New("revocation dispatch already claimed: outcome unknown; do not retry")

// ErrDispatchClaimOutcomeUnknown covers an ambiguous transaction commit.
// A caller must re-read the durable fence; it must never assume that the
// operation was not claimed, nor send an Agent task.
var ErrDispatchClaimOutcomeUnknown = errors.New("revocation dispatch claim commit outcome unknown")

// DispatchClaim is a non-executable historical fence, not a sendable task
// or any form of Agent credential-removal authorization.
type DispatchClaim struct {
	ID          string    `json:"id"`
	OperationID string    `json:"operationId"`
	ClaimedBy   string    `json:"claimedBy"`
	State       string    `json:"state"`
	ClaimedAt   time.Time `json:"claimedAt"`
}

type DispatchClaimInput struct {
	OperationID               string
	ExpectedCandidateHash     string
	ExplicitExecutionApproval bool
}

// AuthorizedDispatchClaim is intentionally unmounted from Manager HTTP and
// Agent job dispatch. The future HTTP route must first run auth.Middleware,
// revalidate the live session and obtain explicit, preview-bound consent.
type AuthorizedDispatchClaim struct{ pool *pgxpool.Pool }

func NewAuthorizedDispatchClaim(pool *pgxpool.Pool) *AuthorizedDispatchClaim {
	return &AuthorizedDispatchClaim{pool: pool}
}

// Claim persistently burns an operation's only possible dispatch claim.
// The record starts as outcome_unknown BEFORE any network transmission.
// If the process dies immediately afterwards, nothing can automatically
// reclaim this operation: confirmation and preparation remain preserved.
//
// This method DOES NOT enqueue, deliver or execute a task.
func (s *AuthorizedDispatchClaim) Claim(ctx context.Context, input DispatchClaimInput) (DispatchClaim, error) {
	deny := func() (DispatchClaim, error) { return DispatchClaim{}, ErrPreflight }
	if s == nil || s.pool == nil || !input.ExplicitExecutionApproval ||
		!evidenceID.MatchString(input.OperationID) || !validRemovalHash(input.ExpectedCandidateHash) {
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

	// Cancel acquires server-row -> operation-row locks. Do not obtain any
	// server FOR UPDATE here: that would invert the cancellation lock order.
	// The operation lock serializes competing claim/cancel transactions.
	var state, requestedBy, candidateHash, confirmedBy string
	err = tx.QueryRow(ctx, `
		SELECT p.state,p.requested_by,p.candidate_hash,c.confirmed_by
		FROM vpn_credential_revocations p
		JOIN vpn_credential_revocation_confirmations c ON c.operation_id=p.id
		WHERE p.id=$1::uuid
		FOR UPDATE OF p
	`, input.OperationID).Scan(&state, &requestedBy, &candidateHash, &confirmedBy)
	if err != nil || state != "prepared" || requestedBy != actor ||
		confirmedBy != actor || candidateHash != input.ExpectedCandidateHash {
		return deny()
	}

	// An identical repeated HTTP request, even by the same admin, MUST NOT
	// return another token or a "send now" instruction. The primary-key
	// constraint also guards against any alternate SQL insertion path.
	var alreadyClaimed bool
	if err := tx.QueryRow(ctx, `
		SELECT EXISTS(SELECT 1 FROM vpn_credential_revocation_dispatch_claims
		              WHERE operation_id=$1::uuid)
	`, input.OperationID).Scan(&alreadyClaimed); err != nil {
		return deny()
	}
	if alreadyClaimed {
		return DispatchClaim{}, ErrDispatchClaimAlreadyExists
	}

	var claim DispatchClaim
	err = tx.QueryRow(ctx, `
		INSERT INTO vpn_credential_revocation_dispatch_claims
		  (operation_id, claimed_by, server_id, agent_id,
		   agent_credential_generation, baseline_version_id,
		   confirmation_at, candidate_hash, candidate_runtime_hash)
		SELECT p.id,$2,p.server_id,p.agent_id,p.agent_credential_generation,
		       p.baseline_version_id,c.confirmed_at,p.candidate_hash,
		       p.candidate_runtime_hash
		FROM vpn_credential_revocations p
		JOIN vpn_credential_revocation_confirmations c ON c.operation_id=p.id
		WHERE p.id=$1::uuid
		RETURNING claim_id::text,operation_id::text,claimed_by,state,claimed_at
	`, input.OperationID, actor).Scan(
		&claim.ID, &claim.OperationID, &claim.ClaimedBy, &claim.State, &claim.ClaimedAt)
	if err != nil {
		return deny()
	}
	if err := tx.Commit(ctx); err != nil {
		return DispatchClaim{}, ErrDispatchClaimOutcomeUnknown
	}
	return claim, nil
}
