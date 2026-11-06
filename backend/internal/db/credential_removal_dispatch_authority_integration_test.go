package db

import (
	"context"
	"crypto/ed25519"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/ikaevus/routegate/backend/internal/agents"
	"github.com/ikaevus/routegate/backend/internal/auth"
	"github.com/ikaevus/routegate/backend/internal/revocations"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

func newSignedRemovalDecision(t *testing.T) (dispatchGateFixture, revocations.RemovalDispatchAuthority, revocations.RemovalDispatchDecision, ed25519.PrivateKey) {
	return newSignedRemovalDecisionWithToken(t, "")
}

func newSignedRemovalDecisionWithToken(t *testing.T, tokenHash string) (dispatchGateFixture, revocations.RemovalDispatchAuthority, revocations.RemovalDispatchDecision, ed25519.PrivateKey) {
	return newSignedRemovalDecisionWithSourceSetup(t, tokenHash, nil)
}

func newSignedRemovalDecisionWithSourceSetup(t *testing.T, tokenHash string, setup func(context.Context, *pgxpool.Pool, string)) (dispatchGateFixture, revocations.RemovalDispatchAuthority, revocations.RemovalDispatchDecision, ed25519.PrivateKey) {
	t.Helper()
	f := newDispatchGateFixtureWithSourceSetup(t, tokenHash, setup)
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
	return f, authority, decision, private
}

func TestCredentialRemovalDispatchAdmissionIndependentSignature(t *testing.T) {
	f, authority, decision, private := newSignedRemovalDecision(t)
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
		decision.CandidateVersionID, decision.ApplyJobID).Scan(&executable); err != nil || executable {
		t.Fatal("signature check allocated an executable record", err)
	}
}

