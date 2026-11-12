package tasks

import (
 "bytes"
 "context"
 "encoding/json"
 "errors"
 "os"
 "path/filepath"
 "strings"
 "sync"
 "testing"
 "time"
)

func recoveryEnrollmentGenesisFixtureV1() recoveryEnrollmentGenesisV1 {
 return recoveryEnrollmentGenesisV1{
  SchemaVersion:1,
  ManagerPreparationID:"11111111-1111-4111-8111-111111111111",
  AgentID:"33333333-3333-4333-8333-333333333333",
  ServerID:"22222222-2222-4222-8222-222222222222",
  AgentCredentialGeneration:1,
  ManagerPreparationPhase:"prepared_unattested",
  NextJournalSequence:1,
 }
}

func recoveryEnrollmentOfflineDirectoriesV1(t *testing.T)(string,string){
 t.Helper()
 root:=t.TempDir()
 runtimeDir:=filepath.Join(root,"runtime")
 mutation,err:=lockRuntimeMutation(runtimeDir)
 if err!=nil{t.Fatal("create isolated test-only runtime lock:",err)}
 if err:=mutation.Close();err!=nil{t.Fatal(err)}
 journalDir:=filepath.Join(root,"journal")
 if err:=os.Mkdir(journalDir,0700);err!=nil{t.Fatal(err)}
 return runtimeDir,journalDir
}

func requireRecoveryEnrollmentGenesisRefusedV1(t *testing.T,err error){
 t.Helper()
 if !errors.Is(err,errRecoveryJournalUntrustedV1){
  t.Fatalf("unsafe offline enrollment must refuse, got: %v",err)
 }
}

func TestRuntimeRecoveryEnrollmentGenesisOfflineV1OnlyUnattestedAndImmutable(t *testing.T){
 runtimeDir,journalDir:=recoveryEnrollmentOfflineDirectoriesV1(t)
 genesis:=recoveryEnrollmentGenesisFixtureV1()
 ctx:=context.Background()
 if result,err:=inspectRecoveryEnrollmentGenesisOfflineV1(ctx,runtimeDir,journalDir,genesis);
  result!=(recoveryEnrollmentGenesisObservationV1{})||!errors.Is(err,errRecoveryJournalUntrustedV1){
  t.Fatalf("unprovisioned genesis read was accepted: %+v %v",result,err)
 }
 if err:=writeRecoveryEnrollmentGenesisOfflineV1(ctx,runtimeDir,journalDir,genesis,nil);err!=nil{
  t.Fatal("offline genesis did not persist:",err)
 }
 path:=filepath.Join(journalDir,recoveryEnrollmentGenesisFilenameV1)
 before,err:=os.ReadFile(path)
 if err!=nil{t.Fatal(err)}
 canonical,err:=json.Marshal(genesis)
 if err!=nil||!bytes.Equal(before,canonical){t.Fatal("genesis bytes are not canonical",err)}
 observation,err:=inspectRecoveryEnrollmentGenesisOfflineV1(ctx,runtimeDir,journalDir,genesis)
 want,hashErr:=recoveryEnrollmentGenesisDigestV1(genesis)
 if err!=nil||hashErr!=nil||!observation.Unattested||observation.GenesisSHA256!=want{
  t.Fatalf("genesis inspection elevated trust or returned wrong hash: %+v %v %v",observation,err,hashErr)
 }
 after,err:=os.ReadFile(path)
 if err!=nil||!bytes.Equal(before,after){t.Fatal("read-only inspection changed genesis",err)}
 requireRecoveryEnrollmentGenesisRefusedV1(t,
  writeRecoveryEnrollmentGenesisOfflineV1(ctx,runtimeDir,journalDir,genesis,nil))
 // The old mutation journal prototype cannot interpret enrollment genesis
 // as an empty chain or as runtime mutation evidence.
 result,err:=reviewRecoveryJournalDiskV1(journalDir)
 if result!=(recoveryJournalIntegrityObservationV1{})||!errors.Is(err,errRecoveryJournalUntrustedV1){
  t.Fatalf("legacy journal parser silently accepted genesis: %+v %v",result,err)
 }
 // Both locks are released after historical reading.
 lock,err:=holdExistingRuntimeMutationForGenesisV1(runtimeDir)
 if err!=nil{t.Fatal("runtime flock leaked:",err)}
 if err:=lock.Close();err!=nil{t.Fatal(err)}
 journalLock,err:=openRecoveryJournalDiskV1(journalDir)
 if err!=nil{t.Fatal("journal flock leaked:",err)}
 if err:=journalLock.Close();err!=nil{t.Fatal(err)}
}

