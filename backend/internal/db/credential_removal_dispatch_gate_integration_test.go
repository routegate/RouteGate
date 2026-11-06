package db

import (
 "context"
 "errors"
 "strings"
 "testing"
 "os"
 "time"

 "github.com/ikaevus/routegate/backend/internal/auth"
 "github.com/ikaevus/routegate/backend/internal/revocations"
 "github.com/jackc/pgx/v5/pgxpool"
)

type dispatchGateFixture struct {
 ctx context.Context
 pool *pgxpool.Pool
 svc *revocations.Service
 operation string
 nonce string
 agent string
 session string
 target string
 done func()
}

func newDispatchGateFixture(t *testing.T) dispatchGateFixture {
 return newDispatchGateFixtureWithToken(t, "")
}

func newDispatchGateFixtureWithToken(t *testing.T, tokenHash string) dispatchGateFixture {
 return newDispatchGateFixtureWithSourceSetup(t, tokenHash, nil)
}

func newDispatchGateFixtureWithSourceSetup(t *testing.T, tokenHash string, setup func(context.Context, *pgxpool.Pool, string)) dispatchGateFixture {
 t.Helper()
 ctx, pool, in, target, done := removalPreparationFixtureWithSourceSetup(t, setup)
 t.Cleanup(done)
 if tokenHash != "" {
  if _, err := pool.Exec(ctx, "UPDATE agents SET token_hash=$2 WHERE server_id=$1::uuid", in.ServerID, tokenHash); err != nil { t.Fatal(err) }
 }
 svc := revocations.NewService(pool)
 prep, err := svc.Prepare(ctx, in)
 if err != nil { t.Fatal(err) }
 if _, err := svc.ReservePreparedRemovalJobIDs(ctx, prep.ID); err != nil { t.Fatal(err) }

 u := auth.AuthenticatedUser{UserProfile:auth.UserProfile{
  UserType:"human",Status:"active",Roles:[]string{"admin"},Permissions:[]string{"configs:apply"},
 }}
 if err := pool.QueryRow(ctx,`INSERT INTO users(email,display_name,user_type,status)
 VALUES('gate-admin@isolated.invalid','Gate administrator','human','active')
 RETURNING id::text`).Scan(&u.ID); err != nil { t.Fatal(err) }
 if err := pool.QueryRow(ctx,`INSERT INTO auth_sessions(user_id,token_hash,expires_at)
 VALUES($1::uuid,'isolated-dispatch-gate-token',clock_timestamp()+interval '1 hour')
 RETURNING id::text`,u.ID).Scan(&u.SessionID); err != nil { t.Fatal(err) }
 if _, err := pool.Exec(ctx,`INSERT INTO roles(code,name) VALUES('admin','Admin') ON CONFLICT(code) DO NOTHING;
 INSERT INTO permissions(code,name) VALUES('configs:apply','Apply config') ON CONFLICT(code) DO NOTHING`); err != nil { t.Fatal(err) }
 if _, err := pool.Exec(ctx,`INSERT INTO user_roles(user_id,role_id)
 SELECT $1::uuid,id FROM roles WHERE code='admin'`,u.ID); err != nil { t.Fatal(err) }
 if _, err := pool.Exec(ctx,`INSERT INTO role_permissions(role_id,permission_id)
 SELECT r.id,p.id FROM roles r,permissions p
 WHERE r.code='admin' AND p.code='configs:apply' ON CONFLICT DO NOTHING`); err != nil { t.Fatal(err) }
 ctx = auth.ContextWithUser(ctx,u)
 approval, err := svc.IssuePreparedRemovalDispatchApproval(ctx,
  revocations.PreparedRemovalDispatchApprovalInput{OperationID:prep.ID,ConfirmSharedProcessRestart:true})
 if err != nil { t.Fatal(err) }
 return dispatchGateFixture{ctx:ctx,pool:pool,svc:svc,operation:prep.ID,
  nonce:approval.Nonce,agent:prep.AgentID,session:u.SessionID,target:target,done:done}
}

