package db

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/tls"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"testing"

	"github.com/ikaevus/routegate/backend/internal/agents"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestCredentialRemovalManagerAgentReality(t *testing.T) {
	agentBinary, singBox := os.Getenv("ROUTEGATE_TEST_REMOVAL_AGENT_BINARY"), os.Getenv("ROUTEGATE_TEST_REMOVAL_SING_BOX")
	if agentBinary == "" || singBox == "" || os.Getenv("ROUTEGATE_TEST_REMOVAL_SYSTEMD") != "1" {
		t.Skip("requires the disposable Manager/Agent systemd runner")
	}
	if os.Geteuid() != 0 {
		t.Fatal("isolated systemd test requires root")
	}
	handshake, err := net.Listen("tcp4", "127.0.0.1:443")
	if err != nil {
		t.Fatal("allocate isolated Reality handshake target", err)
	}
	target := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "handshake-only")
	}))
	_ = target.Listener.Close()
	target.Listener = handshake
	target.TLS = &tls.Config{MinVersion: tls.VersionTLS13, MaxVersion: tls.VersionTLS13, CurvePreferences: []tls.CurveID{tls.X25519}}
	target.StartTLS()
	defer target.Close()
	reserved, err := net.Listen("tcp6", "[::]:0")
	if err != nil {
		t.Fatal(err)
	}
	port := reserved.Addr().(*net.TCPAddr).Port
	_ = reserved.Close()
	token, err := agents.GenerateAgentToken()
	if err != nil {
		t.Fatal(err)
	}
	f, authority, decision, private := newSignedRemovalDecisionWithSourceSetup(t, agents.HashToken(token),
		func(ctx context.Context, pool *pgxpool.Pool, node string) {
			if _, err := pool.Exec(ctx, "UPDATE servers SET reality_server_name='localhost',vless_port=$2 WHERE id=$1::uuid", node, port); err != nil {
				t.Fatal("configure isolated Reality listener", err)
			}
			if _, err := pool.Exec(ctx, `UPDATE agents SET capabilities=capabilities ||
 '{"credentialRemovalV1":true,"runtimeMutationFencingV1":true}'::jsonb WHERE server_id=$1::uuid`, node); err != nil {
				t.Fatal(err)
			}
		})
	var baseline json.RawMessage
	var publicKey string
	if err := f.pool.QueryRow(f.ctx, `SELECT v.rendered_config,s.reality_public_key FROM servers s
 JOIN config_versions v ON v.id=s.active_config_version_id WHERE s.id=$1::uuid`,
		decision.ServerID).Scan(&baseline, &publicKey); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.StagePreparedCredentialRemoval(f.ctx, f.operation, f.nonce, true); err != nil {
		t.Fatal(err)
	}
	message, err := decision.SigningBytes()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.IssueHeldRemovalDispatch(f.ctx, f.operation, f.nonce, authority, ed25519.Sign(private, message)); err != nil {
		t.Fatal(err)
	}
	manager := agents.NewHandler(slog.New(slog.NewTextHandler(io.Discard, nil)), f.pool)
	mux := http.NewServeMux()
	manager.MountHeldRemoval(mux, f.svc)
	httpManager := httptest.NewServer(mux)
	defer httpManager.Close()
	input, err := json.Marshal(struct {
		ManagerURL, Token, AccountID, PublicKey, JobID string
		Baseline                                       json.RawMessage
	}{httpManager.URL, token, decision.AccountID, publicKey, decision.ApplyJobID, baseline})
	if err != nil {
		t.Fatal(err)
	}
	child := exec.CommandContext(f.ctx, agentBinary, "-test.run=^TestCredentialRemovalRealManagerChild$", "-test.v", "-test.timeout=45s")
	child.Env = append(os.Environ(), "ROUTEGATE_TEST_REMOVAL_MANAGER_CHILD=1")
	child.Stdin = bytes.NewReader(input)
	out, err := child.CombinedOutput()
	if err != nil {
		t.Fatalf("isolated Agent/Reality child failed: %v\n%s", err, out)
	}
	t.Logf("%s", out)
	var retained bool
	if err := f.pool.QueryRow(f.ctx, `SELECT r.state='prepared' AND j.status='held_for_revocation'
 AND s.active_config_version_id=r.baseline_version_id
 AND e.evidence->>'jobId'=h.job_id::text
 AND e.evidence->>'activeRuntimeHash'=r.candidate_runtime_hash
 FROM routegate_dispatch.removal_results e JOIN routegate_dispatch.held_removals h USING(operation_id)
 JOIN vpn_credential_revocations r ON r.id=h.operation_id JOIN config_apply_jobs j ON j.id=h.job_id
 JOIN servers s ON s.id=r.server_id WHERE h.operation_id=$1::uuid`, f.operation).Scan(&retained); err != nil || !retained {
		t.Fatal("Manager did not retain exact evidence, baseline pointer and reservation", err)
	}
}
