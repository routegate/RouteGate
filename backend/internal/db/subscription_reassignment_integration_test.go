package db

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"log/slog"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/ikaevus/routegate/backend/internal/configs"
	"github.com/ikaevus/routegate/backend/internal/transfers"
	"github.com/ikaevus/routegate/backend/internal/vpnaccounts"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Every stage is tested against the existing device bearer in both directions;
// failed preparation cannot interrupt the source's already applied material.
func TestDeviceSubscriptionSurvivesBidirectionalHybridVPNNodeReassignment(t *testing.T) {
	for _, tc := range []struct {
		name, sourceRole, targetRole string
		sourcePort, targetPort       int
	}{
		{"hybrid_to_vpn_and_back", "hybrid", "vpn", 8443, 443},
		{"vpn_to_hybrid_and_back", "vpn", "hybrid", 443, 8443},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, pool, done := setupAppliedSettingsTest(t)
			defer done()
			sk, tk := newRealityTestKey(t), newRealityTestKey(t)
			source, account, other := createAppliedSettingsServer(t, ctx, pool, sk)
			target := createReassignmentTarget(t, ctx, pool, tk)
			setReassignmentNodeTopology(t, ctx, pool, source, tc.sourceRole, tc.sourcePort)
			setReassignmentNodeTopology(t, ctx, pool, target, tc.targetRole, tc.targetPort)
			addTransferAgent(t, ctx, pool, source)
			addTransferAgent(t, ctx, pool, target)
			configSvc := configs.NewService(configs.NewRepository(pool))
			sourceVersion := renderVersion(t, ctx, configSvc, source)
			first, err := configSvc.Apply(ctx, source, sourceVersion, configs.ApplyConfigRequest{})
			if err != nil {
				t.Fatal(err)
			}
			completeTransferApply(t, ctx, pool, first.Job.ID, "succeeded", true)
			accounts := vpnaccounts.NewRepository(pool)
			d, err := accounts.CreateDevice(ctx, vpnaccounts.CreateDeviceInput{VPNAccountID: account, Name: "iPhone test", ClientType: vpnaccounts.ClientTypeHiddify, DeviceType: vpnaccounts.DevicePlatformIOS})
			if err != nil {
				t.Fatal(err)
			}
			raw := "rg-140-stable-device-token"
			token, err := accounts.CreateDeviceSubscriptionToken(ctx, d.ID, vpnaccounts.HashSubscriptionToken(raw), nil)
			if err != nil {
				t.Fatal(err)
			}
			probe := newSubscriptionProbe(t, ctx, pool, accounts)
			assertServed := func(host string, port int, key realityTestKey) {
				t.Helper()
				req := httptest.NewRequest(http.MethodGet, "/sub/"+raw+"?format=raw", nil)
				req.SetPathValue("token", raw)
				rec := httptest.NewRecorder()
				probe.handler.GetClientSubscription(rec, req)
				if rec.Code != 200 || !strings.Contains(rec.Body.String(), host+":"+strconv.Itoa(port)) || !strings.Contains(rec.Body.String(), key.public) {
					t.Fatalf("wrong applied subscription: code=%d error=%s", rec.Code, rec.Body.String())
				}
				now, e := accounts.GetActiveDeviceSubscriptionToken(ctx, d.ID)
				if e != nil || now.ID != token.ID {
					t.Fatal("device token changed")
				}
			}
			// An unrelated account assigned to either reserved node must be shown
			// the blocking transfer, without revealing credentials or token data.
			readTransfer := func(selected string) (transfers.Transfer, *transfers.NodeTransferBlocker, string) {
				t.Helper()
				req := httptest.NewRequest(http.MethodGet, "/api/v1/vpn-accounts/"+selected+"/transfer", nil)
				req.SetPathValue("id", selected)
				rec := httptest.NewRecorder()
				transfers.NewHandler(slog.Default(), pool).Latest(rec, req)
				if rec.Code != http.StatusOK {
					t.Fatalf("transfer read model status=%d", rec.Code)
				}
				var result struct {
					Transfer        *transfers.Transfer            `json:"transfer"`
					BlockingTransfer *transfers.NodeTransferBlocker `json:"blockingTransfer"`
				}
				if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
					t.Fatal(err)
				}
				var own transfers.Transfer
				if result.Transfer != nil {
					own = *result.Transfer
				}
				return own, result.BlockingTransfer, rec.Body.String()
			}
			svc := transfers.NewServiceWithCheck(pool, func(context.Context, string, int) error { return nil })
			act := func(tr transfers.Transfer, action string, ack bool) transfers.Transfer {
				t.Helper()
				v, e := svc.Act(ctx, account, tr.ID, action, "test-operator", ack)
				if e != nil {
					t.Fatalf("%s: %v", action, e)
				}
				return v
			}
			assertServed("203.0.113.10", tc.sourcePort, sk)
			// The old manual and bulk SQL paths are guarded even without a UI.
			if _, e := accounts.UpdateAccount(ctx, account, vpnaccounts.UpdateAccountInput{ServerID: &target}); e == nil {
				t.Fatal("manual path bypassed staging")
			}
			tr, err := svc.Start(ctx, account, target, "test-operator")
			if err != nil {
				t.Fatal(err)
			}
			own, blocker, readBody := readTransfer(other)
			if blocker == nil || blocker.AccountID != account || blocker.SourceServerID != source ||
				blocker.TargetServerID != target || blocker.State != tr.State || own.ID != "" {
				t.Fatal("unrelated account did not receive exact active node reservation evidence")
			}
			if strings.Contains(readBody, raw) || strings.Contains(readBody, "tokenHash") ||
				strings.Contains(readBody, "vlessUuid") || strings.Contains(readBody, "privateKey") {
				t.Fatal("read-only blocker exposed sensitive subscription or protocol material")
			}
			own, blocker, _ = readTransfer(account)
			if own.ID != tr.ID || blocker != nil {
				t.Fatal("reserved account must show its own transfer, not an unrelated blocker")
			}
			again, e := svc.Start(ctx, account, target, "test-operator")
			if e != nil || again.ID != tr.ID {
				t.Fatal("duplicate start queued another transfer")
			}
			if _, e = svc.Start(ctx, other, target, "test-operator"); e == nil {
				t.Fatal("conflicting node reservation accepted")
			}
			if _, e = configSvc.Apply(ctx, source, sourceVersion, configs.ApplyConfigRequest{}); e == nil {
				t.Fatal("generic apply bypassed node reservation")
			}
			if _, e = pool.Exec(ctx, `UPDATE servers SET vless_port=vless_port+1 WHERE id=$1::uuid`, target); e == nil {
				t.Fatal("target settings edit bypassed reservation")
			}
			if _, e = pool.Exec(ctx, `DELETE FROM config_apply_jobs WHERE id=$1::uuid`, tr.TargetJobID); e == nil {
				t.Fatal("active proof deletion accepted")
			}
			if _, e = svc.Act(ctx, account, tr.ID, "cutover", "test", false); e == nil {
				t.Fatal("cutover before apply allowed")
			}
			assertServed("203.0.113.10", tc.sourcePort, sk)
			completeTransferApply(t, ctx, pool, tr.TargetJobID, "failed", false)
			if _, e = svc.Act(ctx, account, tr.ID, "verify", "test", false); e == nil {
				t.Fatal("failed target verified")
			}
			assertServed("203.0.113.10", tc.sourcePort, sk)
			tr = act(tr, "retry", false)
			// A success response with service control disabled is not readiness proof.
			completeTransferApply(t, ctx, pool, tr.TargetJobID, "succeeded", false)
			if _, e = svc.Act(ctx, account, tr.ID, "verify", "test", false); e == nil {
				t.Fatal("skipped runtime accepted")
			}
			assertServed("203.0.113.10", tc.sourcePort, sk)
			// Simulate corrected Agent evidence for the same applied task, without
			// changing membership, tokens, or runtime version.
			setTransferReport(t, ctx, pool, tr.TargetJobID, true)
			blocked := transfers.NewServiceWithCheck(pool, func(context.Context, string, int) error { return errors.New("blocked") })
			if _, e = blocked.Act(ctx, account, tr.ID, "verify", "test", false); e == nil {
				t.Fatal("unreachable target accepted")
			}
			tr = act(tr, "verify", false)
			assertServed("203.0.113.10", tc.sourcePort, sk)
			tr = act(tr, "cutover", false)
			assertServed("203.0.113.20", tc.targetPort, tk)
			// Reconstruct the service to prove recovery reads persisted state.
			svc = transfers.NewServiceWithCheck(pool, func(context.Context, string, int) error { return nil })
			latest, e := svc.Latest(ctx, account)
			if e != nil || latest.State != "client_refresh_pending" || len(latest.Devices) != 1 || !latest.Devices[0].RequestedAfterCutover {
				t.Fatalf("missing refresh evidence: %v %+v", e, latest)
			}
			if _, e = svc.Act(ctx, account, tr.ID, "cleanup", "test", false); e == nil {
				t.Fatal("cleanup without explicit client verification allowed")
			}
			// Rendering either node while waiting retains the migrated account.
			for _, node := range []string{source, target} {
				v := renderVersion(t, ctx, configSvc, node)
				var n bool
				if e = pool.QueryRow(ctx, `SELECT client_settings->'accounts' ? $2 FROM config_versions WHERE id=$1::uuid`, v, account).Scan(&n); e != nil || !n {
					t.Fatal("render lost retained membership")
				}
			}
			tr = act(tr, "cleanup", true)
			completeTransferApply(t, ctx, pool, tr.CleanupJobID, "failed", false)
			if _, e = svc.Act(ctx, account, tr.ID, "finish", "test", false); e == nil {
				t.Fatal("failed cleanup completed")
			}
			if _, e = svc.Act(ctx, account, tr.ID, "rollback", "test", true); e == nil {
				t.Fatal("unsafe rollback after cleanup started")
			}
			tr = act(tr, "retry", false)
			completeTransferApply(t, ctx, pool, tr.CleanupJobID, "succeeded", true)
			tr = act(tr, "finish", false)
			if tr.State != "complete" {
				t.Fatal(tr.State)
			}
			_, blocker, _ = readTransfer(other)
			if blocker != nil {
				t.Fatal("completed transfer still shown as a node reservation")
			}
			assertServed("203.0.113.20", tc.targetPort, tk)
			var kept bool
			if e = pool.QueryRow(ctx, `SELECT cv.client_settings->'accounts' ? $2 FROM servers s JOIN config_versions cv ON cv.id=s.active_config_version_id WHERE s.id=$1::uuid`, source, other).Scan(&kept); e != nil || !kept {
				t.Fatal("cleanup removed unrelated account")
			}
			// Reverse direction is a new operation, then exercise post-cutover rollback.
			back, e := svc.Start(ctx, account, source, "test")
			if e != nil {
				t.Fatal(e)
			}
			assertServed("203.0.113.20", tc.targetPort, tk)
			completeTransferApply(t, ctx, pool, back.TargetJobID, "succeeded", true)
			back = act(back, "verify", false)
			back = act(back, "cutover", false)
			assertServed("203.0.113.10", tc.sourcePort, sk)
			back = act(back, "rollback", true)
			assertServed("203.0.113.20", tc.targetPort, tk)
			completeTransferApply(t, ctx, pool, back.CleanupJobID, "succeeded", true)
			back = act(back, "finish", false)
			if back.State != "rolled_back" {
				t.Fatal(back.State)
			}
			// A fresh reverse move can still complete after rollback.
			back, e = svc.Start(ctx, account, source, "test")
			if e != nil {
				t.Fatal(e)
			}
			completeTransferApply(t, ctx, pool, back.TargetJobID, "succeeded", true)
			back = act(back, "verify", false)
			back = act(back, "cutover", false)
			back = act(back, "cleanup", true)
			completeTransferApply(t, ctx, pool, back.CleanupJobID, "succeeded", true)
			back = act(back, "finish", false)
			assertServed("203.0.113.10", tc.sourcePort, sk)
		})
	}
}

