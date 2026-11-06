package db

import (
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/ikaevus/routegate/backend/internal/configs"
	"github.com/jackc/pgx/v5/pgconn"
)

func TestRetiredVLESSCredential(t *testing.T) {
	f := newDispatchGateFixture(t)
	runtime, _ := runtimeRoleFixture(t, f.ctx, f.pool)
	const retired = "01234567-89ab-4def-8123-456789abcdef"
	const insertConfig = `INSERT INTO config_versions(server_id,version,config_hash,status,rendered_config)
VALUES($1::uuid,$2,'retired-vless-fixture','validated',$3::jsonb) RETURNING id::text`
	const queue = `INSERT INTO config_apply_jobs(server_id,config_version_id,action,status)
VALUES($1::uuid,$2::uuid,$3,$4) RETURNING id::text`
	const promote = `UPDATE servers SET active_config_version_id=$2::uuid WHERE id=$1::uuid`
	const retire = `INSERT INTO routegate_dispatch.retired_vless_credentials(digest,operation_id)
VALUES(sha256(uuid_send($1::uuid)),$2::uuid)`
	checkBlocked := func(err error, message string) {
		t.Helper()
		var pgerr *pgconn.PgError
		if !errors.As(err, &pgerr) || pgerr.Code != "P0141" || pgerr.Message != message {
			t.Fatalf("expected retirement guard %q, got %v", message, err)
		}
	}
	const stillUsable = "VLESS credential remains in an account, active config, or open job"
	const rejected = "retired VLESS credential cannot be used"

	// The target is unreserved. Render through the normal runtime role to
	// preserve the exact format and hash that Apply and Reapply validate.
	const createAccount = `INSERT INTO vpn_accounts(username,protocol,display_name,status,server_id,vless_uuid)
VALUES('retired-vless-fixture','sing-box','Retirement fixture','active',$1::uuid,$2::uuid)`
	var account, job string
	if err := runtime.QueryRow(f.ctx, createAccount+" RETURNING id::text", f.target, retired).Scan(&account); err != nil {
		t.Fatal(err)
	}
	svc := configs.NewService(configs.NewRepository(runtime))
	stale := renderVersion(t, f.ctx, svc, f.target)
	_, err := f.pool.Exec(f.ctx, retire, retired, f.operation)
	checkBlocked(err, stillUsable)
	if _, err = runtime.Exec(f.ctx, `DELETE FROM vpn_accounts WHERE id=$1::uuid`, account); err != nil {
		t.Fatal(err)
	}
	if _, err = runtime.Exec(f.ctx, promote, f.target, stale); err != nil {
		t.Fatal(err)
	}
	_, err = f.pool.Exec(f.ctx, retire, retired, f.operation)
	checkBlocked(err, stillUsable)
	if _, err = runtime.Exec(f.ctx, promote, f.target, nil); err != nil {
		t.Fatal(err)
	}
	if err = runtime.QueryRow(f.ctx, queue, f.target, stale, "apply", "pending").Scan(&job); err != nil {
		t.Fatal(err)
	}
	_, err = f.pool.Exec(f.ctx, retire, retired, f.operation)
	checkBlocked(err, stillUsable)
	if _, err = runtime.Exec(f.ctx, `UPDATE config_apply_jobs SET status='failed' WHERE id=$1::uuid`, job); err != nil {
		t.Fatal(err)
	}
	if _, err = runtime.Exec(f.ctx, retire, retired, f.operation); err == nil {
		t.Fatal("ordinary runtime role wrote retirement history")
	}

	// While a retirement is uncommitted, a competing apply must wait and then
	// check the newly committed registry rather than its original snapshot.
	tx, err := f.pool.Begin(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(f.ctx)
	if _, err = tx.Exec(f.ctx, retire, retired, f.operation); err != nil {
		t.Fatal(err)
	}
	conn, err := runtime.Acquire(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Release()
	waiting := make(chan error, 1)
	pid := conn.Conn().PgConn().PID()
	go func() {
		_, err := conn.Exec(f.ctx, queue, f.target, stale, "apply", "pending")
		waiting <- err
	}()
	var blocked bool
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline) && !blocked; {
		if err = f.pool.QueryRow(f.ctx, `SELECT cardinality(pg_blocking_pids($1)) > 0`, int32(pid)).Scan(&blocked); err != nil {
			t.Fatal(err)
		}
		if !blocked {
			time.Sleep(10 * time.Millisecond)
		}
	}
	if err = tx.Commit(f.ctx); err != nil {
		t.Fatal(err)
	}
	checkBlocked(<-waiting, rejected)
	if !blocked {
		t.Fatal("competing apply was not observed waiting for retirement")
	}

	_, err = svc.Apply(f.ctx, f.target, stale, configs.ApplyConfigRequest{})
	checkBlocked(err, rejected)
	if _, err = runtime.Exec(f.ctx, `UPDATE config_versions SET applied_at=now() WHERE id=$1::uuid`, stale); err != nil {
		t.Fatal(err)
	}
	_, err = svc.Reapply(f.ctx, f.target, stale, configs.ApplyConfigRequest{})
	checkBlocked(err, rejected)
	for _, action := range []string{"apply", "rollback"} {
		_, err = runtime.Exec(f.ctx, queue, f.target, stale, action, "pending")
		checkBlocked(err, rejected)
	}
	_, err = runtime.Exec(f.ctx, `UPDATE config_apply_jobs SET status='in_progress' WHERE id=$1::uuid`, job)
	checkBlocked(err, rejected)
	_, err = runtime.Exec(f.ctx, promote, f.target, stale)
	checkBlocked(err, rejected)
	_, err = runtime.Exec(f.ctx, createAccount, f.target, retired)
	checkBlocked(err, rejected)

	body := `{"schemaVersion":"routegate/v1","singBox":{"inbounds":[{"type":"vless","users":[{"uuid":"` + retired + `"}]}]}}`
	for _, candidate := range []string{
		body,
		`{"vpnAccounts":[{"vlessUuid":"` + retired + `"}]}`,
		`{"Inbounds":[{"type":"vless","Users":[{"UUID":"` + strings.ToUpper(strings.ReplaceAll(retired, "-", "")) + `"}]}]}`,
	} {
		_, err = runtime.Exec(f.ctx, insertConfig, f.target, 2, candidate)
		checkBlocked(err, rejected)
	}

	var stored, clean string
	if err = runtime.QueryRow(f.ctx, `SELECT rendered_config::text FROM config_versions WHERE id=$1::uuid`, stale).Scan(&stored); err != nil || !strings.Contains(stored, retired) {
		t.Fatal("historical config is no longer readable", err)
	}
	cleanBody := strings.ReplaceAll(body, retired, "23456789-abcd-4ef0-8123-456789abcdef")
	if err = runtime.QueryRow(f.ctx, insertConfig, f.target, 2, cleanBody).Scan(&clean); err != nil {
		t.Fatal(err)
	}
	if _, err = runtime.Exec(f.ctx, queue, f.target, clean, "apply", "pending"); err != nil {
		t.Fatal("unrelated credential was blocked", err)
	}

	down, err := os.ReadFile("../../migrations/000178_retired_vless_credentials.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.pool.Exec(f.ctx, string(down))
	checkBlocked(err, "cannot remove retained VLESS retirement history")
}
