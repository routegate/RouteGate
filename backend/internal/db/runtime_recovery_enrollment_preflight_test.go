package db

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/ikaevus/routegate/backend/internal/recoveryobserve"
)

func hasEnrollmentPreflightReasonV1(result recoveryEnrollmentPreflightV1, reason string) bool {
	for _, item := range result.Reasons {
		if item == reason {
			return true
		}
	}
	return false
}

func requireEnrollmentPreflightHistoricalV1(t *testing.T, got recoveryEnrollmentPreflightV1) {
	t.Helper()
	if !got.Unattested ||
		got.Assessment != recoveryEnrollmentManualReviewUnattestedV1 ||
		!hasEnrollmentPreflightReasonV1(got, "local_journal_unverified") ||
		!hasEnrollmentPreflightReasonV1(got, "independent_host_provenance_unverified") ||
		!hasEnrollmentPreflightReasonV1(got, "dispatcher_checkpoint_coupling_not_implemented") {
		t.Fatalf("Manager snapshot was promoted into attestation or authorization: %+v", got)
	}
}

func TestRuntimeRecoveryEnrollmentPreflightV1LegacyNeverSilentlyEnrolled(t *testing.T) {
	f := newRecoveryReservationFixture(t)
	defer f.done()
	// An existing legacy Agent has no preparation and no journal. It must
	// not be interpreted as newly registered or allowed to unlock.
	got, err := inspectRecoveryEnrollmentPreflightV1(f.ctx, f.pool, f.agentID)
	if err != nil { t.Fatal(err) }
	requireEnrollmentPreflightHistoricalV1(t, got)
	if got.PreparationID != "" ||
		!hasEnrollmentPreflightReasonV1(got, "preparation_absent") ||
		!hasEnrollmentPreflightReasonV1(got, "manager_agent_not_eligible") ||
		!hasEnrollmentPreflightReasonV1(got, "checkpoint_absent") {
		t.Fatalf("legacy Agent was assumed enrolled: %+v",got)
	}
}

func TestRuntimeRecoveryEnrollmentPreflightV1PreparedIsStillUnattested(t *testing.T) {
	f := newRecoveryReservationFixture(t)
	defer f.done()
	f.enableFencing(t)
	prepID, err := prepareRecoveryEnrollmentV1(f.ctx,f)
	if err != nil { t.Fatal(err) }
	before, err := inspectRecoveryEnrollmentPreflightV1(f.ctx, f.pool, f.agentID)
	if err != nil { t.Fatal(err) }
	requireEnrollmentPreflightHistoricalV1(t,before)
	if before.AgentID != f.agentID || before.ServerID != f.serverID ||
		before.ManagerCredentialGeneration != f.generation ||
		before.PreparedCredentialGeneration != f.generation ||
		before.PreparationID != prepID || before.PreparationPhase != "prepared_unattested" ||
		before.AuditEvents != 1 || before.CheckpointCount != 0 ||
		before.OutstandingManagerJobs != 0 || before.RecoveryHolds != 0 ||
		hasEnrollmentPreflightReasonV1(before,"preparation_generation_stale") ||
		!hasEnrollmentPreflightReasonV1(before,"checkpoint_absent") {
		t.Fatalf("inconsistent Manager preparation snapshot: %+v",before)
	}
	// Pure inspection must not write to its snapshot or advance the phase.
	after, err := inspectRecoveryEnrollmentPreflightV1(f.ctx, f.pool, f.agentID)
	if err != nil || after.PreparationID != prepID ||
		after.AuditEvents != before.AuditEvents ||
		after.ManagerCredentialGeneration != before.ManagerCredentialGeneration {
		t.Fatalf("read-only inspection unexpectedly modified Manager rows: %+v %v",after,err)
	}
}

