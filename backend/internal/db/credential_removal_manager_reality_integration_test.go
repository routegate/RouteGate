package db

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/tls"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/ikaevus/routegate/backend/internal/agents"
	"github.com/ikaevus/routegate/backend/internal/revocations"
	"github.com/jackc/pgx/v5/pgconn"
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
	// The isolated child has now observed the old stream close, challenged the
	// cached UUID and connected the second account to the real origin.
	proof, verifier, signature := checkRemovalTerminalEvidence(t, f, decision.Audience, private)
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
	testRemovalTerminalCommit(t, f, decision, proof, verifier, signature, token)
}

func checkRemovalTerminalEvidence(t *testing.T, f dispatchGateFixture, audience string, dispatchKey ed25519.PrivateKey) (revocations.RemovalVerificationProof, revocations.RemovalVerificationAuthority, []byte) {
	t.Helper()
	var report []byte
	var now time.Time
	if err := f.pool.QueryRow(f.ctx, `SELECT evidence,clock_timestamp()
FROM routegate_dispatch.removal_results WHERE operation_id=$1::uuid`, f.operation).Scan(&report, &now); err != nil {
		t.Fatal(err)
	}
	digest, err := revocations.RuntimeDigest(report)
	if err != nil {
		t.Fatal(err)
	}
	public, private, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	authority, err := revocations.NewRemovalVerificationAuthority(audience, public)
	if err != nil {
		t.Fatal(err)
	}
	proof := revocations.RemovalVerificationProof{
		Audience: audience, OperationID: f.operation, AgentReportDigest: digest, ObservedAtUnixMicros: now.UnixMicro(),
		OldSessionTerminated: true, CachedCredentialRejected: true, Continuity: revocations.RemovalRemainingAccountReconnected,
	}
	sign := func(p revocations.RemovalVerificationProof, key ed25519.PrivateKey) []byte {
		t.Helper()
		message, err := p.SigningBytes()
		if err != nil {
			t.Fatal(err)
		}
		return ed25519.Sign(key, message)
	}
	if err := f.svc.CheckHeldRemovalTerminalEvidence(f.ctx, proof, authority, sign(proof, private)); err != nil {
		t.Fatal("valid independently signed traffic evidence refused", err)
	}
	dispatchAuthority, err := revocations.NewRemovalVerificationAuthority(audience, dispatchKey.Public().(ed25519.PublicKey))
	if err != nil {
		t.Fatal(err)
	}
	if err := f.svc.CheckHeldRemovalTerminalEvidence(f.ctx, proof, dispatchAuthority, sign(proof, dispatchKey)); !errors.Is(err, revocations.ErrPreflight) {
		t.Fatal("dispatch signer was allowed to verify its own operation", err)
	}
	for _, tc := range []struct {
		name string
		edit func(*revocations.RemovalVerificationProof)
	}{
		{"wrong report", func(p *revocations.RemovalVerificationProof) { p.AgentReportDigest = digest[1:] + digest[:1] }},
		{"old session unverified", func(p *revocations.RemovalVerificationProof) { p.OldSessionTerminated = false }},
		{"cached credential unverified", func(p *revocations.RemovalVerificationProof) { p.CachedCredentialRejected = false }},
		{"wrong continuity", func(p *revocations.RemovalVerificationProof) { p.Continuity = revocations.RemovalEmptyListenerVerified }},
		{"future observation", func(p *revocations.RemovalVerificationProof) {
			p.ObservedAtUnixMicros = now.Add(time.Minute).UnixMicro()
		}},
		{"pre-result observation", func(p *revocations.RemovalVerificationProof) {
			p.ObservedAtUnixMicros = now.Add(-time.Minute).UnixMicro()
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			changed := proof
			tc.edit(&changed)
			if err := f.svc.CheckHeldRemovalTerminalEvidence(f.ctx, changed, authority, sign(changed, private)); !errors.Is(err, revocations.ErrPreflight) {
				t.Fatal("invalid traffic observation accepted", err)
			}
		})
	}
	return proof, authority, sign(proof, private)
}

