package db

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
)

// Verify a real schema-163 possible-send claim survives the forward-only
// schema-164 trigger upgrade and an idempotent migration replay unchanged.
func TestRevocationLockOrderUpgrade163To164PreservesUnknownClaim(t *testing.T) {
	dsn := os.Getenv("ROUTEGATE_TEST_DATABASE_URL")
	if dsn == "" { t.Skip("ROUTEGATE_TEST_DATABASE_URL is not set") }
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	pool, err := Connect(ctx, dsn, log)
	if err != nil { t.Fatal(err) }
	defer pool.Close()
	resetPublicSchema(t, ctx, pool)
	prior := copyMigrationsBefore(t, "../../migrations", "000164_credential_revocation_dispatch_lock_order.up.sql")
	if err = Migrate(ctx, pool, prior, log); err != nil { t.Fatal("apply schema 163",err) }
	version, err := NewSchemaVersionRepository(pool).AppliedSchemaVersion(ctx)
	if err != nil || version != "000163_credential_revocation_dispatch_claims" { t.Fatalf("pre-upgrade schema=%s: %v",version,err) }

	var server, agent, account, baseline, operation, claimID string
	if err = pool.QueryRow(ctx, `INSERT INTO servers(name,status) VALUES('schema163-existing-claim','active') RETURNING id::text`).Scan(&server); err != nil { t.Fatal(err) }
	if err = pool.QueryRow(ctx, `INSERT INTO agents(server_id,agent_version,token_hash,status,credential_generation,last_authenticated_heartbeat_at,last_authenticated_heartbeat_generation)
		VALUES($1::uuid,'test-v163','token-v163','online',1,now(),1) RETURNING id::text`,server).Scan(&agent); err != nil { t.Fatal(err) }
	if err = pool.QueryRow(ctx, `INSERT INTO vpn_accounts(username,display_name,protocol,server_id,status)
		VALUES('claim-upgrade-v163','Existing claimed VPN user','sing-box',$1::uuid,'active') RETURNING id::text`,server).Scan(&account); err != nil { t.Fatal(err) }
	if err = pool.QueryRow(ctx, `INSERT INTO config_versions(server_id,version,config_hash,status,rendered_config)
		VALUES($1::uuid,1,$2,'applied','{}'::jsonb) RETURNING id::text`,server,strings.Repeat("a",64)).Scan(&baseline); err != nil { t.Fatal(err) }
	if _, err = pool.Exec(ctx, `UPDATE servers SET active_config_version_id=$2::uuid WHERE id=$1::uuid`,server,baseline); err != nil { t.Fatal(err) }
	const actor = "11111111-1111-4111-8111-111111111111"
	if err = pool.QueryRow(ctx, `INSERT INTO vpn_credential_revocations
		(vpn_account_id,server_id,agent_id,agent_credential_generation,baseline_version_id,
		 baseline_hash,baseline_runtime_hash,candidate_runtime_hash,candidate_hash,
		 candidate_config,account_status,requested_by,last_action_by)
		VALUES($1::uuid,$2::uuid,$3::uuid,1,$4::uuid,$5,$6,$7,$8,'{}'::jsonb,
		 'active',$9,$9) RETURNING id::text`,account,server,agent,baseline,
		 strings.Repeat("a",64),strings.Repeat("b",64),strings.Repeat("c",64),strings.Repeat("d",64),actor).Scan(&operation); err != nil { t.Fatal(err) }
	if _, err = pool.Exec(ctx, `INSERT INTO vpn_credential_revocation_confirmations
		(operation_id,confirmed_by,baseline_version_id,agent_id,agent_credential_generation,
		 candidate_hash,candidate_runtime_hash,shared_restart_acknowledged)
		SELECT id,requested_by,baseline_version_id,agent_id,agent_credential_generation,
		       candidate_hash,candidate_runtime_hash,true
		FROM vpn_credential_revocations WHERE id=$1::uuid`,operation); err != nil { t.Fatal(err) }
	if err = pool.QueryRow(ctx, `INSERT INTO vpn_credential_revocation_dispatch_claims
		(operation_id,claimed_by,server_id,agent_id,agent_credential_generation,
		 baseline_version_id,confirmation_at,candidate_hash,candidate_runtime_hash)
		SELECT p.id,p.requested_by,p.server_id,p.agent_id,p.agent_credential_generation,
		       p.baseline_version_id,c.confirmed_at,p.candidate_hash,p.candidate_runtime_hash
		FROM vpn_credential_revocations p JOIN vpn_credential_revocation_confirmations c ON c.operation_id=p.id
		WHERE p.id=$1::uuid RETURNING claim_id::text`,operation).Scan(&claimID); err != nil { t.Fatal(err) }

	var before string
	if err = pool.QueryRow(ctx, `SELECT row_to_json(c)::text FROM vpn_credential_revocation_dispatch_claims c WHERE claim_id=$1::uuid`,claimID).Scan(&before); err != nil { t.Fatal(err) }
	through164 := copyMigrationsBefore(t, "../../migrations", "000165_credential_revocation_task_identity.up.sql")
	for attempt:=0;attempt<2;attempt++ {
		if err = Migrate(ctx,pool,through164,log); err != nil { t.Fatalf("164 migration/replay: %v",err) }
		version, err = NewSchemaVersionRepository(pool).AppliedSchemaVersion(ctx)
		if err != nil || version != "000164_credential_revocation_dispatch_lock_order" { t.Fatalf("latest schema=%s: %v",version,err) }
		var after, state, name string
		var count, jobs int
		if err = pool.QueryRow(ctx, `SELECT row_to_json(c)::text FROM vpn_credential_revocation_dispatch_claims c WHERE claim_id=$1::uuid`,claimID).Scan(&after); err != nil { t.Fatal(err) }
		if err = pool.QueryRow(ctx, `SELECT p.state,a.display_name,
			(SELECT count(*) FROM vpn_credential_revocation_dispatch_claims WHERE operation_id=p.id),
			(SELECT count(*) FROM agent_operation_jobs WHERE server_id=p.server_id)
			FROM vpn_credential_revocations p JOIN vpn_accounts a ON a.id=p.vpn_account_id WHERE p.id=$1::uuid`,operation).
			Scan(&state,&name,&count,&jobs); err != nil { t.Fatal(err) }
		if before!=after || state!="prepared" || name!="Existing claimed VPN user" || count!=1 || jobs!=0 {
			t.Fatalf("upgraded claim changed: same=%v state=%s account=%s claims=%d jobs=%d",before==after,state,name,count,jobs)
		}
	}
	var functionSource string
	if err = pool.QueryRow(ctx, `SELECT pg_get_functiondef('routegate_revocation_dispatch_claim_guard()'::regprocedure)`).Scan(&functionSource); err != nil { t.Fatal(err) }
	if !strings.Contains(functionSource,"FOR KEY SHARE") { t.Fatal("missing schema-164 server-first SQL guard") }
	down, err := os.ReadFile("../../migrations/000164_credential_revocation_dispatch_lock_order.down.sql")
	if err != nil { t.Fatal(err) }
	_, err = pool.Exec(ctx,string(down))
	var pgErr *pgconn.PgError
	if !errors.As(err,&pgErr) || pgErr.Code!="P0141" { t.Fatalf("unsafe downgrade not blocked: %v",err) }
	// Next schema must not synthesize reserved task IDs for historical claims.
	if err = Migrate(ctx,pool,"../../migrations",log); err != nil { t.Fatal(err) }
	var reservations int
	if err = pool.QueryRow(ctx,"SELECT count(*) FROM vpn_credential_revocation_task_reservations").Scan(&reservations); err != nil || reservations != 0 {
		t.Fatalf("schema-165 upgrade fabricated task reservation: %d %v",reservations,err)
	}
}
