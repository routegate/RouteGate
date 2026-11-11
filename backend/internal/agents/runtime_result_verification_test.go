package agents

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
)

type resultVerificationFake struct {
	*fakeAgentAPIRepository
	input RuntimeResultVerificationInput
	calls int
	err   error
}

func (f *resultVerificationFake) VerifyRuntimeResult(_ context.Context, in RuntimeResultVerificationInput) (RuntimeResultVerification, error) {
	f.calls++
	f.input = in
	return RuntimeResultVerification{TaskID: in.JobID, AgentID: "agent", ServerID: "node", Verified: true}, f.err
}

func TestRuntimeMutationFencingVerifyHandler(t *testing.T) {
	for _, mode := range []string{"valid", "missing_auth", "failed", "error", "array", "trailing", "oversize", "unknown_field", "not_found"} {
		t.Run(mode, func(t *testing.T) {
			body := `{"status":"succeeded","resultPayload":{"count":9007199254740993}}`
			switch mode {
			case "failed":
				body = `{"status":"failed"}`
			case "error":
				body = `{"status":"succeeded","errorMessage":"uncertain"}`
			case "array":
				body = `{"status":"succeeded","resultPayload":[]}`
			case "trailing":
				body += "{}"
			case "oversize":
				body = strings.Repeat(" ", runtimeResultVerificationLimit) + body
			case "unknown_field":
				body = `{"status":"succeeded","extra":true}`
			}
			f := &resultVerificationFake{fakeAgentAPIRepository: &fakeAgentAPIRepository{}}
			if mode == "not_found" {
				f.err = pgx.ErrNoRows
			}
			h := &Handler{logger: slog.Default(), repository: f}
			r := httptest.NewRequest(http.MethodPost, "/result/verify", strings.NewReader(body))
			r.SetPathValue("job_id", "task")
			if mode != "missing_auth" {
				r.Header.Set("Authorization", "Bearer rg_agent_isolated-token")
			}
			w := httptest.NewRecorder()
			h.VerifyTaskResult(w, r)
			want := 400
			switch mode {
			case "valid":
				want = 200
			case "missing_auth":
				want = 401
			case "not_found":
				want = 404
			}
			if w.Code != want || w.Header().Get("Cache-Control") != "no-store" {
				t.Fatal(w.Code, w.Body.String())
			}
			if mode == "valid" {
				var result RuntimeResultVerification
				if json.Unmarshal(w.Body.Bytes(), &result) != nil {
					t.Fatal("invalid response")
				}
				digest := sha256.Sum256([]byte(body))
				if !result.Verified || result.SchemaVersion != 1 || result.EnvelopeSHA256 != hex.EncodeToString(digest[:]) || f.input.TokenHash != HashToken("rg_agent_isolated-token") || string(f.input.ResultPayload) != `{"count":9007199254740993}` {
					t.Fatal("binding or numeric value lost")
				}
			} else if mode != "not_found" && f.calls != 0 {
				t.Fatal("invalid input reached repository")
			}
		})
	}
}
