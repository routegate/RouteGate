package db

import (
 "context"
 "errors"
 "os"
 "sync"
 "testing"
 "time"

 "github.com/jackc/pgx/v5/pgxpool"

 "github.com/ikaevus/routegate/backend/internal/revocations"
 "github.com/jackc/pgx/v5/pgconn"
)

func TestCredentialRemovalDispatchStageAtomic(t *testing.T) {
 f:=newDispatchGateFixture(t)
 var originalVersions,originalJobs,originalTokens,originalNext int
 if err:=f.pool.QueryRow(f.ctx,`SELECT
 (SELECT count(*) FROM config_versions),
 (SELECT count(*) FROM config_apply_jobs),
 (SELECT count(*) FROM vpn_subscription_tokens),
 s.next_config_version
 FROM servers s JOIN vpn_credential_revocations r ON r.server_id=s.id
 WHERE r.id=$1::uuid`,f.operation).Scan(
 &originalVersions,&originalJobs,&originalTokens,&originalNext);err!=nil{t.Fatal(err)}
 // A raw SQL writer with a substituted job ID cannot construct the manifest
 // from a still-live admin approval; the SQL trigger checks exact bindings.
 _,err:=f.pool.Exec(f.ctx,`INSERT INTO vpn_credential_removal_dispatch_stages
  (operation_id,candidate_version_id,apply_job_id,agent_id,
   agent_credential_generation,candidate_hash,candidate_runtime_hash,
   actor_user_id,actor_session_id)
 SELECT a.operation_id,a.candidate_version_id,gen_random_uuid(),r.agent_id,
   a.agent_credential_generation,a.candidate_hash,a.candidate_runtime_hash,
   a.actor_user_id,a.actor_session_id
 FROM vpn_credential_removal_dispatch_authorizations a
 JOIN vpn_credential_revocations r ON r.id=a.operation_id
 WHERE a.operation_id=$1::uuid`,f.operation)
 var pgerr *pgconn.PgError
 if !errors.As(err,&pgerr)||pgerr.Code!="P0154"{t.Fatal("substituted SQL job ID admitted",err)}
 if _,err=f.svc.StagePreparedCredentialRemoval(f.ctx,f.operation,f.nonce,false);!errors.Is(err,revocations.ErrPreflight){
  t.Fatal("missing restart consent staged a dispatch",err)
 }
 if _,err=f.svc.StagePreparedCredentialRemoval(f.ctx,f.operation,revocations.ErrCredentialRemoval.Error(),true);!errors.Is(err,revocations.ErrPreflight){
  t.Fatal("bad approval nonce staged a dispatch",err)
 }
 if _,err=f.svc.StagePreparedCredentialRemoval(context.Background(),f.operation,f.nonce,true);!errors.Is(err,revocations.ErrPreflight){
  t.Fatal("unauthenticated caller staged a dispatch",err)
 }
 var wg sync.WaitGroup
 outputs:=make(chan struct{stage revocations.StagedCredentialRemoval;err error},2)
 for i:=0;i<2;i++{
  wg.Add(1)
  go func(){
   defer wg.Done()
   s,e:=f.svc.StagePreparedCredentialRemoval(f.ctx,f.operation,f.nonce,true)
   outputs<-struct{stage revocations.StagedCredentialRemoval;err error}{s,e}
  }()
 }
 wg.Wait()
 a,b:=<-outputs,<-outputs
 if a.err!=nil||b.err!=nil||a.stage!=b.stage ||
  a.stage.OperationID!=f.operation ||
  a.stage.ApplyJobID==a.stage.CandidateVersionID ||
  a.stage.StagedAt.IsZero() || a.stage.AgentGeneration<1{
  t.Fatal("atomic dispatch staging lacked immutable idempotence",a,b)
 }
 var rowCount,versions,jobs,tokens,next int
 if err=f.pool.QueryRow(f.ctx,`SELECT
  (SELECT count(*) FROM vpn_credential_removal_dispatch_stages),
  (SELECT count(*) FROM config_versions),
  (SELECT count(*) FROM config_apply_jobs),
  (SELECT count(*) FROM vpn_subscription_tokens),
  s.next_config_version
 FROM servers s JOIN vpn_credential_revocations r ON r.server_id=s.id
 WHERE r.id=$1::uuid`,f.operation).Scan(
 &rowCount,&versions,&jobs,&tokens,&next);err!=nil ||
 rowCount!=1 || versions!=originalVersions || jobs!=originalJobs ||
 tokens!=originalTokens || next!=originalNext {
  t.Fatal("staged intent created executable records or rotated credentials",err)
 }
 for _,query:=range []string{
  `UPDATE vpn_credential_removal_dispatch_stages
    SET apply_job_id=gen_random_uuid()`,
  `DELETE FROM vpn_credential_removal_dispatch_stages`,
 } {
  if _,err=f.pool.Exec(f.ctx,query);err==nil{t.Fatal("stage history changed",query)}
  if !errors.As(err,&pgerr)||pgerr.Code!="P0154"{t.Fatal("stage guard did not preserve history",err)}
 }
 // Ordinary Apply stays forbidden, even when a human-approved exact staging
 // record already exists in this separate, non-executable ledger.
 if _,err=f.pool.Exec(f.ctx,`INSERT INTO config_apply_jobs
 (server_id,agent_id,config_version_id,action,status)
 SELECT r.server_id,r.agent_id,r.baseline_version_id,'apply','pending'
 FROM vpn_credential_revocations r WHERE r.id=$1::uuid`,f.operation);err==nil{
  t.Fatal("staging bypassed original Apply fence")
 }
 if !errors.As(err,&pgerr)||pgerr.Code!="P0141"{t.Fatal("ordinary Apply fence changed",err)}
 if _,err=f.svc.Cancel(f.ctx,f.operation,"isolated-operator");err!=nil{t.Fatal(err)}
 if _,err=f.svc.StagePreparedCredentialRemoval(f.ctx,f.operation,f.nonce,true);!errors.Is(err,revocations.ErrPreflight){
  t.Fatal("cancelled operation renewed a stage",err)
 }
 if err=f.pool.QueryRow(f.ctx,`SELECT count(*) FROM vpn_credential_removal_dispatch_stages`).Scan(&rowCount);err!=nil||rowCount!=1{
  t.Fatal("cancellation erased immutable stage history",err)
 }
 down,err:=os.ReadFile("../../migrations/000170_credential_removal_dispatch_stages.down.sql")
 if err!=nil{t.Fatal(err)}
 if _,err=f.pool.Exec(f.ctx,string(down));err==nil{t.Fatal("downgrade destroyed retained stage history")}
}