func TestRuntimeRecoveryEnrollmentGenesisOfflineV1ManagerIdentityGenerationDriftRefused(t *testing.T){
 runtimeDir,journalDir:=recoveryEnrollmentOfflineDirectoriesV1(t)
 genesis:=recoveryEnrollmentGenesisFixtureV1()
 if err:=writeRecoveryEnrollmentGenesisOfflineV1(context.Background(),runtimeDir,journalDir,genesis,nil);err!=nil{t.Fatal(err)}
 cases:=map[string]func(*recoveryEnrollmentGenesisV1){
  "different_preparation":func(g *recoveryEnrollmentGenesisV1){g.ManagerPreparationID=g.AgentID},
  "different_agent":func(g *recoveryEnrollmentGenesisV1){g.AgentID=g.ServerID},
  "different_server":func(g *recoveryEnrollmentGenesisV1){g.ServerID=g.AgentID},
  "rotated_generation":func(g *recoveryEnrollmentGenesisV1){g.AgentCredentialGeneration++},
  "zero_generation":func(g *recoveryEnrollmentGenesisV1){g.AgentCredentialGeneration=0},
  "bad_preparation":func(g *recoveryEnrollmentGenesisV1){g.ManagerPreparationID="not-a-uuid"},
  "schema_drift":func(g *recoveryEnrollmentGenesisV1){g.SchemaVersion++},
  "premature_enrolled_phase":func(g *recoveryEnrollmentGenesisV1){g.ManagerPreparationPhase="enrolled"},
  "wrong_journal_start":func(g *recoveryEnrollmentGenesisV1){g.NextJournalSequence=2},
 }
 for name,change:=range cases{
  t.Run(name,func(t *testing.T){
   expected:=genesis
   change(&expected)
   got,err:=inspectRecoveryEnrollmentGenesisOfflineV1(context.Background(),runtimeDir,journalDir,expected)
   requireRecoveryEnrollmentGenesisRefusedV1(t,err)
   if got!=(recoveryEnrollmentGenesisObservationV1{}){t.Fatal("mismatched Manager identity produced a receipt",got)}
  })
 }
}

func TestRuntimeRecoveryEnrollmentGenesisOfflineV1InvalidGenesisAndAbsentState(t *testing.T){
 genesis:=recoveryEnrollmentGenesisFixtureV1()
 ctx:=context.Background()
 t.Run("missing_runtime_directory",func(t *testing.T){
  root:=t.TempDir()
  journal:=filepath.Join(root,"journal")
  if err:=os.Mkdir(journal,0700);err!=nil{t.Fatal(err)}
  runtimeDir:=filepath.Join(root,"missing")
  requireRecoveryEnrollmentGenesisRefusedV1(t,writeRecoveryEnrollmentGenesisOfflineV1(ctx,runtimeDir,journal,genesis,nil))
  if _,err:=os.Lstat(runtimeDir);!os.IsNotExist(err){t.Fatal("prototype created a runtime root for legacy Agent",err)}
 })
 t.Run("missing_runtime_lock",func(t *testing.T){
  runtimeDir,journalDir:=recoveryEnrollmentOfflineDirectoriesV1(t)
  if err:=os.Remove(filepath.Join(runtimeDir,"runtime.lock"));err!=nil{t.Fatal(err)}
  requireRecoveryEnrollmentGenesisRefusedV1(t,writeRecoveryEnrollmentGenesisOfflineV1(ctx,runtimeDir,journalDir,genesis,nil))
  if _,err:=os.Lstat(filepath.Join(runtimeDir,"runtime.lock"));!os.IsNotExist(err){t.Fatal("prototype auto-recreated runtime lock",err)}
 })
 for _,mode:=range []string{"relative_runtime","world_readable_runtime","symlink_runtime","symlink_journal","world_readable_journal","nil_context","cancelled_context","bad_phase","bad_agent","zero_generation","existing_mutation_marker"}{
  t.Run(mode,func(t *testing.T){
   runtimeDir,journalDir:=recoveryEnrollmentOfflineDirectoriesV1(t)
   candidate:=genesis
   callCtx:=ctx
   switch mode{
   case "relative_runtime":runtimeDir="relative/runtime"
   case "world_readable_runtime":if err:=os.Chmod(runtimeDir,0755);err!=nil{t.Fatal(err)}
   case "symlink_runtime":
    link:=filepath.Join(t.TempDir(),"runtime-link")
    if err:=os.Symlink(runtimeDir,link);err!=nil{t.Fatal(err)}
    runtimeDir=link
   case "symlink_journal":
    link:=filepath.Join(t.TempDir(),"journal-link")
    if err:=os.Symlink(journalDir,link);err!=nil{t.Fatal(err)}
    journalDir=link
   case "world_readable_journal":if err:=os.Chmod(journalDir,0755);err!=nil{t.Fatal(err)}
   case "nil_context":callCtx=nil
   case "cancelled_context":
    cancelled,cancel:=context.WithCancel(ctx);cancel();callCtx=cancelled
   case "bad_phase":candidate.ManagerPreparationPhase="active"
   case "bad_agent":candidate.AgentID="invalid"
   case "zero_generation":candidate.AgentCredentialGeneration=0
   case "existing_mutation_marker":
    if err:=os.WriteFile(filepath.Join(runtimeDir,"mutation-inflight.json"),[]byte("{"),0600);err!=nil{t.Fatal(err)}
   }
   requireRecoveryEnrollmentGenesisRefusedV1(t,
    writeRecoveryEnrollmentGenesisOfflineV1(callCtx,runtimeDir,journalDir,candidate,nil))
  })
 }
}

