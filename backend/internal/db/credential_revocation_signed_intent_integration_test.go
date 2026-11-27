package db

import (
	"crypto/ed25519"
	"errors"
	"strings"
	"testing"

	"github.com/ikaevus/routegate/backend/internal/auth"
	"github.com/ikaevus/routegate/backend/internal/revocations"
)

// Adapted from historical #713 signature-contract tests. The verifier runs
// against today's 168–170 PostgreSQL consent/stage ledgers, and issues no
// config version, task, Agent lease or network change.
func TestCredentialRemovalIndependentSignedIntentIsReadOnly(t *testing.T) {
	f, authority, decision, private := signedRemovalFixture(t)
	sign := func(d revocations.RemovalDispatchDecision, key ed25519.PrivateKey) []byte {
		t.Helper()
		bytes, e := d.SigningBytes()
		if e != nil {
			t.Fatal(e)
		}
		return ed25519.Sign(key, bytes)
	}
	check := func(signature []byte) error {
		return f.svc.CheckSignedPreparedRemovalDispatchAdmission(
			f.ctx, f.operation, f.nonce, authority, signature)
	}
	if e := f.svc.CheckPreparedRemovalDispatchAdmission(f.ctx, f.operation, f.nonce, true); e != nil {
		t.Fatal("unsigned admission fixture invalid", e)
	}
	if e := check(nil); !errors.Is(e, revocations.ErrPreflight) {
		t.Fatal("missing signature was accepted", e)
	}
	_, untrusted, e := ed25519.GenerateKey(nil)
	if e != nil {
		t.Fatal(e)
	}
	if e = check(sign(decision, untrusted)); !errors.Is(e, revocations.ErrPreflight) {
		t.Fatal("wrong independent signer's key was accepted", e)
	}
	for name, mutate := range map[string]func(*revocations.RemovalDispatchDecision){
		"audience":               func(d *revocations.RemovalDispatchDecision) { d.Audience = d.ServerID },
		"operation":              func(d *revocations.RemovalDispatchDecision) { d.OperationID = d.AccountID },
		"account":                func(d *revocations.RemovalDispatchDecision) { d.AccountID = d.ServerID },
		"node":                   func(d *revocations.RemovalDispatchDecision) { d.ServerID = d.AccountID },
		"agent":                  func(d *revocations.RemovalDispatchDecision) { d.AgentID = d.ServerID },
		"agent_generation":       func(d *revocations.RemovalDispatchDecision) { d.AgentGeneration++ },
		"human":                  func(d *revocations.RemovalDispatchDecision) { d.ActorID = d.SessionID },
		"session":                func(d *revocations.RemovalDispatchDecision) { d.SessionID = d.ActorID },
		"nonce":                  func(d *revocations.RemovalDispatchDecision) { d.ApprovalNonce = strings.Repeat("0", 64) },
		"baseline_id":            func(d *revocations.RemovalDispatchDecision) { d.BaselineVersionID = d.ApplyJobID },
		"baseline_hash":          func(d *revocations.RemovalDispatchDecision) { d.BaselineHash = d.CandidateHash },
		"baseline_runtime_hash":  func(d *revocations.RemovalDispatchDecision) { d.BaselineRuntimeHash = d.CandidateRuntimeHash },
		"candidate_id":           func(d *revocations.RemovalDispatchDecision) { d.CandidateVersionID = d.ApplyJobID },
		"job_id":                 func(d *revocations.RemovalDispatchDecision) { d.ApplyJobID = d.CandidateVersionID },
		"candidate_hash":         func(d *revocations.RemovalDispatchDecision) { d.CandidateHash = d.BaselineHash },
		"candidate_runtime_hash": func(d *revocations.RemovalDispatchDecision) { d.CandidateRuntimeHash = d.BaselineRuntimeHash },
		"issue_time":             func(d *revocations.RemovalDispatchDecision) { d.IssuedAtUnixMicros-- },
		"expiry":                 func(d *revocations.RemovalDispatchDecision) { d.ExpiresAtUnixMicros++ },
	} {
		t.Run(name, func(t *testing.T) {
			changed := decision
			mutate(&changed)
			if e := check(sign(changed, private)); !errors.Is(e, revocations.ErrPreflight) {
				t.Fatal("modified signed intent unexpectedly matches original prepared operation", e)
			}
		})
	}
	if e := check(sign(decision, private)); e != nil {
		t.Fatal("valid independent signature rejected", e)
	}
	if _, e = f.pool.Exec(f.ctx, `UPDATE auth_sessions SET revoked_at=clock_timestamp()
		WHERE id=$1::uuid`, f.session); e != nil {
		t.Fatal(e)
	}
	if e := check(sign(decision, private)); !errors.Is(e, revocations.ErrPreflight) {
		t.Fatal("valid signature survived human session revocation", e)
	}
	var versionCount, jobCount, stageCount int
	if e := f.pool.QueryRow(f.ctx, `SELECT
		(SELECT count(*) FROM config_versions WHERE id=$1::uuid),
		(SELECT count(*) FROM config_apply_jobs WHERE id=$2::uuid),
		(SELECT count(*) FROM vpn_credential_removal_dispatch_stages WHERE operation_id=$3::uuid)`,
		decision.CandidateVersionID, decision.ApplyJobID, f.operation).
		Scan(&versionCount, &jobCount, &stageCount); e != nil ||
		versionCount != 0 || jobCount != 0 || stageCount != 0 {
		t.Fatal("signed read-only admission created executable state", e)
	}
}

func signedRemovalFixture(t *testing.T) (dispatchGateFixture, revocations.RemovalDispatchAuthority, revocations.RemovalDispatchDecision, ed25519.PrivateKey) {
	return signedRemovalFixtureWithToken(t, "")
}

func signedRemovalFixtureWithToken(t *testing.T, tokenHash string) (dispatchGateFixture, revocations.RemovalDispatchAuthority, revocations.RemovalDispatchDecision, ed25519.PrivateKey) {
	t.Helper()
	return signedRemovalFixtureFromGate(t, newDispatchGateFixtureWithToken(t, tokenHash))
}

func signedRemovalFixtureFromGate(t *testing.T, f dispatchGateFixture) (dispatchGateFixture, revocations.RemovalDispatchAuthority, revocations.RemovalDispatchDecision, ed25519.PrivateKey) {
	t.Helper()
	pinned, err := f.svc.InspectPreparedRemovalIntegrity(f.ctx, f.operation)
	if err != nil {
		t.Fatal(err)
	}
	u, ok := auth.UserFromContext(f.ctx)
	if !ok {
		t.Fatal("expected original human identity")
	}
	approval, err := f.svc.IssuePreparedRemovalDispatchApproval(f.ctx,
		revocations.PreparedRemovalDispatchApprovalInput{
			OperationID: f.operation, ConfirmSharedProcessRestart: true,
		})
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
		BaselineRuntimeHash: pinned.BaselineRuntimeHash,
		CandidateVersionID:  approval.CandidateVersionID, ApplyJobID: approval.ApplyJobID,
		CandidateHash: approval.CandidateHash, CandidateRuntimeHash: approval.CandidateRuntimeHash,
		IssuedAtUnixMicros:  approval.IssuedAt.UnixMicro(),
		ExpiresAtUnixMicros: approval.ExpiresAt.UnixMicro(),
	}

	return f, authority, decision, private
}