func TestRuntimeRecoveryEnrollmentPreflightV1DetectsCredentialGenerationDrift(t *testing.T) {
	f := newRecoveryReservationFixture(t)
	defer f.done()
	f.enableFencing(t)
	if _, err := prepareRecoveryEnrollmentV1(f.ctx,f); err != nil { t.Fatal(err) }
	if _,err:=f.pool.Exec(f.ctx,`
UPDATE agents
 SET credential_generation=credential_generation+1,
     last_authenticated_heartbeat_at=NULL,
     last_authenticated_heartbeat_generation=NULL
 WHERE id=$1::uuid
`,f.agentID);err!=nil{t.Fatal(err)}
	got,err:=inspectRecoveryEnrollmentPreflightV1(f.ctx,f.pool,f.agentID)
	if err!=nil{t.Fatal(err)}
	requireEnrollmentPreflightHistoricalV1(t,got)
	if !hasEnrollmentPreflightReasonV1(got,"preparation_generation_stale") ||
		got.PreparedCredentialGeneration != f.generation ||
		got.ManagerCredentialGeneration != f.generation+1 {
		t.Fatalf("old preparation was reused after Agent rotation: %+v",got)
	}
}

func TestRuntimeRecoveryEnrollmentPreflightV1CheckpointDoesNotGrantReadiness(t *testing.T) {
	f := newRecoveryReservationFixture(t)
	defer f.done()
	f.enableFencing(t)
	if _,err:=prepareRecoveryEnrollmentV1(f.ctx,f);err!=nil{t.Fatal(err)}
	var jobID string
	if err:=f.pool.QueryRow(f.ctx,`
INSERT INTO config_apply_jobs(server_id,agent_id,config_version_id,action,status)
VALUES($1::uuid,$2::uuid,$3::uuid,'apply','pending') RETURNING id::text
`,f.serverID,f.agentID,f.configVersionID).Scan(&jobID);err!=nil{t.Fatal(err)}
	if _,err:=checkpointInsert(f.ctx,f,jobID,"config_apply");err!=nil{t.Fatal(err)}
	got,err:=inspectRecoveryEnrollmentPreflightV1(f.ctx,f.pool,f.agentID)
	if err!=nil{t.Fatal(err)}
	requireEnrollmentPreflightHistoricalV1(t,got)
	if got.CheckpointCount!=1 || got.CheckpointEpoch<=0 ||
		got.CheckpointCredentialGeneration!=f.generation ||
		got.OutstandingManagerJobs!=1 ||
		!hasEnrollmentPreflightReasonV1(got,"manager_jobs_not_quiet") {
		t.Fatalf("dispatched-state ambiguity was silently treated as ready: %+v",got)
	}
	want,err:=recoveryobserve.ManagerExpectedMutationMarkerV1(recoveryobserve.ManagerJobIntentV1{
		TaskID:jobID,AgentID:f.agentID,ServerID:f.serverID,
		Kind:"config_apply",AgentCredentialGeneration:f.generation,
	})
	if err!=nil||got.CheckpointMarkerSHA256!=want{
		t.Fatalf("DB checkpoint was not Manager-derived: %s want %s err %v",got.CheckpointMarkerSHA256,want,err)
	}
	// Even after the Manager-side job becomes complete, the local journal,
	// actual process generation and independent origin remain UNKNOWN.
	if _,err:=f.pool.Exec(f.ctx,`
UPDATE config_apply_jobs
 SET status='succeeded',started_at=now()-interval '10 seconds',completed_at=now()
 WHERE id=$1::uuid
`,jobID);err!=nil{t.Fatal(err)}
	finished,err:=inspectRecoveryEnrollmentPreflightV1(f.ctx,f.pool,f.agentID)
	if err!=nil{t.Fatal(err)}
	requireEnrollmentPreflightHistoricalV1(t,finished)
	if finished.CheckpointCount!=1 || finished.OutstandingManagerJobs!=0 ||
		len(finished.Reasons)==0 {
		t.Fatalf("historical checkpoint improperly granted host truth: %+v",finished)
	}
	// A later rotation invalidates the historical checkpoint generation too.
	if _,err:=f.pool.Exec(f.ctx,`
UPDATE agents SET credential_generation=credential_generation+1,
 last_authenticated_heartbeat_at=NULL,last_authenticated_heartbeat_generation=NULL
WHERE id=$1::uuid
`,f.agentID);err!=nil{t.Fatal(err)}
	stale,err:=inspectRecoveryEnrollmentPreflightV1(f.ctx,f.pool,f.agentID)
	if err!=nil{t.Fatal(err)}
	requireEnrollmentPreflightHistoricalV1(t,stale)
	if !hasEnrollmentPreflightReasonV1(stale,"checkpoint_generation_stale") ||
		!hasEnrollmentPreflightReasonV1(stale,"preparation_generation_stale") {
		t.Fatalf("stale checkpoint was promoted after token rotation: %+v",stale)
	}
}

