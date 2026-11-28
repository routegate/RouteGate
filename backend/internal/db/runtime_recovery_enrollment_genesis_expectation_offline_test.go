package db

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/ikaevus/routegate/backend/internal/recoveryenroll"
)

func requireGenesisManagerExpectationRefusedV1(t *testing.T,
	ctx context.Context, f recoveryReservationFixture, agentID string,
) {
	t.Helper()
	got, err := inspectRecoveryGenesisManagerExpectationOfflineV1(ctx, f.pool, agentID)
	if !errors.Is(err, errRecoveryGenesisManagerExpectationRefusedV1) ||
		got != (recoveryGenesisManagerExpectationV1{}) {
		t.Fatalf("invalid independent Manager expectation accepted: %+v %v", got, err)
	}
}

func TestRuntimeRecoveryEnrollmentGenesisExpectationV1PreparedSnapshotUnattested(t *testing.T) {
	f := newRecoveryReservationFixture(t)
	defer f.done()
	f.enableFencing(t)
	requireGenesisManagerExpectationRefusedV1(t, f.ctx, f, f.agentID)
	id, err := prepareRecoveryEnrollmentV1(f.ctx, f)
	if err != nil { t.Fatal(err) }
	got, err := inspectRecoveryGenesisManagerExpectationOfflineV1(f.ctx, f.pool, f.agentID)
	if err != nil || !got.Unattested ||
		got.AgentID != f.agentID || got.ServerID != f.serverID ||
		got.PreparationID != id ||
		got.AgentCredentialGeneration != f.generation ||
		len(got.GenesisSHA256) != 64 {
		t.Fatalf("Manager genesis expectation absent or promoted: %+v %v", got, err)
	}
	expected, err := recoveryenroll.OfflineGenesisDigestUnattestedV1(
		recoveryenroll.Preparation{
			ID: id, AgentID: f.agentID, ServerID: f.serverID,
			AgentCredentialGeneration: f.generation,
			Phase: recoveryenroll.PhasePreparedUnattested,
		},
	)
	if err != nil || expected != got.GenesisSHA256 {
		t.Fatalf("Agent/Manager genesis digest mismatch: got %q expected %q err=%v",
			got.GenesisSHA256, expected, err)
	}
	again, err := inspectRecoveryGenesisManagerExpectationOfflineV1(f.ctx, f.pool, f.agentID)
	if err != nil || again != got {
		t.Fatalf("read-only Manager snapshot changed: %+v -> %+v (%v)", got, again, err)
	}
	var phase string
	if err := f.pool.QueryRow(f.ctx, `
SELECT phase FROM vpn_runtime_recovery_enrollment_preparations WHERE id=$1::uuid
`, id).Scan(&phase); err != nil || phase != "prepared_unattested" {
		t.Fatalf("historical inspection changed the preparation phase: %q %v", phase, err)
	}
}

func TestRuntimeRecoveryEnrollmentGenesisExpectationV1StaleOrDisabledRefused(t *testing.T) {
	for _, mode := range []string{"rotated_generation", "disabled_agent", "lost_capability"} {
		t.Run(mode, func(t *testing.T) {
			f := newRecoveryReservationFixture(t)
			defer f.done()
			f.enableFencing(t)
			if _, err := prepareRecoveryEnrollmentV1(f.ctx, f); err != nil { t.Fatal(err) }
			var statement string
			switch mode {
			case "rotated_generation":
				statement = `UPDATE agents SET credential_generation=credential_generation+1,
 last_authenticated_heartbeat_at=NULL,last_authenticated_heartbeat_generation=NULL
WHERE id=$1::uuid`
			case "disabled_agent":
				statement = `UPDATE agents SET status='disabled' WHERE id=$1::uuid`
			case "lost_capability":
				statement = `UPDATE agents SET capabilities='{}'::jsonb WHERE id=$1::uuid`
			}
			if _, err := f.pool.Exec(f.ctx, statement, f.agentID); err != nil { t.Fatal(err) }
			requireGenesisManagerExpectationRefusedV1(t, f.ctx, f, f.agentID)
		})
	}
}

func TestRuntimeRecoveryEnrollmentGenesisExpectationV1NewManagerJobBlocksSnapshot(t *testing.T) {
	f := newRecoveryReservationFixture(t)
	defer f.done()
	f.enableFencing(t)
	if _, err := prepareRecoveryEnrollmentV1(f.ctx, f); err != nil { t.Fatal(err) }
	// A preparation is not a gate; legacy jobs can still be added, but
	// a supposedly quiet historical expectation MUST refuse that ambiguity.
	var jobID string
	if err := f.pool.QueryRow(f.ctx, `
INSERT INTO config_apply_jobs(server_id,agent_id,config_version_id,action,status)
VALUES ($1::uuid,$2::uuid,$3::uuid,'apply','pending') RETURNING id::text
`, f.serverID, f.agentID, f.configVersionID).Scan(&jobID); err != nil {
		t.Fatal(err)
	}
	requireGenesisManagerExpectationRefusedV1(t, f.ctx, f, f.agentID)
}

func TestRuntimeRecoveryEnrollmentGenesisExpectationV1InputRefusals(t *testing.T) {
	f := newRecoveryReservationFixture(t)
	defer f.done()
	f.enableFencing(t)
	if _, err := prepareRecoveryEnrollmentV1(f.ctx, f); err != nil { t.Fatal(err) }
	for _, tc := range []struct {
		name, id string
		ctx context.Context
	}{
		{"bad_id", "not-a-uuid", f.ctx},
		{"missing", "", f.ctx},
		{"unknown", "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", f.ctx},
		{"nil_context", f.agentID, nil},
		{"cancelled", f.agentID, func() context.Context {
			ctx, cancel := context.WithCancel(f.ctx); cancel(); return ctx
		}()},
		{"sql_injection", f.agentID + strings.Repeat("'", 1), f.ctx},
	} {
		t.Run(tc.name, func(t *testing.T) {
			requireGenesisManagerExpectationRefusedV1(t, tc.ctx, f, tc.id)
		})
	}
}