func TestCredentialRemovalDispatchStageInvalidation(t *testing.T) {
 for _,mode:=range []string{"revoked_session","offline_agent","stale_heartbeat","cancelled_preparation"}{
  t.Run(mode,func(t *testing.T){
   f:=newDispatchGateFixture(t)
   var err error
   switch mode{
   case "revoked_session":
    _,err=f.pool.Exec(f.ctx,`UPDATE auth_sessions SET revoked_at=clock_timestamp()
      WHERE id=$1::uuid`,f.session)
   case "offline_agent":
    _,err=f.pool.Exec(f.ctx,`UPDATE agents SET status='offline'
      WHERE id=$1::uuid`,f.agent)
   case "stale_heartbeat":
    _,err=f.pool.Exec(f.ctx,`UPDATE agents SET last_authenticated_heartbeat_at=
      clock_timestamp()-interval '2 minutes' WHERE id=$1::uuid`,f.agent)
   case "cancelled_preparation":
    _,err=f.svc.Cancel(f.ctx,f.operation,"isolated-operator")
   }
   if err!=nil{t.Fatal(err)}
   if stage,e:=f.svc.StagePreparedCredentialRemoval(f.ctx,f.operation,f.nonce,true);!errors.Is(e,revocations.ErrPreflight)||
    stage!=(revocations.StagedCredentialRemoval{}){
    t.Fatal("stale authority generated dormant dispatch intent",mode,e)
   }
   var count int
   if err=f.pool.QueryRow(f.ctx,`SELECT count(*) FROM vpn_credential_removal_dispatch_stages`).Scan(&count);
    err!=nil||count!=0{t.Fatal("invalid authorization wrote stage",err)}
  })
 }
}

