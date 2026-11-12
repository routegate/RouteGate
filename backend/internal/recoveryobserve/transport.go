package recoveryobserve

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/jackc/pgx/v5/pgxpool"
)

// ObservationTransport is a testable, deliberately UNREGISTERED HTTP adapter
// for Agent's EXISTING outbound bearer-token channel. It is not in
// http.NewRouter or NewRootHandler, is not wired into Agent's heartbeat or
// task poll, and cannot execute, retry, unlock or release any VPN mutation.
type ObservationTransport struct {
	pool *pgxpool.Pool
	issuer *ObservationIssuer
}

func NewObservationTransport(pool *pgxpool.Pool) *ObservationTransport {
	return &ObservationTransport{pool:pool,issuer:NewObservationIssuer(pool)}
}

type PendingObservationAttempt struct {
	SchemaVersion int `json:"schemaVersion"`
	AttemptID string `json:"attemptId"`
	ReservationID string `json:"reservationId"`
	AttemptEpoch int64 `json:"attemptEpoch"`
	TaskID string `json:"taskId"`
	AgentID string `json:"agentId"`
	ServerID string `json:"serverId"`
	AgentCredentialGeneration int64 `json:"agentCredentialGeneration"`
}

type PendingObservationResponse struct {
	Pending *PendingObservationAttempt `json:"pending"`
}

type observationIssueRequest struct {
	AttemptID string `json:"attemptId"`
	Evidence EvidenceDigestsV1 `json:"evidence"`
}

func noStoreObservation(w http.ResponseWriter) {
	w.Header().Set("Cache-Control","no-store, no-cache, max-age=0")
	w.Header().Set("Pragma","no-cache")
	w.Header().Set("Vary","Authorization")
	w.Header().Set("X-Content-Type-Options","nosniff")
	w.Header().Set("Content-Type","application/json")
}

func strictObservationBearer(header string) (string,bool) {
	const prefix="Bearer "
	if len(header)<=len(prefix) || header[:len(prefix)]!=prefix {
		return "",false
	}
	token:=header[len(prefix):]
	return token,validAgentBearerV1(token)
}

func observationHTTPError(w http.ResponseWriter,status int) {
	w.WriteHeader(status)
	// Uniform errors: no tokens, hashes, nonce, attempt identities or SQL
	// details are reflected back from a recovery transport request.
	_,_ = io.WriteString(w,`{"error":"observation_unavailable"}`)
}

// Pending is read-only metadata discovery for an authenticated Agent.
// A valid token with no eligible held attempt gets {"pending":null};
// it does not receive another Agent's attempt. No nonce is created.
func (h *ObservationTransport) Pending(w http.ResponseWriter,r *http.Request) {
	noStoreObservation(w)
	if h==nil||h.pool==nil||r.Method!=http.MethodGet{
		observationHTTPError(w,http.StatusNotFound);return
	}
	token,ok:=strictObservationBearer(r.Header.Get("Authorization"))
	if !ok {observationHTTPError(w,http.StatusUnauthorized);return}
	hash:=sha256.Sum256([]byte(token))
	rows,err:=h.pool.Query(r.Context(),`
SELECT COALESCE(p.id::text,''),COALESCE(p.reservation_id::text,''),
       COALESCE(p.attempt_epoch,0),COALESCE(p.job_id::text,''),
       COALESCE(p.agent_id::text,''),COALESCE(p.server_id::text,''),
       COALESCE(p.agent_credential_generation,0)
FROM agents a
LEFT JOIN vpn_runtime_recovery_attempts p ON p.agent_id=a.id
 AND p.server_id=a.server_id
 AND p.agent_credential_generation=a.credential_generation
 AND p.state='awaiting_fresh_observation'
 AND a.capabilities @> '{"runtimeMutationFencingV1":true}'::jsonb
 AND NOT EXISTS(
     SELECT 1 FROM vpn_runtime_recovery_observation_nonces n
     WHERE n.attempt_id=p.id
 )
WHERE a.token_hash=$1
LIMIT 2
`,hex.EncodeToString(hash[:]))
	if err!=nil{observationHTTPError(w,http.StatusServiceUnavailable);return}
	defer rows.Close()
	var response PendingObservationResponse
	count:=0
	for rows.Next(){
		count++
		if count>1{observationHTTPError(w,http.StatusServiceUnavailable);return}
		var p PendingObservationAttempt
		if err=rows.Scan(&p.AttemptID,&p.ReservationID,&p.AttemptEpoch,
			&p.TaskID,&p.AgentID,&p.ServerID,&p.AgentCredentialGeneration);err!=nil{
			observationHTTPError(w,http.StatusServiceUnavailable);return
		}
		if p.AttemptID!="" {
			p.SchemaVersion=1
			response.Pending=&p
		}
	}
	if rows.Err()!=nil {observationHTTPError(w,http.StatusServiceUnavailable);return}
	if count==0 {observationHTTPError(w,http.StatusUnauthorized);return}
	_ = json.NewEncoder(w).Encode(response)
}

// Issue accepts digest claims ONLY through the bearer Authorization header,
// never user-supplied Agent ID/credential fields. It calls the private
// transactional issuer from ADR-0023, which performs the real DB recheck,
// writes one nonce+audit and returns the secret only AFTER COMMIT.
// The route is intentionally NOT registered in production.
func (h *ObservationTransport) Issue(w http.ResponseWriter,r *http.Request) {
	noStoreObservation(w)
	if h==nil||h.issuer==nil||r.Method!=http.MethodPost{
		observationHTTPError(w,http.StatusNotFound);return
	}
	token,ok:=strictObservationBearer(r.Header.Get("Authorization"))
	if !ok {observationHTTPError(w,http.StatusUnauthorized);return}
	decoder:=json.NewDecoder(http.MaxBytesReader(w,r.Body,4096))
	decoder.DisallowUnknownFields()
	var request observationIssueRequest
	if err:=decoder.Decode(&request);err!=nil{
		observationHTTPError(w,http.StatusBadRequest);return
	}
	var extra any
	if err:=decoder.Decode(&extra);!errors.Is(err,io.EOF){
		observationHTTPError(w,http.StatusBadRequest);return
	}
	issued,err:=h.issuer.IssueFromAuthenticatedAgentClaim(
		r.Context(),token,request.AttemptID,request.Evidence,
	)
	if err!=nil{
		if errors.Is(err,ErrObservationIssueRefused) {
			observationHTTPError(w,http.StatusConflict)
		}else{
			observationHTTPError(w,http.StatusServiceUnavailable)
		}
		return
	}
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(issued)
}
