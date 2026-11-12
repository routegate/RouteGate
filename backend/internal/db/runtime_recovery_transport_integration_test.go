package db

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ikaevus/routegate/backend/internal/recoveryobserve"
)

func requestRecoveryTransport(t *testing.T,handler http.HandlerFunc,method,token string,body any) *httptest.ResponseRecorder {
	t.Helper()
	var reader io.Reader
	if body!=nil {
		data,err:=json.Marshal(body)
		if err!=nil{t.Fatal(err)}
		reader=bytes.NewReader(data)
	}
	req:=httptest.NewRequest(method,"https://manager.invalid/api/v1/agent/recovery-observations",reader)
	if token!="" {req.Header.Set("Authorization","Bearer "+token)}
	if body!=nil {req.Header.Set("Content-Type","application/json")}
	rec:=httptest.NewRecorder()
	handler(rec,req)
	return rec
}

func TestRuntimeRecoveryTransportPendingOnlyForCurrentAgentAndNeverIssuesNonce(t *testing.T) {
	f,attempt,evidence:=issuerFixture(t)
	defer f.done()
	transport:=recoveryobserve.NewObservationTransport(f.pool)
	anon:=requestRecoveryTransport(t,transport.Pending,http.MethodGet,"",nil)
	if anon.Code!=http.StatusUnauthorized || strings.Contains(anon.Body.String(),attempt) {
		t.Fatalf("unauthenticated pending leaked metadata: %d %q",anon.Code,anon.Body.String())
	}
	other:=requestRecoveryTransport(t,transport.Pending,http.MethodGet,
		"rg_agent_"+strings.Repeat("a",43),nil)
	if other.Code==http.StatusOK {
		t.Fatal("foreign bearer accepted")
	}
	rec:=requestRecoveryTransport(t,transport.Pending,http.MethodGet,recoveryFixtureAgentToken,nil)
	if rec.Code!=http.StatusOK ||
		rec.Header().Get("Cache-Control")!="no-store, no-cache, max-age=0" ||
		rec.Header().Get("Vary")!="Authorization" {
		t.Fatalf("read-only pending rejected/no-store missing: status=%d headers=%v",rec.Code,rec.Header())
	}
	var result recoveryobserve.PendingObservationResponse
	if err:=json.Unmarshal(rec.Body.Bytes(),&result);err!=nil{t.Fatal(err)}
	if result.Pending==nil||result.Pending.AttemptID!=attempt||
		result.Pending.ReservationID!=f.reservationID||
		result.Pending.TaskID!=evidence.TaskID||
		result.Pending.AgentID!=f.agentID||result.Pending.ServerID!=f.serverID||
		result.Pending.AgentCredentialGeneration!=f.generation||
		result.Pending.AttemptEpoch<=0 {
		t.Fatalf("Manager returned wrong Agent's pending attempt: %+v",result.Pending)
	}
	if strings.Contains(rec.Body.String(),recoveryFixtureAgentToken) ||
		strings.Contains(rec.Body.String(),"nonce") ||
		strings.Contains(rec.Body.String(),evidence.MarkerSHA256) {
		t.Fatal("read-only discovery leaked token, nonce or private hash")
	}
	var count int
	if err:=f.pool.QueryRow(f.ctx,
		`SELECT count(*) FROM vpn_runtime_recovery_observation_nonces`).Scan(&count);
		err!=nil||count!=0{
		t.Fatalf("read-only pending created challenge: %d %v",count,err)
	}
}