func testRemovalTerminalCommit(t *testing.T, f dispatchGateFixture, decision revocations.RemovalDispatchDecision, proof revocations.RemovalVerificationProof, verifier revocations.RemovalVerificationAuthority, signature []byte, token string) {
	t.Helper()
	var originalUUID, remainingID, stale string
	if err := f.pool.QueryRow(f.ctx, `SELECT vless_uuid::text FROM vpn_accounts WHERE id=$1::uuid`, decision.AccountID).Scan(&originalUUID); err != nil {
		t.Fatal(err)
	}
	if err := f.pool.QueryRow(f.ctx, `SELECT id::text FROM vpn_accounts WHERE server_id=$1::uuid AND id<>$2::uuid`,
		decision.ServerID, decision.AccountID).Scan(&remainingID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(f.ctx, `WITH device AS (
 INSERT INTO vpn_account_devices(vpn_account_id) VALUES($1::uuid) RETURNING id
)
INSERT INTO vpn_subscription_tokens(vpn_account_id,token_hash,device_id)
VALUES ($1::uuid,'terminal-target-legacy',NULL),
       ($1::uuid,'terminal-target-device',(SELECT id FROM device)),
       ($2::uuid,'terminal-survivor',NULL)`, decision.AccountID, remainingID); err != nil {
		t.Fatal(err)
	}

	runtime, _ := runtimeRoleFixture(t, f.ctx, f.pool)
	var denied *pgconn.PgError
	_, err := runtime.Exec(f.ctx, `SELECT routegate_dispatch.confirm_held_removal($1::uuid,'{}'::jsonb,NULL,NULL)`, f.operation)
	if !errors.As(err, &denied) || denied.Code != "42501" {
		t.Fatal("ordinary runtime could invoke terminal writer", err)
	}

	// A stale active version on another node prevents retirement at commit,
	// after the coordinator has attempted all the terminal writes.
	if err := f.pool.QueryRow(f.ctx, `INSERT INTO config_versions(server_id,version,config_hash,status,rendered_config)
VALUES($1::uuid,1,'stale-terminal-test','applied',jsonb_build_object('vpnAccounts',
  jsonb_build_array(jsonb_build_object('vlessUuid',$2::text)))) RETURNING id::text`, f.target, originalUUID).Scan(&stale); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(f.ctx, `UPDATE servers SET active_config_version_id=$2::uuid WHERE id=$1::uuid`, f.target, stale); err != nil {
		t.Fatal(err)
	}
	_, err = f.svc.ConfirmHeldRemoval(f.ctx, proof, verifier, signature)
	if !errors.As(err, &denied) || denied.Code != "P0141" || denied.Message != "VLESS credential remains in an account, active config, or open job" {
		t.Fatal("expected terminal commit to fail at the retirement fence", err)
	}
	var rolledBack bool
	if err := f.pool.QueryRow(f.ctx, `SELECT r.state='prepared' AND j.status='held_for_revocation'
AND s.active_config_version_id=r.baseline_version_id AND a.vless_uuid=$2::uuid AND a.status=r.account_status
AND (SELECT count(*) FROM vpn_subscription_tokens WHERE vpn_account_id=a.id AND status='active')=2
AND NOT EXISTS (SELECT 1 FROM routegate_dispatch.confirmed_removals c WHERE c.operation_id=r.id)
AND NOT EXISTS (SELECT 1 FROM routegate_dispatch.retired_vless_credentials x WHERE x.operation_id=r.id)
FROM vpn_credential_revocations r JOIN servers s ON s.id=r.server_id JOIN vpn_accounts a ON a.id=r.vpn_account_id
JOIN routegate_dispatch.held_removals h ON h.operation_id=r.id JOIN config_apply_jobs j ON j.id=h.job_id
WHERE r.id=$1::uuid`, f.operation, originalUUID).Scan(&rolledBack); err != nil || !rolledBack {
		t.Fatal("failed confirmation left a partial terminal state", err)
	}
	if _, err := f.pool.Exec(f.ctx, `UPDATE servers SET active_config_version_id=NULL WHERE id=$1::uuid`, f.target); err != nil {
		t.Fatal(err)
	}
	confirmed, err := f.svc.ConfirmHeldRemoval(f.ctx, proof, verifier, signature)
	if err != nil {
		t.Fatal("confirm held removal", err)
	}
	again, err := f.svc.ConfirmHeldRemoval(f.ctx, proof, verifier, signature)
	if err != nil || confirmed.IsZero() || !again.Equal(confirmed) {
		t.Fatal("confirmation replay rewrote its timestamp", err)
	}

	var exact bool
	var report []byte
	if err := f.pool.QueryRow(f.ctx, `SELECT r.state='confirmed' AND j.status='revocation_confirmed'
AND s.active_config_version_id=v.id AND v.status='applied' AND v.pinned AND v.applied_at=c.confirmed_at
AND a.status='revoked' AND a.vless_uuid<>$2::uuid AND x.digest=sha256(uuid_send($2::uuid))
AND (v.client_settings->'accounts' ? ($3::uuid)::text) AND NOT (v.client_settings->'accounts' ? a.id::text)
AND (SELECT count(*) FROM vpn_subscription_tokens WHERE vpn_account_id=a.id AND status='revoked')=2
AND EXISTS(SELECT 1 FROM vpn_subscription_tokens WHERE vpn_account_id=$3::uuid AND status='active')
AND (SELECT count(*) FROM vpn_credential_revocation_events WHERE operation_id=r.id AND action='confirmed')=1,
e.evidence
FROM vpn_credential_revocations r JOIN servers s ON s.id=r.server_id JOIN vpn_accounts a ON a.id=r.vpn_account_id
JOIN routegate_dispatch.held_removals h ON h.operation_id=r.id JOIN config_apply_jobs j ON j.id=h.job_id
JOIN config_versions v ON v.id=h.version_id JOIN routegate_dispatch.confirmed_removals c ON c.operation_id=r.id
JOIN routegate_dispatch.removal_results e ON e.operation_id=r.id
JOIN routegate_dispatch.retired_vless_credentials x ON x.operation_id=r.id
WHERE r.id=$1::uuid`, f.operation, originalUUID, remainingID).Scan(&exact, &report); err != nil || !exact {
		t.Fatal("terminal state did not agree with evidence, applied membership or subscriptions", err)
	}
	if err := f.svc.AcceptHeldRemovalResult(f.ctx, agents.HashToken(token), decision.ApplyJobID, report); err != nil {
		t.Fatal("exact Agent result replay failed after confirmation", err)
	}
	_, err = runtime.Exec(f.ctx, `UPDATE vpn_subscription_tokens SET status='active' WHERE token_hash='terminal-target-legacy'`)
	if !errors.As(err, &denied) || denied.Code != "P0141" {
		t.Fatal("terminal subscription was reactivated while account remained revoked", err)
	}
	_, err = runtime.Exec(f.ctx, `UPDATE servers SET active_config_version_id=$2::uuid WHERE id=$1::uuid`,
		decision.ServerID, decision.BaselineVersionID)
	if !errors.As(err, &denied) || denied.Code != "P0141" || denied.Message != "retired VLESS credential cannot be used" {
		t.Fatal("old UUID passed the retirement fence after reservation release", err)
	}
}