func TestCredentialRemovalDispatchAdmissionHeldIssuance(t *testing.T) {
	f, authority, decision, private := newSignedRemovalDecision(t)
	data, err := decision.SigningBytes()
	if err != nil {
		t.Fatal(err)
	}
	signature := ed25519.Sign(private, data)
	issue := func(svc *revocations.Service) (revocations.HeldRemovalDispatch, error) {
		return svc.IssueHeldRemovalDispatch(f.ctx, f.operation, f.nonce, authority, signature)
	}
	if _, err := issue(f.svc); !errors.Is(err, revocations.ErrPreflight) {
		t.Fatal("issuance without a stage was not refused", err)
	}
	if _, err := f.svc.StagePreparedCredentialRemoval(f.ctx, f.operation, f.nonce, true); err != nil {
		t.Fatal(err)
	}
	runtime, _ := runtimeRoleFixture(t, f.ctx, f.pool)
	if _, err := issue(revocations.NewService(runtime)); !errors.Is(err, revocations.ErrPreflight) {
		t.Fatal("ordinary runtime issued a signed job", err)
	}
	denySQL := func(want string, stmt string, args ...any) {
		t.Helper()
		_, err := runtime.Exec(f.ctx, stmt, args...)
		var pgErr *pgconn.PgError
		if !errors.As(err, &pgErr) || pgErr.Code != want {
			t.Fatalf("wanted SQLSTATE %s, got %v", want, err)
		}
	}
	denySQL("42501", "SELECT 1 FROM routegate_dispatch.held_removals")
	denySQL("42501", "INSERT INTO routegate_dispatch.held_removals DEFAULT VALUES")

	type state struct {
		next                     int
		receipts, versions, jobs int
		active                   string
	}
	snapshot := func() state {
		t.Helper()
		var s state
		if err := f.pool.QueryRow(f.ctx, `SELECT next_config_version,active_config_version_id::text,
 (SELECT count(*) FROM routegate_dispatch.held_removals),
 (SELECT count(*) FROM config_versions WHERE id=$2::uuid),
 (SELECT count(*) FROM config_apply_jobs WHERE id=$3::uuid)
 FROM servers WHERE id=$1::uuid`, decision.ServerID, decision.CandidateVersionID, decision.ApplyJobID).
			Scan(&s.next, &s.active, &s.receipts, &s.versions, &s.jobs); err != nil {
			t.Fatal(err)
		}
		return s
	}
	before := snapshot()
	if _, err := f.pool.Exec(f.ctx, `CREATE FUNCTION rg176_fixture_abort() RETURNS trigger
 LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'fixture counter abort'; END; $$;
 CREATE TRIGGER rg176_fixture_abort AFTER UPDATE OF next_config_version ON servers
 FOR EACH ROW EXECUTE FUNCTION rg176_fixture_abort()`); err != nil {
		t.Fatal(err)
	}
	if _, err := issue(f.svc); !errors.Is(err, revocations.ErrPreflight) || snapshot() != before {
		t.Fatal("failure after writes left a receipt, version, job or counter change", err)
	}
	if _, err := f.pool.Exec(f.ctx, "DROP TRIGGER rg176_fixture_abort ON servers; DROP FUNCTION rg176_fixture_abort()"); err != nil {
		t.Fatal(err)
	}
	type outcome struct {
		receipt revocations.HeldRemovalDispatch
		err     error
	}
	results := make(chan outcome, 2)
	for range 2 {
		go func() {
			receipt, err := issue(f.svc)
			results <- outcome{receipt, err}
		}()
	}
	want := revocations.HeldRemovalDispatch{VersionID: decision.CandidateVersionID, JobID: decision.ApplyJobID, VersionNumber: before.next}
	for range 2 {
		got := <-results
		if got.err != nil || got.receipt != want {
			t.Fatal("concurrent issuance failed", got.err)
		}
	}
	if got := snapshot(); got != (state{next: before.next + 1, active: before.active, receipts: 1, versions: 1, jobs: 1}) {
		t.Fatalf("wrong committed state: %+v", got)
	}
	var exact bool
	if err := f.pool.QueryRow(f.ctx, `SELECT v.rendered_config=r.candidate_config
 AND v.config_hash=r.candidate_hash AND v.applied_at IS NULL
 AND v.status='held_for_revocation' AND j.status='held_for_revocation' AND j.agent_id=r.agent_id
 FROM vpn_credential_revocations r CROSS JOIN config_versions v
 JOIN config_apply_jobs j ON j.config_version_id=v.id
 WHERE r.id=$1::uuid AND v.id=$2::uuid AND j.id=$3::uuid`,
		f.operation, decision.CandidateVersionID, decision.ApplyJobID).Scan(&exact); err != nil || !exact {
		t.Fatal("held job did not retain the exact candidate and Agent", err)
	}
	if job, err := agents.NewRepository(runtime).ClaimNextConfigTask(f.ctx, decision.ServerID); err != nil || job != nil {
		t.Fatal("generic Agent polling exposed the held removal", err)
	}
	denySQL("P0141", "UPDATE config_apply_jobs SET status='pending' WHERE id=$1::uuid", decision.ApplyJobID)
	denySQL("P0141", "UPDATE config_versions SET status='applied' WHERE id=$1::uuid", decision.CandidateVersionID)
	denySQL("P0141", "UPDATE servers SET next_config_version=next_config_version+1 WHERE id=$1::uuid", decision.ServerID)
	if _, err := f.svc.Cancel(f.ctx, f.operation, "isolated-operator"); err == nil {
		t.Fatal("generic cancellation released the held removal reservation")
	}
	downgradeEmptyRetirementGuard(t, f.ctx, f.pool)
	exchangeDown, err := os.ReadFile("../../migrations/000177_credential_removal_agent_exchange.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.pool.Exec(f.ctx, string(exchangeDown)); err != nil {
		t.Fatal("empty Agent exchange downgrade", err)
	}
	down, err := os.ReadFile("../../migrations/000176_credential_removal_held_dispatch.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(f.ctx, string(down)); err == nil {
		t.Fatal("downgrade erased a consumed approval")
	}
}