func TestRuntimeRecoveryTransportIssuesNonceAfterCommitWithoutRetry(t *testing.T) {
	f,attempt,evidence:=issuerFixture(t)
	defer f.done()
	transport:=recoveryobserve.NewObservationTransport(f.pool)
	issue:=requestRecoveryTransport(t,transport.Issue,http.MethodPost,
		recoveryFixtureAgentToken,
		map[string]any{"attemptId":attempt,"evidence":evidence})
	if issue.Code!=http.StatusCreated {
		t.Fatalf("authenticated issue failed %d %s",issue.Code,issue.Body.String())
	}
	if issue.Header().Get("Cache-Control")!="no-store, no-cache, max-age=0" ||
		issue.Header().Get("X-Content-Type-Options")!="nosniff"{
		t.Fatal("nonce response not marked non-cacheable")
	}
	var issued recoveryobserve.IssuedObservationNonce
	if err:=json.Unmarshal(issue.Body.Bytes(),&issued);err!=nil{t.Fatal(err)}
	if issued.Nonce==""||issued.AttemptID!=attempt||issued.ExpectedEvidence!=evidence {
		t.Fatalf("lost/cross-bound nonce response: %+v",issued)
	}
	nonceAuditState(t,f,issued.ObservationID,"issued",1)
	second:=requestRecoveryTransport(t,transport.Issue,http.MethodPost,
		recoveryFixtureAgentToken,map[string]any{"attemptId":attempt,"evidence":evidence})
	if second.Code!=http.StatusConflict || strings.Contains(second.Body.String(),issued.Nonce) ||
		strings.Contains(second.Body.String(),attempt) {
		t.Fatalf("HTTP reissue leaked nonce: %d %q",second.Code,second.Body.String())
	}
	nonceAuditState(t,f,issued.ObservationID,"issued",1)
	// A committed nonce is no longer discoverable as a new attempt.
	pending:=requestRecoveryTransport(t,transport.Pending,http.MethodGet,recoveryFixtureAgentToken,nil)
	var out recoveryobserve.PendingObservationResponse
	if pending.Code!=http.StatusOK || json.Unmarshal(pending.Body.Bytes(),&out)!=nil || out.Pending!=nil {
		t.Fatalf("previously issued attempt offered for repeat delivery: %d %q",pending.Code,pending.Body.String())
	}
}

func TestRuntimeRecoveryTransportMalformedOrForeignRequestNeverIssues(t *testing.T) {
	for _,mode:=range []string{"missing_bearer","foreign_bearer","wrong_attempt","wrong_agent","wrong_task","unknown_field"} {
		t.Run(mode,func(t *testing.T){
			f,attempt,evidence:=issuerFixture(t)
			defer f.done()
			handler:=recoveryobserve.NewObservationTransport(f.pool)
			token:=recoveryFixtureAgentToken
			body:=map[string]any{"attemptId":attempt,"evidence":evidence}
			switch mode{
			case "missing_bearer":token=""
			case "foreign_bearer":token="rg_agent_"+strings.Repeat("a",43)
			case "wrong_attempt":body["attemptId"]="11111111-1111-4111-8111-111111111111"
			case "wrong_agent":evidence.AgentID=evidence.ServerID;body["evidence"]=evidence
			case "wrong_task":evidence.TaskID=evidence.AgentID;body["evidence"]=evidence
			case "unknown_field":body["agentToken"]=recoveryFixtureAgentToken
			}
			rec:=requestRecoveryTransport(t,handler.Issue,http.MethodPost,token,body)
			if rec.Code==http.StatusCreated ||
				strings.Contains(rec.Body.String(),recoveryFixtureAgentToken) ||
				strings.Contains(rec.Body.String(),attempt) {
				t.Fatalf("invalid issue request created or leaked nonce: %d %s",rec.Code,rec.Body.String())
			}
			var n int
			if err:=f.pool.QueryRow(f.ctx,`SELECT count(*) FROM vpn_runtime_recovery_observation_nonces`).Scan(&n);err!=nil||n!=0{
				t.Fatalf("invalid request persisted nonce: %d %v",n,err)
			}
		})
	}
}

func TestRuntimeRecoveryTransportNoUnsafePayloadReflection(t *testing.T) {
	f,attempt,evidence:=issuerFixture(t)
	defer f.done()
	handler:=recoveryobserve.NewObservationTransport(f.pool)
	// Oversized and multiple JSON values must be refused without ever
	// sending the bearer back to the caller or recording a nonce.
	for _,body:=range []string{
		strings.Repeat("X",5000),
		`{"attemptId":"`+attempt+`","evidence":{}}{"agentToken":"SECRET"}`,
	}{
		req:=httptest.NewRequest(http.MethodPost,
			"https://manager.invalid/api/v1/agent/recovery-observations/issue",
			strings.NewReader(body))
		req.Header.Set("Authorization","Bearer "+recoveryFixtureAgentToken)
		r:=httptest.NewRecorder()
		handler.Issue(r,req)
		if r.Code!=http.StatusBadRequest || strings.Contains(r.Body.String(),recoveryFixtureAgentToken) {
			t.Fatalf("bad request accepted or leaked credential: %d %q",r.Code,r.Body.String())
		}
	}
	_ = evidence
	var n int
	if err:=f.pool.QueryRow(f.ctx,`SELECT count(*) FROM vpn_runtime_recovery_observation_nonces`).Scan(&n);err!=nil||n!=0 {
		t.Fatalf("invalid JSON wrote to nonce registry: %d %v",n,err)
	}
}