// Stage admission is re-checked AFTER waiting for the original server lock.
// A previously correct nonce must not be able to stage an intent once its
// owner session or original Agent has been invalidated concurrently.
func TestCredentialRemovalDispatchStageLockedInvalidation(t *testing.T) {
 for _,mode:=range []string{"human_session_revoked","agent_offline"}{
  t.Run(mode,func(t *testing.T){
   f:=newDispatchGateFixture(t)
   ctx,cancel:=context.WithTimeout(f.ctx,12*time.Second)
   defer cancel()
   var node string
   if err:=f.pool.QueryRow(ctx,`SELECT server_id::text FROM vpn_credential_revocations
    WHERE id=$1::uuid`,f.operation).Scan(&node);err!=nil{t.Fatal(err)}
   blocker,err:=f.pool.Begin(ctx)
   if err!=nil{t.Fatal(err)}
   defer blocker.Rollback(ctx)
   if _,err=blocker.Exec(ctx,`SELECT id FROM servers WHERE id=$1::uuid FOR UPDATE`,node);err!=nil{t.Fatal(err)}
   cfg,err:=pgxpool.ParseConfig(os.Getenv("ROUTEGATE_TEST_DATABASE_URL"))
   if err!=nil{t.Fatal(err)}
   if cfg.ConnConfig.RuntimeParams==nil {cfg.ConnConfig.RuntimeParams=map[string]string{}}
   cfg.ConnConfig.RuntimeParams["application_name"]="rg140-removal-stage-lock-wait"
   contender,err:=pgxpool.NewWithConfig(ctx,cfg)
   if err!=nil{t.Fatal(err)}
   defer contender.Close()
   results:=make(chan error,1)
   go func(){
    _,e:=revocations.NewService(contender).StagePreparedCredentialRemoval(ctx,f.operation,f.nonce,true)
    results<-e
   }()
   waiting:=false
   deadline:=time.Now().Add(5*time.Second)
   for time.Now().Before(deadline){
    var blocked bool
    if err=f.pool.QueryRow(ctx,`SELECT EXISTS(SELECT 1 FROM pg_stat_activity
      WHERE application_name='rg140-removal-stage-lock-wait'
       AND wait_event_type='Lock')`).Scan(&blocked);err!=nil{t.Fatal(err)}
    if blocked{waiting=true;break}
    select{
    case e:=<-results:t.Fatalf("stage returned before server lock wait: %v",e)
    case <-time.After(10*time.Millisecond):
    }
   }
   if !waiting{t.Fatal("staging never waited for original server lock")}
   switch mode{
   case "human_session_revoked":
    _,err=blocker.Exec(ctx,`UPDATE auth_sessions SET revoked_at=clock_timestamp()
     WHERE id=$1::uuid`,f.session)
   case "agent_offline":
    _,err=blocker.Exec(ctx,`UPDATE agents SET status='offline'
     WHERE id=$1::uuid`,f.agent)
   }
   if err!=nil{t.Fatal(err)}
   if err=blocker.Commit(ctx);err!=nil{t.Fatal(err)}
   select{
   case e:=<-results:
    if !errors.Is(e,revocations.ErrPreflight){
     t.Fatal("stale approved staging succeeded after node-lock wait",mode,e)
    }
   case <-ctx.Done():t.Fatal("staging did not return after invalidation",ctx.Err())
   }
   var count int
   if err=f.pool.QueryRow(ctx,`SELECT count(*) FROM vpn_credential_removal_dispatch_stages`).Scan(&count);
    err!=nil||count!=0{
    t.Fatal("staging inserted a record after original authority invalidation",err,count)
   }
  })
 }
}


// The SQL trigger is an independent enforcement boundary: a direct DB writer
// must not convert a historical approval into an executable-looking stage
// after the human super_admin grant has been revoked.
func TestCredentialRemovalDispatchStageDirectSQLRevokedAuthority(t *testing.T) {
 f:=newDispatchGateFixture(t)
 if _,err:=f.pool.Exec(f.ctx,`DELETE FROM user_roles
  WHERE user_id=(SELECT user_id FROM auth_sessions WHERE id=$1::uuid)
  AND role_id=(SELECT id FROM roles WHERE code='super_admin')`,f.session);err!=nil{
  t.Fatal("cannot revoke original human grant",err)
 }
 _,err:=f.pool.Exec(f.ctx,`INSERT INTO vpn_credential_removal_dispatch_stages
  (operation_id,candidate_version_id,apply_job_id,agent_id,
   agent_credential_generation,candidate_hash,candidate_runtime_hash,
   actor_user_id,actor_session_id)
  SELECT a.operation_id,a.candidate_version_id,a.apply_job_id,r.agent_id,
   a.agent_credential_generation,a.candidate_hash,a.candidate_runtime_hash,
   a.actor_user_id,a.actor_session_id
  FROM vpn_credential_removal_dispatch_authorizations a
  JOIN vpn_credential_revocations r ON r.id=a.operation_id
  WHERE a.operation_id=$1::uuid`,f.operation)
 var sqlerr *pgconn.PgError
 if !errors.As(err,&sqlerr)||sqlerr.Code!="P0154"{
  t.Fatal("direct SQL staged revocation after operator role withdrawal",err)
 }
 if stage,e:=f.svc.StagePreparedCredentialRemoval(f.ctx,f.operation,f.nonce,true);
  !errors.Is(e,revocations.ErrPreflight)||stage!=(revocations.StagedCredentialRemoval{}){
  t.Fatal("stale claimed browser role bypassed DB-backed reauthorization",e)
 }
 var n int
 if err=f.pool.QueryRow(f.ctx,`SELECT count(*) FROM vpn_credential_removal_dispatch_stages`).Scan(&n);err!=nil||n!=0{
  t.Fatal("revoked human authority left a staged intent",err,n)
 }
}
