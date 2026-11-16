package db

import (
	"bytes"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ikaevus/routegate/backend/internal/agents"
	"github.com/jackc/pgx/v5"
)

func TestRuntimeMutationFencingVerifyStoredResult(t *testing.T) {
	ctx, pool, in, otherServer, done := removalPreparationFixture(t)
	defer done()
	var agentID, token string
	if err := pool.QueryRow(ctx, `UPDATE agents SET capabilities=capabilities || '{"runtimeMutationFencingV1":true}'::jsonb WHERE server_id=$1::uuid RETURNING id::text,token_hash`, in.ServerID).Scan(&agentID, &token); err != nil {
		t.Fatal(err)
	}
	payload := json.RawMessage(`{"nested":{"b":2,"a":1},"count":9007199254740993}`)
	var configJob, operationJob string
	if err := pool.QueryRow(ctx, `INSERT INTO config_apply_jobs(server_id,agent_id,config_version_id,action,status,result_payload,completed_at) VALUES($1::uuid,$2::uuid,$3::uuid,'apply','succeeded',$4::jsonb,now()) RETURNING id::text`, in.ServerID, agentID, in.BaselineVersionID, payload).Scan(&configJob); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO agent_operation_jobs(server_id,agent_id,kind,operation,status,result_payload,completed_at) VALUES($1::uuid,$2::uuid,'vpn_core_service','restart','succeeded',$3::jsonb,now()) RETURNING id::text`, in.ServerID, agentID, payload).Scan(&operationJob); err != nil {
		t.Fatal(err)
	}
	repo := agents.NewRepository(pool)
	snapshot := func() string {
		var data string
		if err := pool.QueryRow(ctx, `SELECT jsonb_build_array((SELECT to_jsonb(j) FROM config_apply_jobs j WHERE id=$1::uuid),(SELECT to_jsonb(j) FROM agent_operation_jobs j WHERE id=$2::uuid))::text`, configJob, operationJob).Scan(&data); err != nil {
			t.Fatal(err)
		}
		return data
	}
	before := snapshot()
	for _, job := range []string{configJob, operationJob} {
		for _, tc := range []struct {
			payload  json.RawMessage
			verified bool
		}{
			{payload, true},
			{json.RawMessage(`{"count":9007199254740993,"nested":{"a":1,"b":2}}`), true},
			{json.RawMessage(`{"count":9007199254740992,"nested":{"a":1,"b":2}}`), false},
			{json.RawMessage(`{}`), false},
		} {
			result, err := repo.VerifyRuntimeResult(ctx, agents.RuntimeResultVerificationInput{TokenHash: token, JobID: job, ResultPayload: tc.payload})
			if err != nil || result.Verified != tc.verified || result.TaskID != job || result.AgentID != agentID || result.ServerID != in.ServerID {
				t.Fatal(result, err)
			}
		}
		if _, err := repo.VerifyRuntimeResult(ctx, agents.RuntimeResultVerificationInput{TokenHash: "other-agent-token", JobID: job, ResultPayload: payload}); !errors.Is(err, pgx.ErrNoRows) {
			t.Fatal("foreign result accessible", err)
		}
	}
	if snapshot() != before {
		t.Fatal("verification mutated jobs or timestamps")
	}
	// Exercise real Manager HTTP handlers over PostgreSQL: completion commits,
	// its retry is 404, and verification recognizes the stored result read-only.
	const rawToken = "rg_agent_isolated-verification"
	if _, err := pool.Exec(ctx, `UPDATE agents SET token_hash=$2 WHERE id=$1::uuid`, agentID, agents.HashToken(rawToken)); err != nil {
		t.Fatal(err)
	}
	token = agents.HashToken(rawToken)
	h := agents.NewHandler(slog.Default(), pool)
	body := []byte(`{"status":"succeeded","resultPayload":{"phase":"complete","value":42}}`)
	request := func(job string, verify bool) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodPost, "/result", bytes.NewReader(body))
		r.Header.Set("Authorization", "Bearer "+rawToken)
		r.SetPathValue("job_id", job)
		w := httptest.NewRecorder()
		if verify {
			h.VerifyTaskResult(w, r)
		} else {
			h.CompleteTask(w, r)
		}
		return w
	}
	for _, job := range []string{configJob, operationJob} {
		if _, err := pool.Exec(ctx, `UPDATE config_apply_jobs SET status='in_progress' WHERE id=$1::uuid`, job); err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, `UPDATE agent_operation_jobs SET status='in_progress' WHERE id=$1::uuid`, job); err != nil {
			t.Fatal(err)
		}
		if w := request(job, false); w.Code != 200 {
			t.Fatal(w.Code, w.Body.String())
		}
		committed := snapshot()
		if w := request(job, false); w.Code != 404 {
			t.Fatal("duplicate completion unexpectedly accepted", w.Code)
		}
		for range 2 {
			w := request(job, true)
			var evidence agents.RuntimeResultVerification
			if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &evidence) != nil || !evidence.Verified || evidence.AgentID != agentID || evidence.TaskID != job {
				t.Fatal("committed result not verified", w.Code, w.Body.String())
			}
		}
		if snapshot() != committed {
			t.Fatal("readback rewrote committed completion")
		}
	}
	for _, state := range []string{"in_progress", "failed"} {
		if _, err := pool.Exec(ctx, `UPDATE agent_operation_jobs SET status=$2,result_payload=$3::jsonb WHERE id=$1::uuid`, operationJob, state, payload); err != nil {
			t.Fatal(err)
		}
		result, err := repo.VerifyRuntimeResult(ctx, agents.RuntimeResultVerificationInput{TokenHash: token, JobID: operationJob, ResultPayload: payload})
		if err != nil || result.Verified {
			t.Fatal("non-success confirmed", err)
		}
	}
	if _, err := pool.Exec(ctx, `UPDATE agent_operation_jobs SET status='succeeded',error_message='uncertain' WHERE id=$1::uuid`, operationJob); err != nil {
		t.Fatal(err)
	}
	if result, err := repo.VerifyRuntimeResult(ctx, agents.RuntimeResultVerificationInput{TokenHash: token, JobID: operationJob, ResultPayload: payload}); err != nil || result.Verified {
		t.Fatal("stored error accepted", err)
	}
	if _, err := pool.Exec(ctx, `UPDATE agent_operation_jobs SET server_id=$2::uuid,error_message=NULL WHERE id=$1::uuid`, operationJob, otherServer); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.VerifyRuntimeResult(ctx, agents.RuntimeResultVerificationInput{TokenHash: token, JobID: operationJob, ResultPayload: payload}); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatal("different server accepted", err)
	}
	if _, err := pool.Exec(ctx, `UPDATE agents SET capabilities='{}'::jsonb WHERE id=$1::uuid`, agentID); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.VerifyRuntimeResult(ctx, agents.RuntimeResultVerificationInput{TokenHash: token, JobID: configJob, ResultPayload: payload}); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatal("legacy agent got recovery evidence", err)
	}
}
