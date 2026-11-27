package db

import (
	"bytes"
	"crypto/ed25519"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strconv"
	"testing"

	"github.com/ikaevus/routegate/backend/internal/agents"
)

func TestCredentialRemovalManagerAgentReality(t *testing.T) {
	agentBinary, singBox := os.Getenv("ROUTEGATE_TEST_REMOVAL_AGENT_BINARY"), os.Getenv("ROUTEGATE_TEST_SING_BOX")
	if os.Getenv("ROUTEGATE_TEST_HELD_ROLE_REHEARSAL") != "1" || agentBinary == "" || singBox == "" || os.Getenv("ROUTEGATE_TEST_REMOVAL_SYSTEMD") != "1" {
		t.Skip("requires the disposable Manager/Agent systemd runner")
	}
	if os.Geteuid() != 0 {
		t.Fatal("isolated systemd test requires root")
	}
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
	ctx, pool, in, done := preparedRemovalRealityFixture(t, strconv.Itoa(port))
	f := dispatchGateFromPreparation(t, ctx, pool, in, "", done, agents.HashToken(token))
	if _, err := pool.Exec(ctx, `UPDATE agents SET capabilities=capabilities || '{"credentialRemovalV1":true,"runtimeMutationFencingV1":true}'::jsonb WHERE id=$1::uuid`, f.agent); err != nil {
		t.Fatal(err)
	}
	f, authority, decision, private := signedRemovalFixtureFromGate(t, f)
	installHeldIssuanceFixture(t, f)
	sql, err := os.ReadFile("testdata/held_dispatch/exchange.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.pool.Exec(f.ctx, string(sql)); err != nil {
		t.Fatal(err)
	}
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
	// Pin expected data from the real claim; the child claims again over HTTP.
	task, err := f.svc.ClaimHeldRemoval(f.ctx, agents.HashToken(token))
	if err != nil || task == nil {
		t.Fatal("initial held claim", err)
	}
	var envelope struct {
		SingBox json.RawMessage `json:"singBox"`
	}
	if err = json.Unmarshal(baseline, &envelope); err != nil {
		t.Fatal(err)
	}
	input, err := json.Marshal(struct {
		ManagerURL, Token string
		Fixture           any
	}{httpManager.URL, token, struct {
		BaselineRuntime json.RawMessage           `json:"baselineRuntime"`
		PublicKey       string                    `json:"publicKey"`
		Task            *agents.AgentDeliveryTask `json:"task"`
	}{envelope.SingBox, publicKey, task}})
	if err != nil {
		t.Fatal(err)
	}
	child := exec.CommandContext(f.ctx, agentBinary, "-test.run=^TestCredentialRemovalRealManagerChild$", "-test.v", "-test.timeout=75s")
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
