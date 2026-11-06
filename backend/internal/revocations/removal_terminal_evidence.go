package revocations

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"time"

	"github.com/jackc/pgx/v5"
)

const removalVerificationMaxAge = 2 * time.Minute

// RemovalVerificationAuthority is pinned by the protected coordinator outside
// the runtime database. It must use a different key from dispatch approval.
type RemovalVerificationAuthority struct {
	audience string
	key      ed25519.PublicKey
}

func NewRemovalVerificationAuthority(audience string, key ed25519.PublicKey) (RemovalVerificationAuthority, error) {
	if !evidenceID.MatchString(audience) || len(key) != ed25519.PublicKeySize {
		return RemovalVerificationAuthority{}, ErrPreflight
	}
	return RemovalVerificationAuthority{audience: audience, key: bytes.Clone(key)}, nil
}

const (
	RemovalRemainingAccountReconnected = "remaining_account_reconnected"
	RemovalEmptyListenerVerified       = "empty_listener_verified"
)

// RemovalVerificationProof binds the independent traffic observations to one
// exact Agent report. AgentReportDigest uses the v1 canonical JSON digest.
// The verifier holds the pre-removal test stream and tests the cached UUID
// against the same node. No credential or probe transcript is carried here.
type RemovalVerificationProof struct {
	Audience                 string `json:"audience"`
	OperationID              string `json:"operationId"`
	AgentReportDigest        string `json:"agentReportDigest"`
	ObservedAtUnixMicros     int64  `json:"observedAtUnixMicros"`
	OldSessionTerminated     bool   `json:"oldSessionTerminated"`
	CachedCredentialRejected bool   `json:"cachedCredentialRejected"`
	Continuity               string `json:"continuity"`
}

func (p RemovalVerificationProof) SigningBytes() ([]byte, error) {
	data, err := json.Marshal(p)
	if err != nil {
		return nil, err
	}
	return append([]byte("routegate/v1/verify-vless-removal-traffic\x00"), data...), nil
}

// CheckHeldRemovalTerminalEvidence checks the two sources of evidence in one
// read-only snapshot. The eventual terminal writer must repeat the checks
// under its write locks. Success here grants no authority and retains the hold.
func (s *Service) CheckHeldRemovalTerminalEvidence(ctx context.Context, proof RemovalVerificationProof, authority RemovalVerificationAuthority, signature []byte) error {
	if err := verifyRemovalVerificationSignature(proof, authority, signature); err != nil {
		return err
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	if err := verifyHeldRemovalTerminalEvidence(ctx, tx, proof, authority); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func verifyRemovalVerificationSignature(proof RemovalVerificationProof, authority RemovalVerificationAuthority, signature []byte) error {
	if !evidenceID.MatchString(proof.OperationID) || proof.Audience != authority.audience ||
		!validRemovalHash(proof.AgentReportDigest) || !proof.OldSessionTerminated || !proof.CachedCredentialRejected ||
		len(authority.key) != ed25519.PublicKeySize || len(signature) != ed25519.SignatureSize {
		return ErrPreflight
	}
	message, err := proof.SigningBytes()
	if err != nil || !ed25519.Verify(authority.key, message, signature) {
		return ErrPreflight
	}
	return nil
}

func verifyHeldRemovalTerminalEvidence(ctx context.Context, tx pgx.Tx, proof RemovalVerificationProof, authority RemovalVerificationAuthority) error {
	pinned, err := inspectPreparedRemovalIntegrity(ctx, tx, proof.OperationID)
	if err != nil {
		return ErrPreflight
	}
	held, err := readHeldAgentRemoval(ctx, tx, pinned.AgentID)
	if err != nil || held.binding.OperationID != proof.OperationID {
		return ErrPreflight
	}
	var report, dispatchKey []byte
	var audience string
	var received, now time.Time
	var remaining bool
	err = tx.QueryRow(ctx, `
SELECT e.evidence, h.verifier_key, h.audience::text, e.received_at, clock_timestamp(),
 jsonb_path_exists(v.rendered_config, '$.singBox.inbounds[*] ? (@.type == "vless").users[0]')
FROM routegate_dispatch.removal_results e
JOIN routegate_dispatch.held_removals h USING(operation_id)
JOIN public.config_versions v ON v.id=h.version_id
WHERE e.operation_id=$1::uuid`, proof.OperationID).Scan(&report, &dispatchKey, &audience, &received, &now, &remaining)
	if err != nil || audience != authority.audience || bytes.Equal(dispatchKey, authority.key) {
		return ErrPreflight
	}
	if _, err := VerifySuccess(report, held.binding); err != nil {
		return ErrPreflight
	}
	digest, err := RuntimeDigest(report)
	observed := time.UnixMicro(proof.ObservedAtUnixMicros)
	continuity := RemovalEmptyListenerVerified
	if remaining {
		continuity = RemovalRemainingAccountReconnected
	}
	if err != nil || digest != proof.AgentReportDigest || observed.Before(received) ||
		observed.After(now) || now.Sub(observed) > removalVerificationMaxAge || proof.Continuity != continuity {
		return ErrPreflight
	}
	return nil
}
