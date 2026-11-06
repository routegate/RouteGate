package db

import (
	"context"
	"strings"
	"testing"

	"github.com/ikaevus/routegate/backend/internal/configs"
	"github.com/ikaevus/routegate/backend/internal/transfers"
	"github.com/ikaevus/routegate/backend/internal/vpnaccounts"
	"github.com/jackc/pgx/v5/pgxpool"
)

func transferFixture(t *testing.T) (context.Context, *pgxpool.Pool, string, string, string, func()) {
	return transferFixtureWithSourceSetup(t, nil)
}

func transferFixtureWithSourceSetup(t *testing.T, setup func(context.Context, *pgxpool.Pool, string)) (context.Context, *pgxpool.Pool, string, string, string, func()) {
	t.Helper()
	ctx, pool, done := setupAppliedSettingsTest(t)
	source, account, _ := createAppliedSettingsServer(t, ctx, pool, newRealityTestKey(t))
	target := createReassignmentTarget(t, ctx, pool, newRealityTestKey(t))
	addTransferAgent(t, ctx, pool, source)
	addTransferAgent(t, ctx, pool, target)
	if setup != nil {
		setup(ctx, pool, source)
	}
	svc := configs.NewService(configs.NewRepository(pool))
	version := renderVersion(t, ctx, svc, source)
	job, err := svc.Apply(ctx, source, version, configs.ApplyConfigRequest{})
	if err != nil {
		t.Fatal(err)
	}
	completeTransferApply(t, ctx, pool, job.Job.ID, "succeeded", true)
	return ctx, pool, source, target, account, done
}

func TestStagedTransferRejectsUnsafePreflight(t *testing.T) {
	for _, tc := range []struct{ name, sql, reason string }{
		{"management_target", `UPDATE servers SET deployment_role='management' WHERE id=$1::uuid`, "node_role_protocol_or_agent_not_ready"},
		{"offline_target", `UPDATE agents SET status='offline' WHERE server_id=$1::uuid`, "node_role_protocol_or_agent_not_ready"},
		{"unverified_heartbeat", `UPDATE agents SET last_authenticated_heartbeat_at=NULL,last_authenticated_heartbeat_generation=NULL WHERE server_id=$1::uuid`, "node_role_protocol_or_agent_not_ready"},
		{"missing_reality_capability", `UPDATE agents SET capabilities=capabilities-'routegate' WHERE server_id=$1::uuid`, "node_role_protocol_or_agent_not_ready"},
		{"pending_source_change", `UPDATE servers SET vless_port=9844 WHERE id=$1::uuid`, "node_has_pending_configuration_changes"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, pool, source, target, account, done := transferFixture(t)
			defer done()
			node := target
			if tc.name == "pending_source_change" {
				node = source
			}
			if _, err := pool.Exec(ctx, tc.sql, node); err != nil {
				t.Fatal(err)
			}
			_, err := transfers.NewServiceWithCheck(pool, func(context.Context, string, int) error { return nil }).Start(ctx, account, target, "operator")
			if err == nil || !strings.Contains(err.Error(), tc.reason) {
				t.Fatalf("wrong gate: %v", err)
			}
			var count int
			if err = pool.QueryRow(ctx, `SELECT count(*) FROM vpn_account_transfers`).Scan(&count); err != nil || count != 0 {
				t.Fatal("preflight left a partial operation")
			}
		})
	}
}