func addTransferAgent(t *testing.T, ctx context.Context, pool *pgxpool.Pool, node string) {
	t.Helper()
	_, err := pool.Exec(ctx, `INSERT INTO agents(server_id,token_hash,agent_version,hostname,os,arch,status,last_seen_at,last_authenticated_heartbeat_at,last_authenticated_heartbeat_generation,capabilities) VALUES($1::uuid,$1,'test','test','linux','amd64','online',now(),now(),1,
 '{"sing-box":true,"systemctl":true,"ss":true,"vpnCores":[{"type":"sing-box","state":"running"}],"routegate":{"schemaVersion":1,"vpnCoreAdapters":[{"core":"sing-box","protocol":"vless","transports":["tcp"],"securityModes":["reality"]}]}}'::jsonb)`, node)
	if err != nil {
		t.Fatal(err)
	}
}
func completeTransferApply(t *testing.T, ctx context.Context, pool *pgxpool.Pool, job, status string, healthy bool) {
	t.Helper()
	if binary := os.Getenv("ROUTEGATE_TEST_SING_BOX"); binary != "" && healthy {
		var payload []byte
		if err := pool.QueryRow(ctx, `SELECT cv.rendered_config FROM config_apply_jobs j JOIN config_versions cv ON cv.id=j.config_version_id WHERE j.id=$1::uuid`, job).Scan(&payload); err != nil {
			t.Fatal(err)
		}
		var rendered configs.RenderedConfig
		if err := json.Unmarshal(payload, &rendered); err != nil {
			t.Fatal(err)
		}
		dir := t.TempDir()
		rendered.SingBox.Log.Output = filepath.Join(dir, "presence.log")
		raw, err := json.Marshal(rendered.SingBox)
		if err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(dir, "sing-box.json")
		if err = os.WriteFile(path, raw, 0600); err != nil {
			t.Fatal(err)
		}
		if _, err = exec.CommandContext(ctx, binary, "check", "-c", path).CombinedOutput(); err != nil {
			t.Fatalf("sing-box rejected generated configuration: %v", err)
		}
	}
	setTransferReport(t, ctx, pool, job, healthy)
	if _, err := pool.Exec(ctx, `UPDATE config_apply_jobs SET status=$2,completed_at=now(),updated_at=now() WHERE id=$1::uuid`, job, status); err != nil {
		t.Fatal(err)
	}
}
func setTransferReport(t *testing.T, ctx context.Context, pool *pgxpool.Pool, job string, healthy bool) {
	t.Helper()
	stage := "skipped_service_control_disabled"
	if healthy {
		stage = "succeeded"
	}
	_, err := pool.Exec(ctx, `UPDATE config_apply_jobs j SET result_payload=jsonb_build_object('stage',$2::text,'components',jsonb_build_array(jsonb_build_object('core','sing-box','protocol','vless','status','succeeded','listenerPort',(cv.client_settings->>'vlessPort')::int))) FROM config_versions cv WHERE j.id=$1::uuid AND cv.id=j.config_version_id`, job, stage)
	if err != nil {
		t.Fatal(err)
	}
}

func createReassignmentTarget(t *testing.T, ctx context.Context, pool *pgxpool.Pool, key realityTestKey) string {
	t.Helper()
	var id string
	if err := pool.QueryRow(ctx, `
		INSERT INTO servers (
			name, status, deployment_role, public_ip, vpn_protocol, vless_port,
			vless_flow, vless_network, reality_private_key, reality_public_key,
			reality_short_id, reality_server_name
		) VALUES (
			'rg-140-target', 'active', 'vpn', '203.0.113.20', 'vless', 8443,
			'xtls-rprx-vision', 'tcp', $1, $2,
			'fedcba9876543210', 'www.microsoft.com'
		) RETURNING id::text
	`, key.private, key.public).Scan(&id); err != nil {
		t.Fatalf("create reassignment target: %v", err)
	}
	return id
}

func setReassignmentNodeTopology(t *testing.T, ctx context.Context, pool *pgxpool.Pool, serverID, role string, port int) {
	t.Helper()
	if _, err := pool.Exec(ctx, `
		UPDATE servers SET deployment_role = $2, vless_port = $3
		WHERE id = $1::uuid
	`, serverID, role, port); err != nil {
		t.Fatalf("set %s node topology on port %d: %v", role, port, err)
	}
}
