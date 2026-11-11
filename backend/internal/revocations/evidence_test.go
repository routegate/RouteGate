package revocations

import (
	"encoding/json"
	"strings"
	"testing"
)

func evidenceFixture() (EvidenceBinding, CredentialRemovalReport) {
	ids := []string{"11111111-1111-4111-8111-111111111111", "22222222-2222-4222-8222-222222222222", "33333333-3333-4333-8333-333333333333", "44444444-4444-4444-8444-444444444444", "55555555-5555-4555-8555-555555555555", "66666666-6666-4666-8666-666666666666", "77777777-7777-4777-8777-777777777777"}
	b := EvidenceBinding{OperationID: ids[0], JobID: ids[1], AccountID: ids[2], ServerID: ids[3], AgentID: ids[4], BaselineVersionID: ids[5], ConfigVersionID: ids[6], ConfigHash: strings.Repeat("a", 64), BaselineRuntimeHash: strings.Repeat("b", 64), CandidateRuntimeHash: strings.Repeat("c", 64), ListenerPort: 443}
	r := CredentialRemovalReport{SchemaVersion: 1, OperationID: b.OperationID, JobID: b.JobID, AccountID: b.AccountID, ServerID: b.ServerID, AgentID: b.AgentID, BaselineVersionID: b.BaselineVersionID, ConfigVersionID: b.ConfigVersionID, ConfigHash: b.ConfigHash, BaselineRuntimeHash: b.BaselineRuntimeHash, ActiveRuntimeHash: b.CandidateRuntimeHash, ListenerPort: 443, State: "succeeded", SessionEffect: "shared_process_restart_observed", Before: RuntimeGeneration{InvocationID: strings.Repeat("a", 32), PID: 123, StartedMonotonic: "100"}, After: RuntimeGeneration{InvocationID: strings.Repeat("b", 32), PID: 124, StartedMonotonic: "200"}}
	return b, r
}
func TestVerifyRemovalEvidenceBindings(t *testing.T) {
	b, r := evidenceFixture()
	data, _ := json.Marshal(r)
	if got, err := VerifySuccess(data, b); err != nil || got != r {
		t.Fatal(got, err)
	}
	cases := map[string]func(*CredentialRemovalReport){
		"job": func(r *CredentialRemovalReport) { r.JobID = b.AgentID }, "agent": func(r *CredentialRemovalReport) { r.AgentID = b.JobID }, "account": func(r *CredentialRemovalReport) { r.AccountID = b.AgentID }, "server": func(r *CredentialRemovalReport) { r.ServerID = b.AgentID }, "operation": func(r *CredentialRemovalReport) { r.OperationID = b.AgentID }, "baseline version": func(r *CredentialRemovalReport) { r.BaselineVersionID = b.ConfigVersionID }, "candidate version": func(r *CredentialRemovalReport) { r.ConfigVersionID = b.BaselineVersionID }, "envelope hash": func(r *CredentialRemovalReport) { r.ConfigHash = b.BaselineRuntimeHash }, "runtime hash": func(r *CredentialRemovalReport) { r.ActiveRuntimeHash = b.BaselineRuntimeHash }, "baseline hash": func(r *CredentialRemovalReport) { r.BaselineRuntimeHash = b.CandidateRuntimeHash }, "generation": func(r *CredentialRemovalReport) { r.After = r.Before }, "zero pid": func(r *CredentialRemovalReport) { r.After.PID = 0 }, "failure": func(r *CredentialRemovalReport) { r.State = "failed" }, "recovery": func(r *CredentialRemovalReport) { r.State = "recovery_required" }, "error": func(r *CredentialRemovalReport) { r.Code = "secret" }, "port": func(r *CredentialRemovalReport) { r.ListenerPort = 8443 }, "session": func(r *CredentialRemovalReport) { r.SessionEffect = "" }, "schema": func(r *CredentialRemovalReport) { r.SchemaVersion = 2 },
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			bad := r
			change(&bad)
			data, _ := json.Marshal(bad)
			got, err := VerifySuccess(data, b)
			if err == nil || got != (CredentialRemovalReport{}) {
				t.Fatal("accepted unsupported proof")
			}
		})
	}
	for _, bad := range [][]byte{[]byte(`{"state":"failed","state":"succeeded"}`), append(data, []byte(` {}`)...), []byte(strings.Replace(string(data), `"state":`, `"vlessUuid":"secret","state":`, 1)), []byte(`{"schemaVersion":1,"state":"succeeded"}`)} {
		if _, err := VerifySuccess(bad, b); err == nil {
			t.Fatal("accepted malformed/extra evidence")
		}
	}
}
