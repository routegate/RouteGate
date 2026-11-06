package db

import (
	"crypto/ed25519"
	"errors"
	"strings"
	"testing"

	"github.com/ikaevus/routegate/backend/internal/auth"
	"github.com/ikaevus/routegate/backend/internal/revocations"
)

func TestCredentialRemovalDispatchAdmissionIndependentSignature(t *testing.T) {
	f := newDispatchGateFixture(t)
	pinned, err := f.svc.InspectPreparedRemovalIntegrity(f.ctx, f.operation)
	if err != nil {
		t.Fatal(err)
	}
	u, _ := auth.UserFromContext(f.ctx)
	approval, err := f.svc.IssuePreparedRemovalDispatchApproval(f.ctx,
		revocations.PreparedRemovalDispatchApprovalInput{OperationID: f.operation, ConfirmSharedProcessRestart: true})
	if err != nil {
		t.Fatal(err)
	}
	const deployment = "00112233-4455-4677-8899-aabbccddeeff"
	public, private, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	authority, err := revocations.NewRemovalDispatchAuthority(deployment, public)
	if err != nil {
		t.Fatal(err)
	}
	decision := revocations.RemovalDispatchDecision{
		Audience: deployment, OperationID: f.operation, AccountID: pinned.AccountID,
		ServerID: pinned.ServerID, AgentID: pinned.AgentID, AgentGeneration: pinned.AgentGeneration,
		ActorID: u.ID, SessionID: u.SessionID, ApprovalNonce: f.nonce,
		BaselineVersionID: pinned.BaselineVersionID, BaselineHash: pinned.BaselineHash,
		BaselineRuntimeHash: pinned.BaselineRuntimeHash, CandidateVersionID: approval.CandidateVersionID,
		ApplyJobID: approval.ApplyJobID, CandidateHash: approval.CandidateHash,
		CandidateRuntimeHash: approval.CandidateRuntimeHash, IssuedAtUnixMicros: approval.IssuedAt.UnixMicro(),
		ExpiresAtUnixMicros: approval.ExpiresAt.UnixMicro(),
	}
	sign := func(d revocations.RemovalDispatchDecision, key ed25519.PrivateKey) []byte {
		t.Helper()
		data, err := d.SigningBytes()
		if err != nil {
			t.Fatal(err)
		}
		return ed25519.Sign(key, data)
	}
	check := func(signature []byte) error {
		return f.svc.CheckSignedPreparedRemovalDispatchAdmission(f.ctx, f.operation, f.nonce, authority, signature)
	}
	expectRefusal := func(signature []byte) {
		t.Helper()
		if err := check(signature); !errors.Is(err, revocations.ErrPreflight) {
			t.Fatalf("untrusted dispatch proof admitted: %v", err)
		}
	}
	if err := f.svc.CheckPreparedRemovalDispatchAdmission(f.ctx, f.operation, f.nonce, true); err != nil {
		t.Fatal("fixture must have an independently valid SQL approval", err)
	}
	expectRefusal(nil)
	_, untrusted, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	expectRefusal(sign(decision, untrusted))
	for name, mutate := range map[string]func(*revocations.RemovalDispatchDecision){
		"deployment":       func(d *revocations.RemovalDispatchDecision) { d.Audience = d.ServerID },
		"operation":        func(d *revocations.RemovalDispatchDecision) { d.OperationID = d.AccountID },
		"target":           func(d *revocations.RemovalDispatchDecision) { d.AccountID = d.ServerID },
		"node":             func(d *revocations.RemovalDispatchDecision) { d.ServerID = d.AccountID },
		"agent":            func(d *revocations.RemovalDispatchDecision) { d.AgentID = d.ServerID },
		"generation":       func(d *revocations.RemovalDispatchDecision) { d.AgentGeneration++ },
		"human":            func(d *revocations.RemovalDispatchDecision) { d.ActorID = d.SessionID },
		"session":          func(d *revocations.RemovalDispatchDecision) { d.SessionID = d.ActorID },
		"nonce":            func(d *revocations.RemovalDispatchDecision) { d.ApprovalNonce = strings.Repeat("0", 64) },
		"baseline ID":      func(d *revocations.RemovalDispatchDecision) { d.BaselineVersionID = d.ApplyJobID },
		"baseline hash":    func(d *revocations.RemovalDispatchDecision) { d.BaselineHash = d.CandidateHash },
		"baseline runtime": func(d *revocations.RemovalDispatchDecision) { d.BaselineRuntimeHash = d.CandidateRuntimeHash },
		"candidate ID":     func(d *revocations.RemovalDispatchDecision) { d.CandidateVersionID = d.ApplyJobID },
		"job":              func(d *revocations.RemovalDispatchDecision) { d.ApplyJobID = d.CandidateVersionID },
		"candidate hash":   func(d *revocations.RemovalDispatchDecision) { d.CandidateHash = d.BaselineHash },
		"runtime hash":     func(d *revocations.RemovalDispatchDecision) { d.CandidateRuntimeHash = d.BaselineRuntimeHash },
		"issued at":        func(d *revocations.RemovalDispatchDecision) { d.IssuedAtUnixMicros-- },
		"expiry":           func(d *revocations.RemovalDispatchDecision) { d.ExpiresAtUnixMicros++ },
	} {
		t.Run(name, func(t *testing.T) {
			changed := decision
			mutate(&changed)
			if err := check(sign(changed, private)); !errors.Is(err, revocations.ErrPreflight) {
				t.Fatalf("mismatched dispatch proof admitted: %v", err)
			}
		})
	}
	signature := sign(decision, private)
	if err := check(signature); err != nil {
		t.Fatal("independently signed decision refused", err)
	}
	if _, err := f.pool.Exec(f.ctx, "UPDATE auth_sessions SET revoked_at=clock_timestamp() WHERE id=$1::uuid", f.session); err != nil {
		t.Fatal(err)
	}
	expectRefusal(signature)
	var executable bool
	if err := f.pool.QueryRow(f.ctx, `SELECT
 EXISTS(SELECT 1 FROM config_versions WHERE id=$1::uuid) OR
 EXISTS(SELECT 1 FROM config_apply_jobs WHERE id=$2::uuid)`,
		approval.CandidateVersionID, approval.ApplyJobID).Scan(&executable); err != nil || executable {
		t.Fatal("signature check allocated an executable record", err)
	}
}
