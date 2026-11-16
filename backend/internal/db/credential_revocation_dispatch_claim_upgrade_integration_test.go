package db

import (
	"context"
	"io"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"
)

// This is an upgrade of a database that already contains a *confirmed*
// but never dispatched schema-162 operation. Schema 163 must NOT create
// a claim on behalf of any past user, change the historic confirmation,
// alter the existing account or dispatch any Agent job.
func TestCredentialRevocationUpgrade162To163DoesNotClaimExistingConfirmation(t *testing.T) {
	dsn:=os.Getenv("ROUTEGATE_TEST_DATABASE_URL")
	if dsn==""{t.Skip("ROUTEGATE_TEST_DATABASE_URL is not set")}
	ctx,cancel:=context.WithTimeout(context.Background(),100*time.Second)
	defer cancel()
	log:=slog.New(slog.NewTextHandler(io.Discard,nil))
	pool,err:=Connect(ctx,dsn,log)
	if err!=nil{t.Fatal(err)}
	defer pool.Close()
	resetPublicSchema(t,ctx,pool)
	prior:=copyMigrationsBefore(t,"../../migrations","000163_credential_revocation_dispatch_claims.up.sql")
	if err=Migrate(ctx,pool,prior,log);err!=nil{t.Fatalf("apply schema 162: %v",err)}
	var server,agent,account,baseline,op string
	if err=pool.QueryRow(ctx,`INSERT INTO servers(name,status) VALUES('existing-schema162-node','active') RETURNING id::text`).Scan(&server);err!=nil{t.Fatal(err)}
	if err=pool.QueryRow(ctx,`INSERT INTO agents(server_id,agent_version,token_hash,status,credential_generation)
 VALUES($1::uuid,'test-v162','v162-token','online',1) RETURNING id::text`,server).Scan(&agent);err!=nil{t.Fatal(err)}
	if err=pool.QueryRow(ctx,`INSERT INTO vpn_accounts(username,display_name,protocol,server_id,status)
 VALUES('existing-schema162-account','Original VPN account','sing-box',$1::uuid,'active') RETURNING id::text`,server).Scan(&account);err!=nil{t.Fatal(err)}
	if err=pool.QueryRow(ctx,`INSERT INTO config_versions(server_id,version,config_hash,status,rendered_config)
 VALUES($1::uuid,1,$2,'applied','{}'::jsonb) RETURNING id::text`,server,strings.Repeat("a",64)).Scan(&baseline);err!=nil{t.Fatal(err)}
	const actor="11111111-1111-4111-8111-111111111111"
	if err=pool.QueryRow(ctx,`INSERT INTO vpn_credential_revocations
 (vpn_account_id,server_id,agent_id,agent_credential_generation,baseline_version_id,
 baseline_hash,baseline_runtime_hash,candidate_runtime_hash,candidate_hash,
 candidate_config,account_status,requested_by,last_action_by)
 VALUES($1::uuid,$2::uuid,$3::uuid,1,$4::uuid,$5,$6,$7,$8,'{}'::jsonb,
 'active',$9,$9) RETURNING id::text`,
 account,server,agent,baseline,strings.Repeat("a",64),
 strings.Repeat("b",64),strings.Repeat("c",64),strings.Repeat("d",64),actor).Scan(&op);err!=nil{
  t.Fatalf("prepare original schema-162 row: %v",err)
 }
 var confirmationAt time.Time
 if err=pool.QueryRow(ctx,`INSERT INTO vpn_credential_revocation_confirmations
 (operation_id,confirmed_by,baseline_version_id,agent_id,agent_credential_generation,
 candidate_hash,candidate_runtime_hash,shared_restart_acknowledged)
 SELECT id,requested_by,baseline_version_id,agent_id,agent_credential_generation,
 candidate_hash,candidate_runtime_hash,true FROM vpn_credential_revocations
 WHERE id=$1::uuid RETURNING confirmed_at`,op).Scan(&confirmationAt);err!=nil{
  t.Fatalf("confirm existing schema-162 row: %v",err)
 }
 for run:=0;run<2;run++{
  if err=Migrate(ctx,pool,"../../migrations",log);err!=nil{t.Fatalf("migrate/retry %d: %v",run,err)}
  var state,display string
  var recorded time.Time
  var confirmations,claims,jobs,versions int
  if err=pool.QueryRow(ctx,`SELECT state FROM vpn_credential_revocations WHERE id=$1::uuid`,op).Scan(&state);err!=nil{t.Fatal(err)}
  if err=pool.QueryRow(ctx,`SELECT confirmed_at FROM vpn_credential_revocation_confirmations WHERE operation_id=$1::uuid`,op).Scan(&recorded);err!=nil{t.Fatal(err)}
  if err=pool.QueryRow(ctx,`SELECT display_name FROM vpn_accounts WHERE id=$1::uuid`,account).Scan(&display);err!=nil{t.Fatal(err)}
  if err=pool.QueryRow(ctx,`
   SELECT
    (SELECT count(*) FROM vpn_credential_revocation_confirmations),
    (SELECT count(*) FROM vpn_credential_revocation_dispatch_claims),
    (SELECT count(*) FROM agent_operation_jobs WHERE server_id=$1::uuid),
    (SELECT count(*) FROM schema_migrations WHERE version IN
      ('000161_credential_revocation_preparations','000162_credential_revocation_confirmations','000163_credential_revocation_dispatch_claims'))
  `,server).Scan(&confirmations,&claims,&jobs,&versions);err!=nil{t.Fatal(err)}
  if state!="prepared"||!recorded.Equal(confirmationAt)||display!="Original VPN account"||
    confirmations!=1||claims!=0||jobs!=0||versions!=3{
   t.Fatalf("upgrade changed existing data: state=%s time=%s display=%s confirmations=%d claims=%d jobs=%d versions=%d",
    state,recorded,display,confirmations,claims,jobs,versions)
  }
 }
}
