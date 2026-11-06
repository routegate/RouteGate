package revocations

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/json"

	"github.com/ikaevus/routegate/backend/internal/auth"
	"github.com/jackc/pgx/v5"
)

// RemovalDispatchAuthority pins the verifier key and deployment identity.
// Both must come from the trusted coordinator, outside Manager's runtime DB.
type RemovalDispatchAuthority struct {
	audience string
	key      ed25519.PublicKey
}

func NewRemovalDispatchAuthority(audience string, key ed25519.PublicKey) (RemovalDispatchAuthority, error) {
	if !evidenceID.MatchString(audience) || len(key) != ed25519.PublicKeySize {
		return RemovalDispatchAuthority{}, ErrPreflight
	}
	return RemovalDispatchAuthority{audience: audience, key: bytes.Clone(key)}, nil
}

// RemovalDispatchDecision is the exact decision an independent authority signs
// after authenticating the human and obtaining explicit shared restart consent.
// It contains no VPN credential. The nonce binds the existing SQL approval.
type RemovalDispatchDecision struct {
	Audience             string `json:"audience"`
	OperationID          string `json:"operationId"`
	AccountID            string `json:"accountId"`
	ServerID             string `json:"serverId"`
	AgentID              string `json:"agentId"`
	AgentGeneration      int64  `json:"agentGeneration"`
	ActorID              string `json:"actorId"`
	SessionID            string `json:"sessionId"`
	ApprovalNonce        string `json:"approvalNonce"`
	BaselineVersionID    string `json:"baselineVersionId"`
	BaselineHash         string `json:"baselineHash"`
	BaselineRuntimeHash  string `json:"baselineRuntimeHash"`
	CandidateVersionID   string `json:"candidateVersionId"`
	ApplyJobID           string `json:"applyJobId"`
	CandidateHash        string `json:"candidateHash"`
	CandidateRuntimeHash string `json:"candidateRuntimeHash"`
	IssuedAtUnixMicros   int64  `json:"issuedAtUnixMicros"`
	ExpiresAtUnixMicros  int64  `json:"expiresAtUnixMicros"`
}

func (d RemovalDispatchDecision) SigningBytes() ([]byte, error) {
	data, err := json.Marshal(d)
	if err != nil {
		return nil, err
	}
	return append([]byte("routegate/v1/authorize-vless-removal-with-shared-restart\x00"), data...), nil
}

// CheckSignedPreparedRemovalDispatchAdmission rehearses the independent proof
// gate without allocating a task or granting authority to a later transaction.
func (s *Service) CheckSignedPreparedRemovalDispatchAdmission(ctx context.Context, operationID, nonce string, authority RemovalDispatchAuthority, signature []byte) error {
	u, ok := auth.UserFromContext(ctx)
	if !ok || !validRecoveryActor(u) || !evidenceID.MatchString(operationID) ||
		!validRemovalHash(nonce) || len(authority.key) != ed25519.PublicKeySize || len(signature) != ed25519.SignatureSize {
		return ErrPreflight
	}
	pinned, err := s.InspectPreparedRemovalIntegrity(ctx, operationID)
	if err != nil {
		return ErrPreflight
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err := validateSignedRemovalDispatchLocked(ctx, tx, pinned, u, nonce, authority, signature); err != nil {
		return ErrPreflight
	}
	return tx.Commit(ctx)
}

func validateSignedRemovalDispatchLocked(ctx context.Context, tx pgx.Tx, pinned PreparedRemovalIntegrity, u auth.AuthenticatedUser, nonce string, authority RemovalDispatchAuthority, signature []byte) (preparedRemovalDispatchGate, error) {
	gate, err := validatePreparedRemovalDispatchLocked(ctx, tx, pinned, u, nonce)
	if err != nil {
		return preparedRemovalDispatchGate{}, err
	}
	decision := RemovalDispatchDecision{
		Audience: authority.audience, OperationID: gate.OperationID, AccountID: pinned.AccountID,
		ServerID: gate.ServerID, AgentID: gate.AgentID, AgentGeneration: gate.Generation,
		ActorID: u.ID, SessionID: u.SessionID, ApprovalNonce: nonce,
		BaselineVersionID: pinned.BaselineVersionID, BaselineHash: pinned.BaselineHash,
		BaselineRuntimeHash: pinned.BaselineRuntimeHash, CandidateVersionID: gate.VersionID,
		ApplyJobID: gate.JobID, CandidateHash: gate.CandidateHash, CandidateRuntimeHash: gate.RuntimeHash,
		IssuedAtUnixMicros: gate.IssuedAt.UnixMicro(), ExpiresAtUnixMicros: gate.ExpiresAt.UnixMicro(),
	}
	message, err := decision.SigningBytes()
	if err != nil || len(authority.key) != ed25519.PublicKeySize || !ed25519.Verify(authority.key, message, signature) {
		return preparedRemovalDispatchGate{}, ErrPreflight
	}
	return gate, nil
}
