package db

import (
	"bytes"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ikaevus/routegate/backend/internal/agents"
	"github.com/ikaevus/routegate/backend/internal/revocations"
)

func TestCredentialRemovalDispatchAdmissionAgentExchange(t *testing.T) {
	for _, code := range []string{"", "restart_failed", "interrupted_or_failed_mutation"} {
		name := "success"
		if code != "" {
			name = code
		}
		t.Run(name, func(t *testing.T) { testCredentialRemovalAgentExchange(t, code) })
	}
}

func testCredentialRemovalAgentExchange(t *testing.T, failureCode string) {
	recovery := failureCode != ""
	token, err := agents.GenerateAgentToken()
	if err != nil {
		t.Fatal(err)
	}
	f, authority, decision, private := newSignedRemovalDecisionWithToken(t, agents.HashToken(token))
	if _, err := f.svc.StagePreparedCredentialRemoval(f.ctx, f.operation, f.nonce, true); err != nil {
		t.Fatal(err)
	}
	data, err := decision.SigningBytes()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.IssueHeldRemovalDispatch(f.ctx, f.operation, f.nonce, authority, ed25519.Sign(private, data)); err != nil {
		t.Fatal(err)
	}
	unrelatedToken, err := agents.GenerateAgentToken()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(f.ctx, "UPDATE agents SET token_hash=$2 WHERE server_id=$1::uuid", f.target, agents.HashToken(unrelatedToken)); err != nil {
		t.Fatal(err)
	}
	handler := agents.NewHandler(slog.New(slog.NewTextHandler(io.Discard, nil)), f.pool)
	mux := http.NewServeMux()
	handler.MountHeldRemoval(mux, f.svc)
	const removalPath = "/api/v1/agent/credential-removals/"
	serve := func(path, bearer string, body []byte, want int) []byte {
		t.Helper()
		req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(body)).WithContext(f.ctx)
		req.Header.Set("Authorization", "Bearer "+bearer)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		if w.Code != want || w.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("exchange response status %d, wanted %d with no-store", w.Code, want)
		}
		return w.Body.Bytes()
	}
	claim := func(bearer string) *agents.AgentDeliveryTask {
		t.Helper()
		var response agents.AgentNextTaskResponse
		if err := json.Unmarshal(serve(removalPath+"claim", bearer, nil, http.StatusOK), &response); err != nil {
			t.Fatal("invalid claim response")
		}
		return response.Task
	}
	serve(removalPath+"claim", "unknown-agent-token", nil, http.StatusUnauthorized)
	serve(removalPath+"claim", "rg_agent_unknown-fixture-token", nil, http.StatusUnauthorized)
	if claim(token) != nil || claim(unrelatedToken) != nil {
		t.Fatal("task delivered before capability negotiation or to another Agent")
	}
	var port int
	if err := f.pool.QueryRow(f.ctx, "SELECT vless_port FROM servers WHERE id=$1::uuid", decision.ServerID).Scan(&port); err != nil {
		t.Fatal(err)
	}
	report := revocations.CredentialRemovalReport{
		SchemaVersion: 1, JobID: decision.ApplyJobID, OperationID: decision.OperationID,
		ServerID: decision.ServerID, AgentID: decision.AgentID, AccountID: decision.AccountID,
		BaselineVersionID: decision.BaselineVersionID, ConfigVersionID: decision.CandidateVersionID,
		ConfigHash: decision.CandidateHash, BaselineRuntimeHash: decision.BaselineRuntimeHash,
		ActiveRuntimeHash: decision.CandidateRuntimeHash, ListenerPort: port,
		Before: revocations.RuntimeGeneration{InvocationID: strings.Repeat("a", 32), PID: 123, StartedMonotonic: "100"},
		After:  revocations.RuntimeGeneration{InvocationID: strings.Repeat("b", 32), PID: 124, StartedMonotonic: "200"},
		State:  "succeeded", SessionEffect: "shared_process_restart_observed",
	}
	if recovery {
		report.State, report.Code = "recovery_required", failureCode
		report.ActiveRuntimeHash, report.SessionEffect, report.ListenerPort = "", "", 0
		report.After = revocations.RuntimeGeneration{}
	}
	body, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	path := removalPath + decision.ApplyJobID + "/result"
	serve(path, token, body, http.StatusConflict)
	if _, err := f.pool.Exec(f.ctx, `UPDATE agents SET capabilities=capabilities ||
 '{"credentialRemovalV1":true,"runtimeMutationFencingV1":true}'::jsonb WHERE id=$1::uuid`, f.agent); err != nil {
		t.Fatal(err)
	}
	task := claim(token)
	if task == nil || task.ID != decision.ApplyJobID || task.Kind != revocations.TaskKindCredentialRemoval ||
		task.CredentialRemoval == nil || task.CredentialRemoval.OperationID != f.operation ||
		task.CredentialRemoval.VLESSUUID == "" || task.ConfigHash != decision.CandidateHash || task.DeliveryExpiresAt == nil {
		t.Fatal("assigned Agent did not receive the exact held task")
	}
	if !reflect.DeepEqual(task, claim(token)) {
		t.Fatal("repeat claim changed the job or its original authority window")
	}
	serve(path, unrelatedToken, body, http.StatusConflict)
	serve(removalPath+decision.CandidateVersionID+"/result", token, body, http.StatusConflict)
	serve(path, token, bytes.Replace(body, []byte(`"jobId"`), []byte(`"JobId"`), 1), http.StatusConflict)
	if _, err := f.pool.Exec(f.ctx, `UPDATE agents SET last_authenticated_heartbeat_at=clock_timestamp()-interval '2 minutes' WHERE id=$1::uuid`, f.agent); err != nil {
		t.Fatal(err)
	}
	if claim(token) != nil {
		t.Fatal("expired delivery authority returned the previously claimed task")
	}
	if _, err := f.pool.Exec(f.ctx, "UPDATE auth_sessions SET revoked_at=clock_timestamp() WHERE id=$1::uuid", f.session); err != nil {
		t.Fatal(err)
	}
	serve(path, token, body, http.StatusNoContent)
	serve(path, token, body, http.StatusNoContent)
	serve(path, token, bytes.Replace(body, []byte(`"pid":123`), []byte(`"pid":125`), 1), http.StatusConflict)

	if recovery {
		key, signer, err := ed25519.GenerateKey(nil)
		if err != nil {
			t.Fatal(err)
		}
		verifier, err := revocations.NewRemovalVerificationAuthority(decision.Audience, key)
		if err != nil {
			t.Fatal(err)
		}
		digest, err := revocations.RuntimeDigest(body)
		if err != nil {
			t.Fatal(err)
		}
		proof := revocations.RemovalVerificationProof{
			Audience: decision.Audience, OperationID: f.operation, AgentReportDigest: digest,
			ObservedAtUnixMicros: time.Now().UnixMicro(), OldSessionTerminated: true,
			CachedCredentialRejected: true, Continuity: revocations.RemovalRemainingAccountReconnected,
		}
		message, err := proof.SigningBytes()
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.svc.ConfirmHeldRemoval(f.ctx, proof, verifier, ed25519.Sign(signer, message)); !errors.Is(err, revocations.ErrPreflight) {
			t.Fatal("failure report admitted terminal confirmation", err)
		}
	}
	var retained bool
	if err := f.pool.QueryRow(f.ctx, `SELECT r.state='prepared' AND j.status='held_for_revocation'
 AND s.active_config_version_id=r.baseline_version_id AND evidence=$2::jsonb
 AND NOT EXISTS(SELECT 1 FROM routegate_dispatch.confirmed_removals c WHERE c.operation_id=r.id)
 AND NOT EXISTS(SELECT 1 FROM routegate_dispatch.retired_vless_credentials x WHERE x.operation_id=r.id)
 FROM routegate_dispatch.removal_results e JOIN routegate_dispatch.held_removals h USING(operation_id)
 JOIN vpn_credential_revocations r ON r.id=h.operation_id JOIN config_apply_jobs j ON j.id=h.job_id
 JOIN servers s ON s.id=r.server_id WHERE h.operation_id=$1::uuid`, f.operation, body).Scan(&retained); err != nil || !retained {
		t.Fatal("result changed held state, active config, reservation or retained evidence", err)
	}
	downgradeEmptyRetirementGuard(t, f.ctx, f.pool)
	down, err := os.ReadFile("../../migrations/000177_credential_removal_agent_exchange.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(f.ctx, string(down)); err == nil {
		t.Fatal("downgrade erased Agent claim and evidence")
	}
}
