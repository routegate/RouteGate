package revocations

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// DispatchClaimObservation is historical, read-only Manager evidence.
//
// Not observed does NOT mean "not sent" or "safe to retry": a concurrent
// claim can still commit after this read. A recorded claim is irrevocably
// outcome_unknown until separately authenticated Agent reconciliation exists.
type DispatchClaimObservation struct {
	OperationID string    `json:"operationId"`
	ClaimID     string    `json:"claimId,omitempty"`
	State       string    `json:"state"`
	ClaimedAt   time.Time `json:"claimedAt,omitempty"`
}

const (
	DispatchClaimNotObserved   = "not_observed"
	DispatchClaimOutcomeUnknown = "outcome_unknown"
)

// DispatchClaimReadback has no method to insert, delete, send, acknowledge,
// cancel, authorize replay, or mark a credential removed. It is NOT mounted
// on HTTP. Any future HTTP caller MUST use auth.Middleware for a freshly
// authenticated session before passing a request context.
type DispatchClaimReadback struct{ pool *pgxpool.Pool }

func NewDispatchClaimReadback(pool *pgxpool.Pool) *DispatchClaimReadback {
	return &DispatchClaimReadback{pool: pool}
}

// Observe reads immutable claim history for its original authenticated
// super-admin. It never returns a "safe to resend" state. An ambiguous
// transaction commit still requires independent reconciliation even when
// a snapshot observes no committed row.
func (s *DispatchClaimReadback) Observe(ctx context.Context, operationID string) (DispatchClaimObservation, error) {
	deny := func() (DispatchClaimObservation, error) { return DispatchClaimObservation{}, ErrPreflight }
	if s == nil || s.pool == nil || !evidenceID.MatchString(operationID) {
		return deny()
	}
	actor, err := authenticatedRevocationActor(ctx)
	if err != nil {
		return deny()
	}
	var parentState, requestedBy, parentServer, parentAgent, parentBaseline, parentHash, parentRuntimeHash string
	var parentGeneration int64
	var confirmedBy, claimID, claimedBy, claimState, claimServer, claimAgent, claimBaseline, claimHash, claimRuntimeHash string
	var claimGeneration int64
	var claimedAt time.Time
	err = s.pool.QueryRow(ctx, `
		SELECT p.state,p.requested_by,p.server_id::text,p.agent_id::text,
		       p.agent_credential_generation,p.baseline_version_id::text,
		       p.candidate_hash,p.candidate_runtime_hash,
		       COALESCE(c.confirmed_by,''),
		       COALESCE(d.claim_id::text,''),COALESCE(d.claimed_by,''),
		       COALESCE(d.state,''),COALESCE(d.server_id::text,''),
		       COALESCE(d.agent_id::text,''),COALESCE(d.agent_credential_generation,0),
		       COALESCE(d.baseline_version_id::text,''),
		       COALESCE(d.candidate_hash,''),COALESCE(d.candidate_runtime_hash,''),
		       COALESCE(d.claimed_at,'epoch'::timestamptz)
		FROM vpn_credential_revocations p
		LEFT JOIN vpn_credential_revocation_confirmations c ON c.operation_id=p.id
		LEFT JOIN vpn_credential_revocation_dispatch_claims d ON d.operation_id=p.id
		WHERE p.id=$1::uuid
	`, operationID).Scan(
		&parentState,&requestedBy,&parentServer,&parentAgent,&parentGeneration,&parentBaseline,
		&parentHash,&parentRuntimeHash,&confirmedBy,
		&claimID,&claimedBy,&claimState,&claimServer,&claimAgent,&claimGeneration,
		&claimBaseline,&claimHash,&claimRuntimeHash,&claimedAt)
	if err != nil || requestedBy != actor {
		return deny()
	}
	if claimID == "" {
		// This observation is intentionally not a negative dispatch proof.
		// It must never be used to automatically reissue a timed-out claim.
		return DispatchClaimObservation{OperationID: operationID, State: DispatchClaimNotObserved}, nil
	}
	if parentState != "prepared" || confirmedBy != actor || claimedBy != actor ||
		!evidenceID.MatchString(claimID) || claimState != DispatchClaimOutcomeUnknown ||
		claimServer != parentServer || claimAgent != parentAgent ||
		claimGeneration != parentGeneration || claimBaseline != parentBaseline ||
		claimHash != parentHash || claimRuntimeHash != parentRuntimeHash ||
		claimedAt.IsZero() {
		return deny()
	}
	return DispatchClaimObservation{
		OperationID: operationID, ClaimID: claimID,
		State: DispatchClaimOutcomeUnknown, ClaimedAt: claimedAt,
	}, nil
}