func TestRuntimeRecoveryEnrollmentGenesisOfflineV1CorruptionAndUnknownEntriesRefused(t *testing.T){
 genesis:=recoveryEnrollmentGenesisFixtureV1()
 modes:=[]string{"empty","partial","duplicate_key","unknown_field","world_readable","hardlink","symlink","extra_file","missing_file"}
 for _,mode:=range modes{
  t.Run(mode,func(t *testing.T){
   runtimeDir,journalDir:=recoveryEnrollmentOfflineDirectoriesV1(t)
   if err:=writeRecoveryEnrollmentGenesisOfflineV1(context.Background(),runtimeDir,journalDir,genesis,nil);err!=nil{t.Fatal(err)}
   path:=filepath.Join(journalDir,recoveryEnrollmentGenesisFilenameV1)
   raw,err:=os.ReadFile(path)
   if err!=nil{t.Fatal(err)}
   switch mode {
   case "empty":if err:=os.WriteFile(path,nil,0600);err!=nil{t.Fatal(err)}
   case "partial":if err:=os.WriteFile(path,[]byte("{"),0600);err!=nil{t.Fatal(err)}
   case "duplicate_key":
    raw=bytes.Replace(raw,[]byte(`"schemaVersion":`),[]byte(`"schemaVersion":999,"schemaVersion":`),1)
    if err:=os.WriteFile(path,raw,0600);err!=nil{t.Fatal(err)}
   case "unknown_field":
    raw=bytes.Replace(raw,[]byte(`"nextJournalSequence":1`),[]byte(`"extra":true,"nextJournalSequence":1`),1)
    if err:=os.WriteFile(path,raw,0600);err!=nil{t.Fatal(err)}
   case "world_readable":if err:=os.Chmod(path,0644);err!=nil{t.Fatal(err)}
   case "hardlink":if err:=os.Link(path,filepath.Join(t.TempDir(),"alias"));err!=nil{t.Fatal(err)}
   case "symlink":
    if err:=os.Remove(path);err!=nil{t.Fatal(err)}
    if err:=os.Symlink(filepath.Join(t.TempDir(),"external"),path);err!=nil{t.Fatal(err)}
   case "extra_file":if err:=os.WriteFile(filepath.Join(journalDir,"unexpected"),[]byte("x"),0600);err!=nil{t.Fatal(err)}
   case "missing_file":if err:=os.Remove(path);err!=nil{t.Fatal(err)}
   }
   got,err:=inspectRecoveryEnrollmentGenesisOfflineV1(context.Background(),runtimeDir,journalDir,genesis)
   requireRecoveryEnrollmentGenesisRefusedV1(t,err)
   if got!=(recoveryEnrollmentGenesisObservationV1{}){t.Fatal("corrupt genesis accepted",got)}
   requireRecoveryEnrollmentGenesisRefusedV1(t,
    writeRecoveryEnrollmentGenesisOfflineV1(context.Background(),runtimeDir,journalDir,genesis,nil))
  })
 }
}