func TestCredentialRemovalDispatchAdmissionGate(t *testing.T) {
 f := newDispatchGateFixture(t)
 check := func(ctx context.Context,op,nonce string,yes bool) error {
  return f.svc.CheckPreparedRemovalDispatchAdmission(ctx,op,nonce,yes)
 }
 var jobsBefore,versionsBefore,authBefore int
 if err := f.pool.QueryRow(f.ctx,`SELECT
 (SELECT count(*) FROM config_apply_jobs),
 (SELECT count(*) FROM config_versions),
 (SELECT count(*) FROM vpn_credential_removal_dispatch_authorizations)`).Scan(
 &jobsBefore,&versionsBefore,&authBefore); err != nil { t.Fatal(err) }

 if err := check(f.ctx,f.operation,f.nonce,true); err != nil { t.Fatal("authorized gate refused",err) }
 if err := check(f.ctx,f.operation,f.nonce,true); err != nil { t.Fatal("idempotent gate refused",err) }

 for name, bad := range map[string]struct{ctx context.Context; op,nonce string; yes bool}{
  "missing_context":{context.Background(),f.operation,f.nonce,true},
  "no_explicit_restart_consent":{f.ctx,f.operation,f.nonce,false},
  "other_operation":{f.ctx,"11111111-1111-4111-8111-111111111111",f.nonce,true},
  "short_nonce":{f.ctx,f.operation,"wrong",true},
  "forged_nonce":{f.ctx,f.operation,strings.Repeat("f",64),true},
 } {
  t.Run(name,func(t *testing.T){
   if err := check(bad.ctx,bad.op,bad.nonce,bad.yes); !errors.Is(err,revocations.ErrPreflight) {
    t.Fatal("invalid dispatch admission passed",err)
   }
  })
 }
 var jobsAfter,versionsAfter,authAfter int
 if err := f.pool.QueryRow(f.ctx,`SELECT
 (SELECT count(*) FROM config_apply_jobs),
 (SELECT count(*) FROM config_versions),
 (SELECT count(*) FROM vpn_credential_removal_dispatch_authorizations)`).Scan(
 &jobsAfter,&versionsAfter,&authAfter); err != nil ||
 jobsAfter!=jobsBefore || versionsAfter!=versionsBefore || authAfter!=authBefore {
  t.Fatal("read-only dispatch gate created a job, version, or authorization",err)
 }
}

func TestCredentialRemovalDispatchAdmissionInvalidated(t *testing.T) {
 for _,mode := range []string{"revoked_human_session","offline_agent","expired_heartbeat","cancelled_preparation"} {
  t.Run(mode,func(t *testing.T){
   f := newDispatchGateFixture(t)
   var err error
   switch mode {
   case "revoked_human_session":
    _,err=f.pool.Exec(f.ctx,`UPDATE auth_sessions SET revoked_at=clock_timestamp() WHERE id=$1::uuid`,f.session)
   case "offline_agent":
    _,err=f.pool.Exec(f.ctx,`UPDATE agents SET status='offline' WHERE id=$1::uuid`,f.agent)
   case "expired_heartbeat":
    _,err=f.pool.Exec(f.ctx,`UPDATE agents SET last_authenticated_heartbeat_at=clock_timestamp()-interval '2 minutes' WHERE id=$1::uuid`,f.agent)
   case "cancelled_preparation":
    _,err=f.svc.Cancel(f.ctx,f.operation,"test-operator")
   }
   if err != nil { t.Fatal(err) }
   if e:=f.svc.CheckPreparedRemovalDispatchAdmission(f.ctx,f.operation,f.nonce,true); !errors.Is(e,revocations.ErrPreflight){
    t.Fatal("stale security authority admitted",mode,e)
   }
   var version,job int
   if e:=f.pool.QueryRow(f.ctx,`SELECT
   (SELECT count(*) FROM config_versions WHERE id IN (
      SELECT candidate_version_id FROM vpn_credential_revocation_dispatch_bindings
   )),
   (SELECT count(*) FROM config_apply_jobs WHERE id IN (
      SELECT apply_job_id FROM vpn_credential_revocation_dispatch_bindings
   ))`).Scan(&version,&job);e!=nil || version!=0 || job!=0{
    t.Fatal("invalidated approval created executable mutation",e)
   }
  })
 }
}

