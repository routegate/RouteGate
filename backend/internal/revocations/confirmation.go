package revocations

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Confirmation is immutable, durable operator intent only. It is NOT a
// dispatchable task, an authorization token, or proof of revoked VPN access.
type Confirmation struct {
	OperationID string    `json:"operationId"`
	ConfirmedBy string    `json:"confirmedBy"`
	ConfirmedAt time.Time `json:"confirmedAt"`
}

type ConfirmInput struct {
	OperationID                      string
	ExpectedCandidateHash            string
	SharedProcessRestartAcknowledged bool
}

type AuthorizedConfirmation struct{ pool *pgxpool.Pool }

func NewAuthorizedConfirmation(pool *pgxpool.Pool) *AuthorizedConfirmation {
	return &AuthorizedConfirmation{pool: pool}
}

// Confirm is deliberately unmounted. A future request handler must call
// auth.Middleware to validate the session and load permissions before calling
// this method. The explicit acknowledgement is not consent for task dispatch.
// The candidate hash is a compare-and-swap precondition, not a hash derived
// from a client-supplied candidate config.
func (s *AuthorizedConfirmation) Confirm(ctx context.Context, in ConfirmInput) (Confirmation, error) {
	deny := func() (Confirmation, error) { return Confirmation{}, ErrPreflight }
	if s == nil || s.pool == nil || !in.SharedProcessRestartAcknowledged ||
		!evidenceID.MatchString(in.OperationID) || !validRemovalHash(in.ExpectedCandidateHash) {
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

	// Lock the existing preparation first, so confirmation cannot race a
	// concurrent cancellation or rewrite its historical snapshot.
	var state, requestedBy, candidateHash string
	var baselineVersionID, agentID string
	var generation int64
	var candidateRuntimeHash string
	err = tx.QueryRow(ctx, `
		SELECT state, requested_by, baseline_version_id::text,
		       agent_id::text, agent_credential_generation,
		       candidate_hash, candidate_runtime_hash
		FROM vpn_credential_revocations WHERE id=$1::uuid FOR UPDATE
	`, in.OperationID).Scan(&state, &requestedBy, &baselineVersionID,
		&agentID, &generation, &candidateHash, &candidateRuntimeHash)
	if err != nil || state != "prepared" || requestedBy != actor ||
		candidateHash != in.ExpectedCandidateHash || generation <= 0 {
		return deny()
	}

	// Same-principal, same-snapshot retry returns the durable prior row.
	// A different or cancelled operation can never be reused.
	var existing Confirmation
	err = tx.QueryRow(ctx, `
		SELECT operation_id::text,confirmed_by,confirmed_at
		FROM vpn_credential_revocation_confirmations
		WHERE operation_id=$1::uuid
	`, in.OperationID).Scan(&existing.OperationID, &existing.ConfirmedBy, &existing.ConfirmedAt)
	if err == nil {
		if existing.ConfirmedBy != actor {
			return deny()
		}
		if err := tx.Commit(ctx); err != nil {
			return deny()
		}
		return existing, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return deny()
	}

	var confirmation Confirmation
	err = tx.QueryRow(ctx, `
		INSERT INTO vpn_credential_revocation_confirmations
		(operation_id, confirmed_by, baseline_version_id, agent_id,
		 agent_credential_generation, candidate_hash,
		 candidate_runtime_hash, shared_restart_acknowledged)
		VALUES ($1::uuid,$2,$3::uuid,$4::uuid,$5,$6,$7,true)
		RETURNING operation_id::text,confirmed_by,confirmed_at
	`, in.OperationID, actor, baselineVersionID, agentID, generation,
		candidateHash, candidateRuntimeHash).Scan(
		&confirmation.OperationID, &confirmation.ConfirmedBy, &confirmation.ConfirmedAt)
	if err != nil {
		return deny()
	}
	if tx.Commit(ctx) != nil {
		return deny()
	}
	return confirmation, nil
}
