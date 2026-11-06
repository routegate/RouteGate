package revocations

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"crypto/subtle"
	"errors"

	"github.com/ikaevus/routegate/backend/internal/auth"
	"github.com/jackc/pgx/v5"
)

type HeldRemovalDispatch struct {
	VersionID     string
	JobID         string
	VersionNumber int
}

// IssueHeldRemovalDispatch is for the isolated trusted coordinator connection.
// The ordinary runtime role cannot read or write the issuance schema. A held
// job is invisible to normal Agent polling and has no execution authority.
func (s *Service) IssueHeldRemovalDispatch(ctx context.Context, operationID, nonce string, authority RemovalDispatchAuthority, signature []byte) (HeldRemovalDispatch, error) {
	var zero HeldRemovalDispatch
	u, ok := auth.UserFromContext(ctx)
	if !ok || !validRecoveryActor(u) || !evidenceID.MatchString(operationID) ||
		!validRemovalHash(nonce) || len(authority.key) != ed25519.PublicKeySize || len(signature) != ed25519.SignatureSize {
		return zero, ErrPreflight
	}
	pinned, err := s.InspectPreparedRemovalIntegrity(ctx, operationID)
	if err != nil {
		return zero, ErrPreflight
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return zero, err
	}
	defer tx.Rollback(ctx)
	var next int
	if err := tx.QueryRow(ctx, `SELECT next_config_version FROM servers WHERE id=$1::uuid FOR UPDATE`,
		pinned.ServerID).Scan(&next); err != nil {
		return zero, ErrPreflight
	}
	digest := sha256.Sum256(signature)
	var receipt HeldRemovalDispatch
	var savedDigest []byte
	var actor, session, savedNonce, audience string
	var savedKey []byte
	err = tx.QueryRow(ctx, `SELECT h.version_id::text,h.job_id::text,h.version_number,h.signature_digest,
 h.audience::text,h.verifier_key,a.actor_user_id::text,a.actor_session_id::text,a.nonce
 FROM routegate_dispatch.held_removals h JOIN vpn_credential_removal_dispatch_authorizations a USING (operation_id)
 WHERE h.operation_id=$1::uuid AND h.server_id=$2::uuid`, operationID, pinned.ServerID).Scan(
		&receipt.VersionID, &receipt.JobID, &receipt.VersionNumber, &savedDigest, &audience, &savedKey, &actor, &session, &savedNonce)
	if err == nil {
		if actor != u.ID || session != u.SessionID || audience != authority.audience ||
			subtle.ConstantTimeCompare(savedKey, authority.key) != 1 ||
			subtle.ConstantTimeCompare([]byte(savedNonce), []byte(nonce)) != 1 ||
			subtle.ConstantTimeCompare(savedDigest, digest[:]) != 1 ||
			lockRecoveryAuthority(ctx, tx, u) != nil {
			return zero, ErrPreflight
		}
		if err := tx.Commit(ctx); err != nil {
			return zero, err
		}
		return receipt, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return zero, ErrPreflight
	}
	gate, err := validateSignedRemovalDispatchLocked(ctx, tx, pinned, u, nonce, authority, signature)
	if err != nil {
		return zero, ErrPreflight
	}
	err = tx.QueryRow(ctx, `INSERT INTO routegate_dispatch.held_removals
 (operation_id,server_id,version_id,job_id,version_number,signature_digest,audience,verifier_key)
 SELECT s.operation_id,$2::uuid,s.candidate_version_id,s.apply_job_id,$3,$4,$9::uuid,$10
 FROM vpn_credential_removal_dispatch_stages s
 JOIN vpn_credential_removal_dispatch_authorizations a USING (operation_id)
 WHERE s.operation_id=$1::uuid AND s.candidate_version_id=$5::uuid AND s.apply_job_id=$6::uuid
 AND s.actor_user_id=$7::uuid AND s.actor_session_id=$8::uuid
 AND a.expires_at>clock_timestamp()
 RETURNING version_id::text,job_id::text,version_number`, operationID, gate.ServerID, next,
		digest[:], gate.VersionID, gate.JobID, u.ID, u.SessionID, authority.audience, []byte(authority.key)).Scan(
		&receipt.VersionID, &receipt.JobID, &receipt.VersionNumber)
	if err != nil {
		return zero, ErrPreflight
	}
	if _, err := tx.Exec(ctx, `INSERT INTO config_versions (id,server_id,version,config_hash,status,rendered_config)
 SELECT $2::uuid,server_id,$3,candidate_hash,'held_for_revocation',candidate_config
 FROM vpn_credential_revocations WHERE id=$1::uuid`, operationID, receipt.VersionID, next); err != nil {
		return zero, ErrPreflight
	}
	if _, err := tx.Exec(ctx, `INSERT INTO config_apply_jobs (id,server_id,agent_id,config_version_id,action,status)
 VALUES ($1::uuid,$2::uuid,$3::uuid,$4::uuid,'apply','held_for_revocation')`,
		receipt.JobID, gate.ServerID, gate.AgentID, receipt.VersionID); err != nil {
		return zero, ErrPreflight
	}
	if _, err := tx.Exec(ctx, `UPDATE servers SET next_config_version=next_config_version+1 WHERE id=$1::uuid`, gate.ServerID); err != nil {
		return zero, ErrPreflight
	}
	if err := tx.Commit(ctx); err != nil {
		return zero, err
	}
	return receipt, nil
}