// Original Agent state and human authority may change while a dispatch
// attempt waits for the server lock. Both must be RE-READ after the wait,
// without allocating either proposed executable identifier.
func TestCredentialRemovalDispatchAdmissionAuthorityInvalidatedDuringLockWait(t *testing.T) {
 for _,mode:=range []string{"human_session_revoked","agent_offline"}{
  t.Run(mode,func(t *testing.T){
   f:=newDispatchGateFixture(t)
   ctx,cancel:=context.WithTimeout(f.ctx,12*time.Second)
   defer cancel()
   var node string
   if err:=f.pool.QueryRow(ctx,`SELECT server_id::text FROM vpn_credential_revocations WHERE id=$1::uuid`,f.operation).Scan(&node);err!=nil{t.Fatal(err)}
   blocker,err:=f.pool.Begin(ctx)
   if err!=nil{t.Fatal(err)}
   defer blocker.Rollback(ctx)
   if _,err=blocker.Exec(ctx,`SELECT id FROM servers WHERE id=$1::uuid FOR UPDATE`,node);err!=nil{t.Fatal(err)}
   cfg,err:=pgxpool.ParseConfig(os.Getenv("ROUTEGATE_TEST_DATABASE_URL"))
   if err!=nil{t.Fatal(err)}
   if cfg.ConnConfig.RuntimeParams==nil{cfg.ConnConfig.RuntimeParams=map[string]string{}}
   cfg.ConnConfig.RuntimeParams["application_name"]="rg140-dispatch-gate-lock-wait"
   contender,err:=pgxpool.NewWithConfig(ctx,cfg)
   if err!=nil{t.Fatal(err)}
   defer contender.Close()
   result:=make(chan error,1)
   go func(){result<-revocations.NewService(contender).CheckPreparedRemovalDispatchAdmission(ctx,f.operation,f.nonce,true)}()
   waiting:=false
   deadline:=time.Now().Add(5*time.Second)
   for time.Now().Before(deadline){
    var blocked bool
    if err=f.pool.QueryRow(ctx,`SELECT EXISTS(SELECT 1 FROM pg_stat_activity
      WHERE application_name='rg140-dispatch-gate-lock-wait'
      AND wait_event_type='Lock')`).Scan(&blocked);err!=nil{t.Fatal(err)}
    if blocked {waiting=true;break}
    select{
    case e:=<-result:t.Fatalf("gate returned before original server lock: %v",e)
    case <-time.After(10*time.Millisecond):
    }
   }
   if !waiting {t.Fatal("gate never waited for original server lock")}
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
   case e:=<-result:
    if !errors.Is(e,revocations.ErrPreflight){t.Fatal("invalidated authority passed lock wait",mode,e)}
   case <-ctx.Done():
    t.Fatal("blocked dispatch gate failed to return",ctx.Err())
   }
   var version,job int
   if err=f.pool.QueryRow(ctx,`SELECT
     (SELECT count(*) FROM config_versions WHERE id IN
       (SELECT candidate_version_id FROM vpn_credential_revocation_dispatch_bindings)),
     (SELECT count(*) FROM config_apply_jobs WHERE id IN
       (SELECT apply_job_id FROM vpn_credential_revocation_dispatch_bindings))`).Scan(&version,&job);err!=nil||version!=0||job!=0{
    t.Fatal("invalidated authority created executable records",err)
   }
  })
 }
}
