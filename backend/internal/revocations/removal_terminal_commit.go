package revocations

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

// ConfirmHeldRemoval is an internal operation for the trusted coordinator
// connection. The separately provisioned verifier attests to traffic on the
// exact held operation. An exact retry only returns the existing timestamp.
func (s *Service) ConfirmHeldRemoval(ctx context.Context, proof RemovalVerificationProof, authority RemovalVerificationAuthority, signature []byte) (time.Time, error) {
	if err := verifyRemovalVerificationSignature(proof, authority, signature); err != nil {
		return time.Time{}, err
	}
	body, err := json.Marshal(proof)
	if err != nil {
		return time.Time{}, err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return time.Time{}, err
	}
	defer tx.Rollback(ctx)

	// Agent claim, Agent result and the ordinary runtime writers serialize
	// on the same server row before any terminal changes.
	var node string
	if err := tx.QueryRow(ctx, `SELECT s.id::text FROM servers s
JOIN vpn_credential_revocations r ON r.server_id=s.id
WHERE r.id=$1::uuid FOR UPDATE OF s`, proof.OperationID).Scan(&node); err != nil {
		return time.Time{}, err
	}

	var confirmedAt time.Time
	err = tx.QueryRow(ctx, `SELECT confirmed_at FROM routegate_dispatch.confirmed_removals
WHERE operation_id=$1::uuid AND proof=$2::jsonb AND verifier_key=$3 AND signature=$4`,
		proof.OperationID, body, []byte(authority.key), signature).Scan(&confirmedAt)
	if err == nil {
		return confirmedAt, tx.Commit(ctx)
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return time.Time{}, err
	}

	// Take the retirement write lock before updates that would themselves take
	// SHARE locks. Revalidate the proof after waiting for those locks.
	if _, err := tx.Exec(ctx, `LOCK TABLE routegate_dispatch.retired_vless_credentials IN SHARE ROW EXCLUSIVE MODE`); err != nil {
		return time.Time{}, err
	}
	var account string
	if err := tx.QueryRow(ctx, `SELECT a.id::text FROM vpn_accounts a
JOIN vpn_credential_revocations r ON r.vpn_account_id=a.id
WHERE r.id=$1::uuid FOR UPDATE OF a,r`, proof.OperationID).Scan(&account); err != nil {
		return time.Time{}, err
	}
	if err := verifyHeldRemovalTerminalEvidence(ctx, tx, proof, authority); err != nil {
		return time.Time{}, err
	}
	if err := tx.QueryRow(ctx, `SELECT routegate_dispatch.confirm_held_removal($1::uuid,$2::jsonb,$3,$4)`,
		proof.OperationID, body, []byte(authority.key), signature).Scan(&confirmedAt); err != nil {
		return time.Time{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return time.Time{}, err
	}
	return confirmedAt, nil
}
