package db

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/ikaevus/routegate/backend/internal/configs"
	"github.com/ikaevus/routegate/backend/internal/revocations"
	"github.com/ikaevus/routegate/backend/internal/transfers"
	"github.com/ikaevus/routegate/backend/internal/vpnaccounts"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

func removalPreparationFixture(t *testing.T) (context.Context, *pgxpool.Pool, revocations.PrepareInput, string, func()) {
	t.Helper()
	ctx, pool, node, target, account, done := transferFixture(t)
	in := revocations.PrepareInput{AccountID: account, ServerID: node, Actor: "operator"}
	if _, err := pool.Exec(ctx, `INSERT INTO vpn_client_profiles(vpn_account_id,active_protocol) VALUES($1::uuid,'vless') ON CONFLICT(vpn_account_id) DO NOTHING`, account); err != nil {
		done()
		t.Fatal(err)
	}
	accounts := vpnaccounts.NewRepository(pool)
	device, err := accounts.CreateDevice(ctx, vpnaccounts.CreateDeviceInput{VPNAccountID: account, Name: "Existing device", ClientType: vpnaccounts.ClientTypeHiddify, DeviceType: vpnaccounts.DevicePlatformIOS})
	if err != nil {
		done()
		t.Fatal(err)
	}
	if _, err = accounts.CreateDeviceSubscriptionToken(ctx, device.ID, vpnaccounts.HashSubscriptionToken("existing-device-token"), nil); err != nil {
		done()
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT cv.id::text,cv.config_hash FROM servers s JOIN config_versions cv ON cv.id=s.active_config_version_id WHERE s.id=$1::uuid`, node).Scan(&in.BaselineVersionID, &in.BaselineHash); err != nil {
		done()
		t.Fatal(err)
	}
	return ctx, pool, in, target, done
}
func TestCredentialRevocationPreparationPreservesAccountAndVersions(t *testing.T) {
	for _, status := range []string{"active", "suspended", "revoked"} {
		t.Run(status, func(t *testing.T) {
			ctx, pool, in, _, done := removalPreparationFixture(t)
			defer done()
			if _, err := pool.Exec(ctx, `UPDATE vpn_accounts SET status=$2 WHERE id=$1::uuid`, in.AccountID, status); err != nil {
				t.Fatal(err)
			}
			var beforeTokens, afterTokens string
			if err := pool.QueryRow(ctx, `SELECT COALESCE(jsonb_agg(to_jsonb(t) ORDER BY id)::text,'[]') FROM vpn_subscription_tokens t`).Scan(&beforeTokens); err != nil {
				t.Fatal(err)
			}
			var beforeVersions, beforeJobs int
			var before string
			if err := pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM config_versions),(SELECT count(*) FROM config_apply_jobs),row_to_json(a)::text FROM vpn_accounts a WHERE id=$1::uuid`, in.AccountID).Scan(&beforeVersions, &beforeJobs, &before); err != nil {
				t.Fatal(err)
			}
			// The comparison must actually work in a READ ONLY transaction, not merely
			// roll back hidden rendering/credential-generation writes afterwards.
			tx, err := pool.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly})
			if err != nil {
				t.Fatal(err)
			}
			repo := configs.NewTransactionRepository(tx)
			baseline, err := repo.GetConfigVersion(ctx, in.ServerID, in.BaselineVersionID)
			if err == nil {
				err = repo.CheckRemovalDesiredBaseline(ctx, baseline, in.AccountID)
			}
			_ = tx.Rollback(ctx)
			if err != nil {
				t.Fatal("read-only baseline", err)
			}
			service := revocations.NewService(pool)
			p, err := service.Prepare(ctx, in)
			if err != nil {
				t.Fatal(err)
			}
			if p.State != "prepared" || p.AccountID != in.AccountID {
				t.Fatal(p)
			}
			again, err := service.Prepare(ctx, in)
			if err != nil || again.ID != p.ID {
				t.Fatal("retry", again, err)
			}
			var afterVersions, afterJobs int
			var after string
			if err = pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM config_versions),(SELECT count(*) FROM config_apply_jobs),row_to_json(a)::text FROM vpn_accounts a WHERE id=$1::uuid`, in.AccountID).Scan(&afterVersions, &afterJobs, &after); err != nil {
				t.Fatal(err)
			}
			if err := pool.QueryRow(ctx, `SELECT COALESCE(jsonb_agg(to_jsonb(t) ORDER BY id)::text,'[]') FROM vpn_subscription_tokens t`).Scan(&afterTokens); err != nil {
				t.Fatal(err)
			}
			if beforeTokens != afterTokens {
				t.Fatal("subscription token was replaced or changed")
			}
			if beforeVersions != afterVersions || beforeJobs != afterJobs || before != after {
				t.Fatal("preparation changed account or queued/rendered configuration")
			}
			var candidate []byte
			var oldHash, newHash string
			if err = pool.QueryRow(ctx, `SELECT candidate_config,baseline_runtime_hash,candidate_runtime_hash FROM vpn_credential_revocations WHERE id=$1::uuid`, p.ID).Scan(&candidate, &oldHash, &newHash); err != nil {
				t.Fatal(err)
			}
			var rendered configs.RenderedConfig
			_ = json.Unmarshal(candidate, &rendered)
			for _, a := range rendered.VPNAccounts {
				if a.ID == in.AccountID {
					t.Fatal("target remains in candidate")
				}
			}
			if oldHash == newHash {
				t.Fatal("runtime digest did not change")
			}
			safe, _ := json.Marshal(p)
			if strings.Contains(string(safe), "candidate") || strings.Contains(string(safe), "vlessUuid") {
				t.Fatal("secret-bearing API response")
			}
			cancelled, err := service.Cancel(ctx, p.ID, "operator-2")
			if err != nil || cancelled.State != "cancelled" {
				t.Fatal(cancelled, err)
			}
			if _, err = service.Cancel(ctx, p.ID, "operator-2"); err != nil {
				t.Fatal(err)
			}
			var events int
			if err = pool.QueryRow(ctx, `SELECT count(*) FROM vpn_credential_revocation_events WHERE operation_id=$1::uuid`, p.ID).Scan(&events); err != nil || events != 2 {
				t.Fatal("audit not transactional/idempotent", events, err)
			}
			if _, err = pool.Exec(ctx, `UPDATE vpn_accounts SET display_name='after cancellation' WHERE id=$1::uuid`, in.AccountID); err != nil {
				t.Fatal("reservation not released", err)
			}
			if _, err = pool.Exec(ctx, `UPDATE config_versions SET applied_at=now() WHERE id=$1::uuid`, in.BaselineVersionID); err != nil {
				t.Fatal("cancelled preparation blocks later legitimate apply metadata", err)
			}
			if _, err = pool.Exec(ctx, `UPDATE config_versions SET config_hash=repeat('a',64) WHERE id=$1::uuid`, in.BaselineVersionID); err == nil {
				t.Fatal("cancelled baseline proof mutable")
			}
		})
	}
}
func TestCredentialRevocationReservationsBlockConflicts(t *testing.T) {
	ctx, pool, in, target, done := removalPreparationFixture(t)
	defer done()
	service := revocations.NewService(pool)
	p, err := service.Prepare(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	statements := []struct {
		name, sql string
		args      []any
	}{
		{"status", `UPDATE vpn_accounts SET status='revoked' WHERE id=$1::uuid`, []any{in.AccountID}},
		{"delete", `DELETE FROM vpn_accounts WHERE id=$1::uuid`, []any{in.AccountID}},
		{"settings", `UPDATE servers SET vless_port=19443 WHERE id=$1::uuid`, []any{in.ServerID}},
		{"active pointer", `UPDATE servers SET active_config_version_id=NULL WHERE id=$1::uuid`, []any{in.ServerID}},
		{"apply", `INSERT INTO config_apply_jobs(server_id,agent_id,config_version_id,action) SELECT server_id,id,$2::uuid,'apply' FROM agents WHERE server_id=$1::uuid`, []any{in.ServerID, in.BaselineVersionID}},
		{"job cleanup", `DELETE FROM config_apply_jobs WHERE server_id=$1::uuid`, []any{in.ServerID}},
		{"version cleanup", `DELETE FROM config_versions WHERE id=$1::uuid`, []any{in.BaselineVersionID}},
		{"protocol preferences", `UPDATE vpn_client_profiles SET protocol='shadowsocks' WHERE vpn_account_id=$1::uuid`, []any{in.AccountID}},
		{"agent rotation", `UPDATE agents SET token_hash='replacement' WHERE server_id=$1::uuid`, []any{in.ServerID}},
		{"shared routing", `INSERT INTO routing_profiles(name) VALUES('blocked shared profile')`, nil},
		{"immutable ledger", `UPDATE vpn_credential_revocations SET baseline_hash=repeat('a',64) WHERE id=$1::uuid`, []any{p.ID}},
		{"no false confirmation", `UPDATE vpn_credential_revocations SET state='confirmed' WHERE id=$1::uuid`, []any{p.ID}},
		{"immutable audit", `DELETE FROM vpn_credential_revocation_events WHERE operation_id=$1::uuid`, []any{p.ID}},
	}
	for _, stmt := range statements {
		t.Run(stmt.name, func(t *testing.T) {
			_, err := pool.Exec(ctx, stmt.sql, stmt.args...)
			var pgerr *pgconn.PgError
			if !errors.As(err, &pgerr) || pgerr.Code != "P0141" {
				t.Fatalf("wrong guard: %v", err)
			}
		})
	}
	if _, err = transfers.NewServiceWithCheck(pool, func(context.Context, string, int) error { return nil }).Start(ctx, in.AccountID, target, "operator"); err == nil {
		t.Fatal("transfer started during reservation")
	}
	// Heartbeat must remain writable. It is liveness, not confirmation evidence.
	if _, err = pool.Exec(ctx, `UPDATE agents SET last_seen_at=now(),last_authenticated_heartbeat_at=now() WHERE server_id=$1::uuid`, in.ServerID); err != nil {
		t.Fatal("heartbeat blocked", err)
	}
	if _, err = service.Cancel(ctx, p.ID, in.Actor); err != nil {
		t.Fatal(err)
	}
}
func TestCredentialRevocationConcurrentPreparation(t *testing.T) {
	ctx, pool, in, _, done := removalPreparationFixture(t)
	defer done()
	service := revocations.NewService(pool)
	type result struct {
		p   revocations.Preparation
		err error
	}
	out := make(chan result, 2)
	for i := 0; i < 2; i++ {
		go func() { p, err := service.Prepare(ctx, in); out <- result{p, err} }()
	}
	a, b := <-out, <-out
	if a.err != nil || b.err != nil || a.p.ID != b.p.ID {
		t.Fatal("concurrent preparation", a, b)
	}
	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM vpn_credential_revocation_events`).Scan(&count); err != nil || count != 1 {
		t.Fatal("duplicate admission", count, err)
	}
	different := in
	different.Actor = "different-operator"
	if _, err := service.Prepare(ctx, different); err == nil {
		t.Fatal("rebound existing preparation")
	}
}
func TestCredentialRevocationPreflightFailsAtomically(t *testing.T) {
	for _, name := range []string{"stale heartbeat", "unapplied port", "unrelated account edit", "wrong baseline", "hidden disabled edit", "transfer reservation"} {
		t.Run(name, func(t *testing.T) {
			ctx, pool, in, target, done := removalPreparationFixture(t)
			defer done()
			var err error
			switch name {
			case "stale heartbeat":
				_, err = pool.Exec(ctx, `UPDATE agents SET last_authenticated_heartbeat_at=now()-interval '10 minutes' WHERE server_id=$1::uuid`, in.ServerID)
			case "unapplied port":
				_, err = pool.Exec(ctx, `UPDATE servers SET vless_port=19443 WHERE id=$1::uuid`, in.ServerID)
			case "unrelated account edit":
				_, err = pool.Exec(ctx, `UPDATE vpn_accounts SET display_name='pending edit' WHERE server_id=$1::uuid AND id<>$2::uuid`, in.ServerID, in.AccountID)
			case "wrong baseline":
				in.BaselineHash = strings.Repeat("f", 64)
			case "hidden disabled edit":
				_, err = pool.Exec(ctx, `INSERT INTO vpn_accounts(username,protocol,display_name,status,server_id) VALUES('new-disabled','sing-box','unapplied','suspended',$1::uuid)`, in.ServerID)
			case "transfer reservation":
				_, err = transfers.NewServiceWithCheck(pool, func(context.Context, string, int) error { return nil }).Start(ctx, in.AccountID, target, "operator")
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, err = revocations.NewService(pool).Prepare(ctx, in); !errors.Is(err, revocations.ErrPreflight) {
				t.Fatal("unsafe preflight accepted", err)
			}
			var count int
			if err = pool.QueryRow(ctx, `SELECT count(*) FROM vpn_credential_revocations`).Scan(&count); err != nil || count != 0 {
				t.Fatal("partial preparation", count, err)
			}
		})
	}
}

func TestCredentialRevocationSeesJobCommittedWhileAdmissionWaits(t *testing.T) {
	ctx, pool, in, _, done := removalPreparationFixture(t)
	defer done()
	blocker, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer blocker.Rollback(ctx)
	if _, err = blocker.Exec(ctx, `INSERT INTO config_apply_jobs(server_id,agent_id,config_version_id,action) SELECT server_id,id,$2::uuid,'apply' FROM agents WHERE server_id=$1::uuid`, in.ServerID, in.BaselineVersionID); err != nil {
		t.Fatal(err)
	}
	config, err := pgxpool.ParseConfig(os.Getenv("ROUTEGATE_TEST_DATABASE_URL"))
	if err != nil {
		t.Fatal(err)
	}
	config.ConnConfig.RuntimeParams["application_name"] = "routegate-revocation-admission-race"
	contender, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	defer contender.Close()
	result := make(chan error, 1)
	go func() { _, err := revocations.NewService(contender).Prepare(ctx, in); result <- err }()
	waiting := false
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		var count int
		if err = pool.QueryRow(ctx, `SELECT count(*) FROM pg_stat_activity WHERE application_name='routegate-revocation-admission-race' AND wait_event_type='Lock' AND query LIKE '%routegate_transfer_node_guard%'`).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count > 0 {
			waiting = true
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !waiting {
		t.Fatal("preparation did not wait on node admission")
	}
	if err = blocker.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case err = <-result:
		if !errors.Is(err, revocations.ErrPreflight) {
			t.Fatal("ignored job committed during admission", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("admission did not resume")
	}
	var count int
	if err = pool.QueryRow(ctx, `SELECT count(*) FROM vpn_credential_revocations`).Scan(&count); err != nil || count != 0 {
		t.Fatal("partial raced preparation", count, err)
	}
}
func TestCredentialRevocationDowngradePreservesHistory(t *testing.T) {
	ctx, pool, in, _, done := removalPreparationFixture(t)
	defer done()
	down, err := os.ReadFile("../../migrations/000160_credential_revocation_preparations.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	up, err := os.ReadFile("../../migrations/000160_credential_revocation_preparations.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, string(down)); err != nil {
		t.Fatal("empty-schema downgrade", err)
	}
	if _, err = pool.Exec(ctx, string(up)); err != nil {
		t.Fatal("reapply", err)
	}
	service := revocations.NewService(pool)
	p, err := service.Prepare(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, string(down)); err == nil {
		t.Fatal("discarded active preparation")
	}
	if _, err = service.Cancel(ctx, p.ID, in.Actor); err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, string(down)); err == nil {
		t.Fatal("discarded cancelled audit")
	}
	var count int
	if err = pool.QueryRow(ctx, `SELECT count(*) FROM vpn_credential_revocation_events WHERE operation_id=$1::uuid`, p.ID).Scan(&count); err != nil || count != 2 {
		t.Fatal("downgrade partially removed history", count, err)
	}
}