func TestStagedTransferConcurrentStartAndPreCutoverCancellation(t *testing.T) {
	ctx, pool, source, target, account, done := transferFixture(t)
	defer done()
	svc := transfers.NewServiceWithCheck(pool, func(context.Context, string, int) error { return nil })
	type result struct {
		tr  transfers.Transfer
		err error
	}
	results := make(chan result, 2)
	for i := 0; i < 2; i++ {
		go func() { tr, err := svc.Start(ctx, account, target, "operator"); results <- result{tr, err} }()
	}
	a, b := <-results, <-results
	if a.err != nil || b.err != nil || a.tr.ID != b.tr.ID {
		t.Fatalf("concurrent start not idempotent: %v %v", a.err, b.err)
	}
	var jobs int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM config_apply_jobs WHERE server_id=$1::uuid`, target).Scan(&jobs); err != nil || jobs != 1 {
		t.Fatal("duplicate target task")
	}
	if _, err := pool.Exec(ctx, `UPDATE config_apply_jobs SET status='in_progress',started_at=now() WHERE id=$1::uuid`, a.tr.TargetJobID); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Act(ctx, account, a.tr.ID, "rollback", "operator", false); err == nil {
		t.Fatal("cancelled a running target task")
	}
	completeTransferApply(t, ctx, pool, a.tr.TargetJobID, "failed", false)
	tr, err := svc.Act(ctx, account, a.tr.ID, "rollback", "operator", false)
	if err != nil {
		t.Fatal(err)
	}
	completeTransferApply(t, ctx, pool, tr.CleanupJobID, "succeeded", true)
	svc = transfers.NewServiceWithCheck(pool, func(context.Context, string, int) error { return nil })
	tr, err = svc.Act(ctx, account, tr.ID, "finish", "operator", false)
	if err != nil || tr.State != "cancelled" {
		t.Fatalf("cancellation: %v %+v", err, tr)
	}
	var node string
	var locks int
	if err = pool.QueryRow(ctx, `SELECT server_id::text FROM vpn_accounts WHERE id=$1::uuid`, account).Scan(&node); err != nil || node != source {
		t.Fatal("source changed before cutover")
	}
	if err = pool.QueryRow(ctx, `SELECT count(*) FROM vpn_account_transfer_nodes`).Scan(&locks); err != nil || locks != 0 {
		t.Fatal("terminal operation retained node locks")
	}
	if _, err = svc.Start(ctx, account, target, "operator"); err != nil {
		t.Fatalf("cannot stage to an empty cleaned node: %v", err)
	}
	// Empty rejecting cleanup cannot be requested from the ordinary API.
	if _, err = configs.NewService(configs.NewRepository(pool)).RenderTransferCleanup(ctx, target, tr.ID); err == nil {
		t.Fatal("cleanup renderer accepted an unreserved terminal transfer")
	}
}

func TestAutomaticSelectionStagesDeployedAccountInsteadOfAssigning(t *testing.T) {
	ctx, pool, source, target, account, done := transferFixture(t)
	defer done()
	var group string
	if err := pool.QueryRow(ctx, `INSERT INTO node_groups(name,selection_strategy) VALUES('RG-140 priority','priority') RETURNING id::text`).Scan(&group); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO node_group_members(node_group_id,server_id,priority) VALUES($1::uuid,$2::uuid,100),($1::uuid,$3::uuid,10)`, group, source, target); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO vpn_account_node_groups(vpn_account_id,node_group_id) VALUES($1::uuid,$2::uuid)`, account, group); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO vpn_account_automatic_selection_policies(vpn_account_id,enabled,allow_degraded,cooldown_seconds) VALUES($1::uuid,true,true,300)`, account); err != nil {
		t.Fatal(err)
	}
	accounts := vpnaccounts.NewRepository(pool)
	result, err := accounts.ApplyAutomaticSelection(ctx, account)
	if err != nil {
		t.Fatal(err)
	}
	if result.Transfer == nil || result.Changed || result.ConfigDeploymentRequired {
		t.Fatal("selection did not return a staged operation")
	}
	if result.Transfer.TargetID != target {
		t.Fatal("wrong selected target")
	}
	current, err := accounts.GetAccountByID(ctx, account)
	if err != nil || current.ServerID != source {
		t.Fatal("automatic selection switched canonical assignment before target apply")
	}
}

func TestStagedTransferInventoryAndPendingCancellation(t *testing.T) {
	ctx, pool, _, target, account, done := transferFixture(t)
	defer done()
	accounts := vpnaccounts.NewRepository(pool)
	d, err := accounts.CreateDevice(ctx, vpnaccounts.CreateDeviceInput{VPNAccountID: account, Name: "Original phone", ClientType: vpnaccounts.ClientTypeHiddify, DeviceType: vpnaccounts.DevicePlatformIOS})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = accounts.CreateDeviceSubscriptionToken(ctx, d.ID, vpnaccounts.HashSubscriptionToken("original-fixture"), nil); err != nil {
		t.Fatal(err)
	}
	svc := transfers.NewServiceWithCheck(pool, func(context.Context, string, int) error { return nil })
	tr, err := svc.Start(ctx, account, target, "operator")
	if err != nil {
		t.Fatal(err)
	}
	// Genuine replacement remains possible, without silently counting it as a
	// refresh of the originally imported link or removing it from the inventory.
	if _, err = accounts.CreateDeviceSubscriptionToken(ctx, d.ID, vpnaccounts.HashSubscriptionToken("replacement-fixture"), nil); err != nil {
		t.Fatal(err)
	}
	n, err := accounts.CreateDevice(ctx, vpnaccounts.CreateDeviceInput{VPNAccountID: account, Name: "New phone", ClientType: vpnaccounts.ClientTypeHiddify, DeviceType: vpnaccounts.DevicePlatformIOS})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = accounts.CreateDeviceSubscriptionToken(ctx, n.ID, vpnaccounts.HashSubscriptionToken("new-fixture"), nil); err != nil {
		t.Fatal(err)
	}
	latest, err := svc.Latest(ctx, account)
	if err != nil || len(latest.Devices) != 2 {
		t.Fatalf("inventory missing devices: %v %+v", err, latest)
	}
	if !latest.Devices[0].LinkChanged || latest.Devices[0].RequestedAfterCutover {
		t.Fatal("replacement falsely counted as original refresh")
	}
	// This task is pending and has never been claimed: cancellation must commit
	// its failed state atomically with the separately linked target cleanup task.
	tr, err = svc.Act(ctx, account, tr.ID, "rollback", "operator", false)
	if err != nil {
		t.Fatal(err)
	}
	completeTransferApply(t, ctx, pool, tr.CleanupJobID, "succeeded", true)
	tr, err = svc.Act(ctx, account, tr.ID, "finish", "operator", false)
	if err != nil || tr.CompletedAt == nil || tr.State != "cancelled" {
		t.Fatalf("pending cancellation: %v %+v", err, tr)
	}
	if _, err = pool.Exec(ctx, `DELETE FROM servers WHERE id=$1::uuid`, target); err != nil {
		t.Fatalf("retiring cleaned node was blocked by terminal history: %v", err)
	}
}
