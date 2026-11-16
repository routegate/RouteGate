package revocations

import (
 "encoding/json"
 "os"
 "strings"
 "testing"
)

func TestCredentialRemovalTaskIdentityV1SharedStrictContract(t *testing.T){
 raw,err:=os.ReadFile("../../../testdata/credential-removal-task-identity-v1.json")
 if err!=nil{t.Fatal(err)}
 valid,err:=ParseCredentialRemovalTaskIdentityV1(raw)
 if err!=nil || valid.SchemaVersion!=1 || valid.AgentCredentialGeneration!=7 ||
  valid.TaskKind!="vless_credential_removal_v1"{t.Fatalf("golden v1 identity refused: %+v %v",valid,err)}
 if !MatchCredentialRemovalTaskLocalIdentity(valid,valid.ServerID,valid.AgentID,7){
  t.Fatal("valid local Agent generation refused")
 }
 if MatchCredentialRemovalTaskLocalIdentity(valid,valid.ServerID,valid.AgentID,8) ||
  MatchCredentialRemovalTaskLocalIdentity(valid,valid.ServerID,
    "99999999-9999-4999-8999-999999999999",7) ||
  MatchCredentialRemovalTaskLocalIdentity(valid,
    "99999999-9999-4999-8999-999999999999",valid.AgentID,7){
  t.Fatal("cross Agent, generation or server identity accepted")
 }
 invalid:=valid;invalid.TaskID=""
 if MatchCredentialRemovalTaskLocalIdentity(invalid,valid.ServerID,valid.AgentID,7){
  t.Fatal("typed identity with missing task ID accepted")
 }
 altered:=func(name string,value any)[]byte{
  t.Helper()
  var data map[string]any
  if e:=json.Unmarshal(raw,&data);e!=nil{t.Fatal(e)}
  if value==nil{delete(data,name)}else{data[name]=value}
  b,e:=json.Marshal(data);if e!=nil{t.Fatal(e)}
  return b
 }
 for _,test:=range []struct{name string;raw []byte}{
  {"unknown success field",altered("status","succeeded")},
  {"missing claim",altered("claimId",nil)},
  {"missing generation",altered("agentCredentialGeneration",nil)},
  {"wrong task kind",altered("taskKind","config_apply")},
  {"wrong schema",altered("schemaVersion",2)},
  {"zero generation",altered("agentCredentialGeneration",0)},
  {"negative generation",altered("agentCredentialGeneration",-1)},
  {"fractional generation",altered("agentCredentialGeneration",1.5)},
  {"wrong UUID version",altered("taskId","11111111-1111-1111-8111-111111111111")},
  {"uppercase hash",altered("candidateHash",strings.Repeat("A",64))},
  {"wrong digest length",altered("candidateRuntimeHash","abcd")},
  {"nested extra",altered("resultPayload",map[string]any{"status":"succeeded"})},
  {"uppercase field",[]byte(strings.Replace(string(raw),`"taskId"`,`"TaskId"`,1))},
  {"duplicate field",[]byte(strings.Replace(string(raw),`"taskId":`,`"taskId":"33333333-3333-4333-8333-333333333333","taskId":`,1))},
  {"trailing object",append(append([]byte(nil),raw...),[]byte("{}")...)},
  {"array",[]byte("[]")},
  {"oversize",[]byte(strings.Repeat(" ",4097))},
 }{
  t.Run(test.name,func(t *testing.T){
   v,e:=ParseCredentialRemovalTaskIdentityV1(test.raw)
   if e==nil || v!=(CredentialRemovalTaskIdentityV1{}){
    t.Fatalf("invalid v1 identity accepted: %+v %v",v,e)
   }
  })
 }
 // The same canonical Go struct roundtrip is accepted on both sides.
 serialized,err:=json.Marshal(valid);if err!=nil{t.Fatal(err)}
 again,err:=ParseCredentialRemovalTaskIdentityV1(serialized)
 if err!=nil || again!=valid{t.Fatalf("roundtrip changed v1 contract: %+v %v",again,err)}
}