func TestRuntimeRecoveryEnrollmentPreflightV1DetectsManagerRecoveryHold(t *testing.T) {
	f:=newRecoveryReservationFixture(t)
	defer f.done()
	f.enableFencing(t)
	// The post-event recovery reservation is permanent; the new read-only
	// preflight must never treat it as a clean node.
	if err:=f.reserve(f.ctx,f.generation,"config_apply");err!=nil{t.Fatal(err)}
	got,err:=inspectRecoveryEnrollmentPreflightV1(f.ctx,f.pool,f.agentID)
	if err!=nil{t.Fatal(err)}
	requireEnrollmentPreflightHistoricalV1(t,got)
	if got.RecoveryHolds!=1 ||
		!hasEnrollmentPreflightReasonV1(got,"manager_recovery_hold_present"){
		t.Fatalf("post-event hold omitted: %+v",got)
	}
}

func TestRuntimeRecoveryEnrollmentPreflightV1RefusesAbsentInvalidAndCancelled(t *testing.T){
	f:=newRecoveryReservationFixture(t)
	defer f.done()
	badAgent:="aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
	for _, tc := range []struct{name,id string;ctx context.Context}{
		{"unknown_agent",badAgent,f.ctx},
		{"malformed_uuid","not-a-uuid",f.ctx},
		{"empty_uuid","",f.ctx},
		{"nil_context",f.agentID,nil},
		{"cancelled_context",f.agentID,func() context.Context {ctx,cancel:=context.WithCancel(f.ctx);cancel();return ctx}()},
	}{
		t.Run(tc.name,func(t *testing.T){
			got,err:=inspectRecoveryEnrollmentPreflightV1(tc.ctx,f.pool,tc.id)
			if !errors.Is(err,errRecoveryEnrollmentPreflightRefusedV1) ||
				got.Assessment!=""||got.Unattested||len(got.Reasons)!=0{
				t.Fatalf("invalid preflight input returned evidence: %+v %v",got,err)
			}
		})
	}
}

func TestRuntimeRecoveryEnrollmentPreflightV1NoImplicitActivationSchema(t *testing.T) {
	f:=newRecoveryReservationFixture(t)
	defer f.done()
	f.enableFencing(t)
	id,err:=prepareRecoveryEnrollmentV1(f.ctx,f)
	if err!=nil{t.Fatal(err)}
	result,err:=inspectRecoveryEnrollmentPreflightV1(f.ctx,f.pool,f.agentID)
	if err!=nil{t.Fatal(err)}
	requireEnrollmentPreflightHistoricalV1(t,result)
	// No "enrolled" / "ready" phase was added and no application callback
	// ever receives a host-trust or CanUnlock flag in this snapshot type.
	var phase string
	if err:=f.pool.QueryRow(f.ctx,`
SELECT phase FROM vpn_runtime_recovery_enrollment_preparations
WHERE id=$1::uuid
`,id).Scan(&phase);err!=nil{t.Fatal(err)}
	if phase!="prepared_unattested" || strings.Contains(result.Assessment,"ready") {
		t.Fatalf("pure inspection activated previously dormant enrollment: %+v phase=%s",result,phase)
	}
}