func TestRuntimeRecoveryEnrollmentGenesisOfflineV1CrashAtPersistenceBoundaries(t *testing.T){
 genesis:=recoveryEnrollmentGenesisFixtureV1()
 for _,stage:=range []string{"after_anchor_create","after_anchor_write","after_anchor_file_fsync","after_anchor_directory_fsync","after_create","after_write","after_file_fsync","after_directory_fsync"}{
  t.Run(stage,func(t *testing.T){
   runtimeDir,journalDir:=recoveryEnrollmentOfflineDirectoriesV1(t)
   reached:=false
   err:=writeRecoveryEnrollmentGenesisOfflineV1(context.Background(),runtimeDir,journalDir,genesis,func(point string)error{
    if point!=stage{return nil}
    reached=true
    // The code must retain both locks until AFTER both fsync boundaries.
    if held,err:=holdExistingRuntimeMutationForGenesisV1(runtimeDir);err==nil{
     _=held.Close();t.Fatal("runtime mutation lock not retained during",stage)
    }
    if held,err:=openRecoveryJournalDiskV1(journalDir);err==nil{
     _=held.Close();t.Fatal("journal directory lock not retained during",stage)
    }
    return errors.New("injected post-boundary crash")
   })
   if !reached{t.Fatal("fault boundary not reached")}
   requireRecoveryEnrollmentGenesisRefusedV1(t,err)
   anchor:=filepath.Join(runtimeDir,recoveryEnrollmentAnchorFilenameV1)
   if _,err:=os.Lstat(anchor);err!=nil{t.Fatal("ambiguous runtime continuity anchor deleted",err)}
   path:=filepath.Join(journalDir,recoveryEnrollmentGenesisFilenameV1)
   if stage=="after_create"||!strings.HasPrefix(stage,"after_anchor_"){
    if _,err:=os.Lstat(path);err!=nil{t.Fatal("ambiguous genesis silently deleted",err)}
   }else if _,err:=os.Lstat(path);!os.IsNotExist(err){
    t.Fatal("journal genesis appeared before runtime anchor was safely fsynced",err)
   }
   result,err:=inspectRecoveryEnrollmentGenesisOfflineV1(context.Background(),runtimeDir,journalDir,genesis)
   if strings.HasPrefix(stage,"after_anchor_")||stage=="after_create"{
    requireRecoveryEnrollmentGenesisRefusedV1(t,err)
    if result!=(recoveryEnrollmentGenesisObservationV1{}){t.Fatal("partial two-file transaction accepted",result)}
   }else if err!=nil||!result.Unattested{
    t.Fatalf("persisted but ambiguous file must remain only unattested: %+v %v",result,err)
   }
   // Even after a valid-looking ambiguous write, retries MUST refuse.
   requireRecoveryEnrollmentGenesisRefusedV1(t,
    writeRecoveryEnrollmentGenesisOfflineV1(context.Background(),runtimeDir,journalDir,genesis,nil))
  })
 }
}

func TestRuntimeRecoveryEnrollmentGenesisOfflineV1ConcurrentOnlyOneWriter(t *testing.T){
 runtimeDir,journalDir:=recoveryEnrollmentOfflineDirectoriesV1(t)
 genesis:=recoveryEnrollmentGenesisFixtureV1()
 ctx,cancel:=context.WithTimeout(context.Background(),12*time.Second)
 defer cancel()
 start:=make(chan struct{})
 out:=make(chan error,2)
 var wg sync.WaitGroup
 for i:=0;i<2;i++{
  wg.Add(1)
  go func(){
   defer wg.Done()
   <-start
   out<-writeRecoveryEnrollmentGenesisOfflineV1(ctx,runtimeDir,journalDir,genesis,nil)
  }()
 }
 close(start);wg.Wait();close(out)
 ok,refused:=0,0
 for err:=range out{
  if err==nil{ok++}else if errors.Is(err,errRecoveryJournalUntrustedV1){refused++}else{t.Fatal("unexpected concurrent genesis writer error",err)}
 }
 if ok!=1||refused!=1{t.Fatalf("concurrent genesis recreated or overwritten: %d/%d",ok,refused)}
}

