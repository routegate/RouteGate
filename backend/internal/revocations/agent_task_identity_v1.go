package revocations

import (
 "bytes"
 "encoding/json"
 "errors"
 "io"
 "regexp"
)

// CredentialRemovalTaskIdentityV1 is a NON-EXECUTABLE, non-secret identity
// assertion. The wire fields must match one schema-165 reservation. It has no
// result/status/approval field and never authorizes a runtime mutation.
// Authentication is external: compare token hash to the CURRENT Agent row
// on Manager; compare verified local Agent generation on Agent.
type CredentialRemovalTaskIdentityV1 struct {
 SchemaVersion int `json:"schemaVersion"`
 TaskID string `json:"taskId"`
 ClaimID string `json:"claimId"`
 OperationID string `json:"operationId"`
 ServerID string `json:"serverId"`
 AgentID string `json:"agentId"`
 AgentCredentialGeneration int64 `json:"agentCredentialGeneration"`
 BaselineVersionID string `json:"baselineVersionId"`
 CandidateHash string `json:"candidateHash"`
 CandidateRuntimeHash string `json:"candidateRuntimeHash"`
 TaskKind string `json:"taskKind"`
}

var ErrCredentialRemovalTaskIdentity = errors.New("credential-removal task identity rejected")

var rg140IdentityUUID = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)
var rg140IdentitySHA = regexp.MustCompile(`^[0-9a-f]{64}$`)

// ParseCredentialRemovalTaskIdentityV1 is intentionally stricter than plain
// json.Unmarshal: no unknown keys, duplicate keys, missing fields, numbers
// outside int64, trailing JSON, nested payloads or credential material.
func ParseCredentialRemovalTaskIdentityV1(data []byte) (CredentialRemovalTaskIdentityV1,error) {
 deny:=func()(CredentialRemovalTaskIdentityV1,error){return CredentialRemovalTaskIdentityV1{},ErrCredentialRemovalTaskIdentity}
 if len(data)==0 || len(data)>4096{return deny()}
 d:=json.NewDecoder(bytes.NewReader(data))
 tok,err:=d.Token()
 if err!=nil || tok!=json.Delim('{'){return deny()}
 allowed:=map[string]bool{
  "schemaVersion":true, "taskId":true, "claimId":true, "operationId":true,
  "serverId":true, "agentId":true, "agentCredentialGeneration":true,
  "baselineVersionId":true,"candidateHash":true,
  "candidateRuntimeHash":true,"taskKind":true,
 }
 seen:=make(map[string]bool,len(allowed))
 for d.More(){
  key,err:=d.Token();if err!=nil{return deny()}
  name,ok:=key.(string)
  if !ok || !allowed[name] || seen[name]{return deny()}
  seen[name]=true
  var value json.RawMessage
  if err=d.Decode(&value);err!=nil{return deny()}
 }
 tok,err=d.Token()
 if err!=nil || tok!=json.Delim('}') || len(seen)!=len(allowed){return deny()}
 if _,err=d.Token();err!=io.EOF{return deny()}
 var v CredentialRemovalTaskIdentityV1
 strict:=json.NewDecoder(bytes.NewReader(data))
 strict.DisallowUnknownFields()
 if strict.Decode(&v)!=nil{return deny()}
 if v.SchemaVersion!=1 || v.TaskKind!="vless_credential_removal_v1" ||
   v.AgentCredentialGeneration<=0{return deny()}
 for _,id:=range []string{v.TaskID,v.ClaimID,v.OperationID,v.ServerID,v.AgentID,v.BaselineVersionID}{
  if !rg140IdentityUUID.MatchString(id){return deny()}
 }
 if !rg140IdentitySHA.MatchString(v.CandidateHash) ||
    !rg140IdentitySHA.MatchString(v.CandidateRuntimeHash){return deny()}
 return v,nil
}

// MatchCredentialRemovalTaskLocalIdentity is only a local identity comparison.
// A true result is NOT proof of Manager authentication, permission to execute,
// Agent durable journal admission, or successful remote credential removal.
func MatchCredentialRemovalTaskLocalIdentity(
 v CredentialRemovalTaskIdentityV1,serverID,agentID string,generation int64,
) bool {
 data,err:=json.Marshal(v)
 if err!=nil{return false}
 verified,err:=ParseCredentialRemovalTaskIdentityV1(data)
 if err!=nil || verified!=v{return false}
 return rg140IdentityUUID.MatchString(serverID) &&
  rg140IdentityUUID.MatchString(agentID) && generation>0 &&
  v.ServerID==serverID && v.AgentID==agentID &&
  v.AgentCredentialGeneration==generation &&
  v.SchemaVersion==1 && v.TaskKind=="vless_credential_removal_v1"
}
