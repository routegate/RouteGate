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

// Real upgrade test: an already persisted 161 preparation must not be
// confirmed, cancelled, dispatched or rewritten by installing schema 162.
func TestCredentialRevocationUpgrade161To162KeepsUnconfirmedPreparation(t *testing.T) {
	dsn := os.Getenv("ROUTEGATE_TEST_DATABASE_URL")
	if dsn == "" { t.Skip("ROUTEGATE_TEST_DATABASE_URL is not set") }
	ctx,cancel := context.WithTimeout(context.Background(),90*time.Second)
	defer cancel()
	logger := slog.New(slog.NewTextHandler(io.Discard,nil))
	pool,err := Connect(ctx,dsn,logger)
	if err != nil {t.Fatal(err)}
	defer pool.Close()
	resetPublicSchema(t,ctx,pool)

	oldCatalog := copyMigrationsBefore(t,"../../migrations","000162_credential_revocation_confirmations.up.sql")
	if err = Migrate(ctx,pool,oldCatalog,logger);err != nil {t.Fatalf("install schema 161: %v",err)}
	var server,agent,account,baseline,op string
	if err=pool.QueryRow(ctx,`INSERT INTO servers(name,status) VALUES('confirmed-upgrade-existing-server','active') RETURNING id::text`).Scan(&server);err!=nil{t.Fatal(err)}
	if err=pool.QueryRow(ctx,`INSERT INTO agents(server_id,agent_version,token_hash,status,credential_generation)
 VALUES($1::uuid,'v0.2-review','upgrading-confirmation-fixture','online',1) RETURNING id::text`,server).Scan(&agent);err!=nil{t.Fatal(err)}
	if err=pool.QueryRow(ctx,`INSERT INTO vpn_accounts(username,display_name,protocol,server_id,status)
 VALUES('confirmation-upgrade-existing-account','Existing deployed user','sing-box',$1::uuid,'active') RETURNING id::text`,server).Scan(&account);err!=nil{t.Fatal(err)}
	if err=pool.QueryRow(ctx,`INSERT INTO config_versions(server_id,version,config_hash,status,rendered_config)
 VALUES($1::uuid,1,$2,'applied','{}'::jsonb) RETURNING id::text`,server,strings.Repeat("a",64)).Scan(&baseline);err!=nil{t.Fatal(err)}
	if err=pool.QueryRow(ctx,`INSERT INTO vpn_credential_revocations
 (vpn_account_id,server_id,agent_id,agent_credential_generation,baseline_version_id,
  baseline_hash,baseline_runtime_hash,candidate_runtime_hash,candidate_hash,
  candidate_config,account_status,requested_by,last_action_by)
 VALUES($1::uuid,$2::uuid,$3::uuid,1,$4::uuid,$5,$6,$7,$8,'{}'::jsonb,
 'active','11111111-1111-4111-8111-111111111111','11111111-1111-4111-8111-111111111111')
 RETURNING id::text`,account,server,agent,baseline,strings.Repeat("a",64),
 strings.Repeat("b",64),strings.Repeat("c",64),strings.Repeat("d",64)).Scan(&op);err!=nil{
  t.Fatalf("persist prior-version prepared operation: %v",err)
 }
	var before time.Time
	if err=pool.QueryRow(ctx,`SELECT created_at FROM vpn_credential_revocations WHERE id=$1::uuid`,op).Scan(&before);err!=nil{t.Fatal(err)}

	for n:=1;n<=2;n++ {
		if err=Migrate(ctx,pool,"../../migrations",logger);err!=nil{t.Fatalf("upgrade/retry %d: %v",n,err)}
		var state,name string
		var after time.Time
		var confirmed,agentJobs, migrationCount int
		if err=pool.QueryRow(ctx,`SELECT state,created_at FROM vpn_credential_revocations WHERE id=$1::uuid`,op).Scan(&state,&after);err!=nil{t.Fatal(err)}
		if state!="prepared" || !after.Equal(before) {t.Fatalf("161 preparation was changed: state=%q created=%v",state,after)}
		if err=pool.QueryRow(ctx,`SELECT display_name FROM vpn_accounts WHERE id=$1::uuid`,account).Scan(&name);err!=nil||name!="Existing deployed user" {t.Fatalf("existing VPN account changed: %q %v",name,err)}
		if err=pool.QueryRow(ctx,`SELECT (SELECT count(*) FROM vpn_credential_revocation_confirmations),
 (SELECT count(*) FROM agent_operation_jobs WHERE server_id=$1::uuid),
 (SELECT count(*) FROM schema_migrations WHERE version IN ('000161_credential_revocation_preparations','000162_credential_revocation_confirmations'))`,server).Scan(&confirmed,&agentJobs,&migrationCount);err!=nil{t.Fatal(err)}
		if confirmed!=0||agentJobs!=0||migrationCount!=2{t.Fatalf("upgrade unexpectedly confirmed/dispatched: confirmation=%d jobs=%d schemaIDs=%d",confirmed,agentJobs,migrationCount)}
	}
}