func TestRuntimeRecoveryEnrollmentGenesisOfflineV1BusyLocksRefuseWithoutWrites(t *testing.T){
 genesis:=recoveryEnrollmentGenesisFixtureV1()
 for _,mode:=range []string{"runtime_busy","journal_busy"}{
  t.Run(mode,func(t *testing.T){
   runtimeDir,journalDir:=recoveryEnrollmentOfflineDirectoriesV1(t)
   var lock *os.File
   var err error
   if mode=="runtime_busy"{lock,err=holdExistingRuntimeMutationForGenesisV1(runtimeDir)}else{
    lock,err=openRecoveryJournalDiskV1(journalDir)
   }
   if err!=nil{t.Fatal(err)}
   defer lock.Close()
   requireRecoveryEnrollmentGenesisRefusedV1(t,
    writeRecoveryEnrollmentGenesisOfflineV1(context.Background(),runtimeDir,journalDir,genesis,nil))
   path:=filepath.Join(journalDir,recoveryEnrollmentGenesisFilenameV1)
   if _,err:=os.Lstat(path);!os.IsNotExist(err){t.Fatal("busy lock still caused a write",err)}
  })
 }
}

func TestRuntimeRecoveryEnrollmentGenesisOfflineV1NeverApprovesFabricatedOrigin(t *testing.T){
 runtimeDir,journalDir:=recoveryEnrollmentOfflineDirectoriesV1(t)
 fake:=recoveryEnrollmentGenesisFixtureV1()
 fake.ManagerPreparationID="aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
 fake.AgentCredentialGeneration=47
 if err:=writeRecoveryEnrollmentGenesisOfflineV1(context.Background(),runtimeDir,journalDir,fake,nil);err!=nil{t.Fatal(err)}
 obs,err:=inspectRecoveryEnrollmentGenesisOfflineV1(context.Background(),runtimeDir,journalDir,fake)
 if err!=nil||!obs.Unattested||len(obs.GenesisSHA256)!=64{
  t.Fatalf("forged Manager input was promoted or refused unexpectedly: %+v %v",obs,err)
 }
}

func TestRuntimeRecoveryEnrollmentGenesisOfflineV1RuntimeContinuityAnchorCannotBeBypassed(t *testing.T){
 genesis:=recoveryEnrollmentGenesisFixtureV1()
 for _,mode:=range []string{"missing","empty","partial","changed_generation","world_readable","hardlink","symlink","extra_json_key"}{
  t.Run(mode,func(t *testing.T){
   runtimeDir,journalDir:=recoveryEnrollmentOfflineDirectoriesV1(t)
   if err:=writeRecoveryEnrollmentGenesisOfflineV1(context.Background(),runtimeDir,journalDir,genesis,nil);err!=nil{t.Fatal(err)}
   path:=filepath.Join(runtimeDir,recoveryEnrollmentAnchorFilenameV1)
   raw,err:=os.ReadFile(path)
   if err!=nil{t.Fatal(err)}
   switch mode{
   case "missing":
    if err:=os.Remove(path);err!=nil{t.Fatal(err)}
   case "empty":
    if err:=os.WriteFile(path,nil,0600);err!=nil{t.Fatal(err)}
   case "partial":
    if err:=os.WriteFile(path,[]byte("{"),0600);err!=nil{t.Fatal(err)}
   case "changed_generation":
    other:=genesis
    other.AgentCredentialGeneration++
    payload,err:=json.Marshal(other)
    if err!=nil{t.Fatal(err)}
    if err:=os.WriteFile(path,payload,0600);err!=nil{t.Fatal(err)}
   case "world_readable":
    if err:=os.Chmod(path,0644);err!=nil{t.Fatal(err)}
   case "hardlink":
    if err:=os.Link(path,filepath.Join(t.TempDir(),"alias"));err!=nil{t.Fatal(err)}
   case "symlink":
    if err:=os.Remove(path);err!=nil{t.Fatal(err)}
    if err:=os.Symlink(filepath.Join(t.TempDir(),"alias"),path);err!=nil{t.Fatal(err)}
   case "extra_json_key":
    raw=bytes.Replace(raw,[]byte(`"nextJournalSequence":1`),[]byte(`"unexpected":true,"nextJournalSequence":1`),1)
    if err:=os.WriteFile(path,raw,0600);err!=nil{t.Fatal(err)}
   }
   observed,err:=inspectRecoveryEnrollmentGenesisOfflineV1(context.Background(),runtimeDir,journalDir,genesis)
   requireRecoveryEnrollmentGenesisRefusedV1(t,err)
   if observed!=(recoveryEnrollmentGenesisObservationV1{}){t.Fatal("altered continuity anchor accepted",observed)}
   // Journal side is still present and blocks rebirth even when an attacker
   // deletes just the runtime anchor. Ambiguous state cannot be auto-repaired.
   requireRecoveryEnrollmentGenesisRefusedV1(t,
    writeRecoveryEnrollmentGenesisOfflineV1(context.Background(),runtimeDir,journalDir,genesis,nil))
  })
 }
}
