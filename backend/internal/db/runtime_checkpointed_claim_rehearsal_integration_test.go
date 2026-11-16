package db

import (
 "context"
 "errors"
 "sync"
 "testing"
 "time"

 "github.com/jackc/pgx/v5"
)

func TestRuntimeCheckpointedClaimRehearsalV1BothKindsReplayRefused(t *testing.T){
 for _,kind:=range []string{"config_apply","vpn_core_service"}{
  t.Run(kind,func(t *testing.T){
   f,id,token:=checkpointedClaimRehearsalFixtureV1(t,kind,true,true)
   defer f.done()
   claimed,err:=rehearsalClaimCheckpointedMutationV1(f.ctx,f.pool,kind,id,token,false)
   if err!=nil||claimed!=id{t.Fatalf("single checkpoint claim failed: %q %v",claimed,err)}
   state,started:=checkpointedClaimRehearsalStateV1(t,f,kind,id)
   if state!="in_progress"||!started{t.Fatalf("task was not atomically claimed: %s %v",state,started)}
   claimed,err=rehearsalClaimCheckpointedMutationV1(f.ctx,f.pool,kind,id,token,false)
   if claimed!=""||!errors.Is(err,pgx.ErrNoRows){
    t.Fatalf("lost response led to a second claim: %q %v",claimed,err)
   }
  })
 }
}

func TestRuntimeCheckpointedClaimRehearsalV1AbsentStaleAndInvalidEvidenceRefused(t *testing.T){
 for _,kind:=range []string{"config_apply","vpn_core_service"}{
  for _,mode:=range []string{"missing_preparation","missing_checkpoint","wrong_token","rotated_generation","removed_capability"}{
   t.Run(kind+"/"+mode,func(t *testing.T){
    f,id,token:=checkpointedClaimRehearsalFixtureV1(
     t,kind,mode!="missing_preparation",mode!="missing_checkpoint",
    )
    defer f.done()
    switch mode{
    case "wrong_token":
     token="not-the-agent-token-hash"
    case "rotated_generation":
     _,err:=f.pool.Exec(f.ctx,`
UPDATE agents SET credential_generation=credential_generation+1,
 last_authenticated_heartbeat_at=NULL,last_authenticated_heartbeat_generation=NULL
WHERE id=$1::uuid
`,f.agentID)
     if err!=nil{t.Fatal(err)}
    case "removed_capability":
     _,err:=f.pool.Exec(f.ctx,`
UPDATE agents SET capabilities=capabilities-'runtimeMutationFencingV1'
WHERE id=$1::uuid
`,f.agentID)
     if err!=nil{t.Fatal(err)}
    }
    claimed,err:=rehearsalClaimCheckpointedMutationV1(f.ctx,f.pool,kind,id,token,false)
    if claimed!=""||!errors.Is(err,pgx.ErrNoRows){
     t.Fatalf("unsafe checkpoint claim was accepted: %q %v",claimed,err)
    }
    status,started:=checkpointedClaimRehearsalStateV1(t,f,kind,id)
    if status!="pending"||started{t.Fatalf("denied job mutated: %s %v",status,started)}
   })
  }
 }
}

func TestRuntimeCheckpointedClaimRehearsalV1AbortAfterUpdateRollsBack(t *testing.T){
 for _,kind:=range []string{"config_apply","vpn_core_service"}{
  t.Run(kind,func(t *testing.T){
   f,id,token:=checkpointedClaimRehearsalFixtureV1(t,kind,true,true)
   defer f.done()
   if claimed,err:=rehearsalClaimCheckpointedMutationV1(f.ctx,f.pool,kind,id,token,true);
    claimed!=""||err==nil{t.Fatalf("injected abort did not fail: %q %v",claimed,err)}
   state,started:=checkpointedClaimRehearsalStateV1(t,f,kind,id)
   if state!="pending"||started{t.Fatalf("uncommitted job mutation escaped: %s %v",state,started)}
   claimed,err:=rehearsalClaimCheckpointedMutationV1(f.ctx,f.pool,kind,id,token,false)
   if err!=nil||claimed!=id{t.Fatalf("safe retry after rollback failed: %q %v",claimed,err)}
  })
 }
}

func TestRuntimeCheckpointedClaimRehearsalV1ConcurrentClaimOneWinner(t *testing.T){
 for _,kind:=range []string{"config_apply","vpn_core_service"}{
  t.Run(kind,func(t *testing.T){
   f,id,token:=checkpointedClaimRehearsalFixtureV1(t,kind,true,true)
   defer f.done()
   ctx,cancel:=context.WithTimeout(f.ctx,15*time.Second)
   defer cancel()
   start:=make(chan struct{})
   out:=make(chan error,2)
   var wg sync.WaitGroup
   for i:=0;i<2;i++{
    wg.Add(1)
    go func(){
     defer wg.Done()
     <-start
     _,err:=rehearsalClaimCheckpointedMutationV1(ctx,f.pool,kind,id,token,false)
     out<-err
    }()
   }
   close(start)
   wg.Wait()
   close(out)
   wins,refused:=0,0
   for err:=range out {
    if err==nil{wins++;continue}
    if errors.Is(err,pgx.ErrNoRows){refused++;continue}
    t.Fatalf("unexpected competing transaction error: %v",err)
   }
   if wins!=1||refused!=1{
    t.Fatalf("concurrent claims broke single-dispatch: accepted=%d refused=%d",wins,refused)
   }
  })
 }
}

func TestRuntimeCheckpointedClaimRehearsalV1PreparedStillNotEnrolled(t *testing.T){
 f,id,token:=checkpointedClaimRehearsalFixtureV1(t,"config_apply",true,true)
 defer f.done()
 before,err:=inspectRecoveryEnrollmentPreflightV1(f.ctx,f.pool,f.agentID)
 if err!=nil||!before.Unattested||before.Assessment!=recoveryEnrollmentManualReviewUnattestedV1{
  t.Fatalf("test-only transaction got promoted to an authorized enrollment: %+v %v",before,err)
 }
 if _,err:=rehearsalClaimCheckpointedMutationV1(f.ctx,f.pool,"config_apply",id,token,false);err!=nil{
  t.Fatal(err)
 }
 after,err:=inspectRecoveryEnrollmentPreflightV1(f.ctx,f.pool,f.agentID)
 if err!=nil||!after.Unattested||after.Assessment!=recoveryEnrollmentManualReviewUnattestedV1{
  t.Fatalf("synthetic claimed state became host truth: %+v %v",after,err)
 }
}
