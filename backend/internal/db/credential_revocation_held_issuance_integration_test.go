package db

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"github.com/ikaevus/routegate/backend/internal/agents"
	"github.com/ikaevus/routegate/backend/internal/revocations"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"os"
	"testing"
	"time"
)

func TestCredentialRemovalDispatchAdmissionHeldIssuance(t *testing.T) {
	if os.Getenv("ROUTEGATE_TEST_HELD_ROLE_REHEARSAL") != "1" {
		t.Skip("disposable role rehearsal is opt-in")
	}
	f, authority, decision, private := signedRemovalFixture(t)
	installHeldIssuanceFixture(t, f)
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
	runtime, _ := heldIssuanceRuntime(t, f)
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
	forged := append([]byte(nil), signature...)
	forged[0] ^= 1
	if _, err := f.svc.IssueHeldRemovalDispatch(f.ctx, f.operation, f.nonce, authority, forged); !errors.Is(err, revocations.ErrPreflight) || snapshot() != before {
		t.Fatal("forged decision allocated held work", err)
	}

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
	var tokenHash string
	if err := f.pool.QueryRow(f.ctx, `SELECT token_hash FROM agents WHERE id=$1::uuid`, decision.AgentID).Scan(&tokenHash); err != nil {
		t.Fatal(err)
	}
	if job, err := agents.NewRepository(runtime).ClaimNextConfigTask(f.ctx, tokenHash); err != nil || job != nil {
		t.Fatal("generic Agent polling exposed the held removal", err)
	}
	denySQL("P0141", "UPDATE config_apply_jobs SET status='pending' WHERE id=$1::uuid", decision.ApplyJobID)
	denySQL("P0141", "UPDATE config_versions SET status='applied' WHERE id=$1::uuid", decision.CandidateVersionID)
	denySQL("P0141", "UPDATE servers SET next_config_version=next_config_version+1 WHERE id=$1::uuid", decision.ServerID)
	if _, err := f.svc.Cancel(f.ctx, f.operation, "isolated-operator"); err == nil {
		t.Fatal("generic cancellation released the held removal reservation")
	}
	// An exact retry after a lost response reads the original receipt only.
	committed := snapshot()
	if receipt, err := issue(f.svc); err != nil || receipt != want || snapshot() != committed {
		t.Fatal("receipt readback reissued work", err)
	}
	// Replay still requires the current human authority, not stale context roles.
	if _, err := f.pool.Exec(f.ctx, `DELETE FROM user_roles WHERE user_id=$1::uuid`, decision.ActorID); err != nil {
		t.Fatal(err)
	}
	if _, err := issue(f.svc); !errors.Is(err, revocations.ErrPreflight) || snapshot() != committed {
		t.Fatal("revoked human authority read back an issuance", err)
	}
	down, err := os.ReadFile("testdata/held_dispatch/issuance.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(f.ctx, string(down)); err == nil {
		t.Fatal("downgrade erased a consumed approval")
	}
}

func installHeldIssuanceFixture(t *testing.T, f dispatchGateFixture) {
	t.Helper()
	var database string
	var superuser bool
	if err := f.pool.QueryRow(f.ctx, `SELECT current_database(), (SELECT rolsuper FROM pg_roles WHERE rolname=current_user)`).Scan(&database, &superuser); err != nil || database != "routegate_ci" || !superuser {
		t.Fatal("requires disposable routegate_ci with CI administrator", err)
	}
	data, err := os.ReadFile("testdata/held_dispatch/issuance.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.pool.Exec(f.ctx, string(data)); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if _, err := f.pool.Exec(ctx, `DROP SCHEMA routegate_dispatch CASCADE`); err != nil {
			t.Error(err)
		}
	})
}

func heldIssuanceRuntime(t *testing.T, f dispatchGateFixture) (*pgxpool.Pool, string) {
	t.Helper()
	const role = "rg140_test_issuance_runtime"
	var raw [32]byte
	if _, err := rand.Read(raw[:]); err != nil {
		t.Fatal(err)
	}
	password := hex.EncodeToString(raw[:])
	if err := ProvisionRuntimeLogin(f.ctx, f.pool, role, password); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if _, err := f.pool.Exec(ctx, `DROP OWNED BY rg140_test_issuance_runtime; DROP ROLE rg140_test_issuance_runtime`); err != nil {
			t.Error(err)
		}
	})
	cfg, err := pgxpool.ParseConfig(os.Getenv("ROUTEGATE_TEST_DATABASE_URL"))
	if err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig.User = role
	cfg.ConnConfig.Password = password
	runtime, err := pgxpool.NewWithConfig(f.ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(runtime.Close)
	if err := runtime.Ping(f.ctx); err != nil {
		t.Fatal(err)
	}
	if err := CheckHeldDispatchRuntimeIsolation(f.ctx, runtime); err != nil {
		t.Fatal(err)
	}
	return runtime, role
}
