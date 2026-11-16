package revocations

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"

	"github.com/ikaevus/routegate/backend/internal/agents"
	"github.com/jackc/pgx/v5/pgxpool"
)

// AgentEvidenceReadback is a strictly read-only internal proof comparator.
// It is NOT an Agent endpoint, a task completion, a stored receipt, a
// success-state transition or evidence that client sessions were terminated.
type AgentEvidenceReadback struct { pool *pgxpool.Pool }

func NewAgentEvidenceReadback(pool *pgxpool.Pool) *AgentEvidenceReadback {
	return &AgentEvidenceReadback{pool:pool}
}

type AgentEvidenceReadbackResult struct {
	OperationID string
	ClaimID string
	ReportSHA256 string
	Matched bool
}

// Check performs a bounded read-only comparison against persisted
// claim+preparation+Agent-token+candidate-version state. No token, JSON
// report, UUID, IP or live config is ever logged or persisted here.
//
// Contract for future task wiring: the one-shot claim UUID is also the
// intended credential-removal job ID. An alternative mapping requires a
// separately reviewed durable authenticated mapping before activation.
// The future HTTP handler MUST obtain rawAgentToken only from the validated
// Agent Authorization header, over protected transport, never from a user
// or a browser form. This helper does not register such a handler.
func (v *AgentEvidenceReadback) Check(ctx context.Context, rawAgentToken, operationID string, report []byte) (AgentEvidenceReadbackResult,error) {
	fail:=func()(AgentEvidenceReadbackResult,error){return AgentEvidenceReadbackResult{},ErrCredentialRemoval}
	if v==nil||v.pool==nil||!evidenceID.MatchString(operationID)||
		!strings.HasPrefix(rawAgentToken,"rg_agent_")||len(rawAgentToken)>256||
		len(report)==0||len(report)>16384 {
		return fail()
	}

	// Extract only the candidate version ID for a SELECT lookup. It remains
	// untrusted and will be independently checked by VerifySuccess below,
	// including duplicate keys, all IDs, hash binding and service generation.
	var untrusted struct{ConfigVersionID string `json:"configVersionId"`}
	if json.Unmarshal(report,&untrusted)!=nil||!evidenceID.MatchString(untrusted.ConfigVersionID) {
		return fail()
	}

	var bound EvidenceBinding
	var claimed, agentGeneration int64
	var currentGeneration int64
	var agentID, serverID, claimID, accountID, baselineID, oldRuntimeHash, newRuntimeHash, candidateConfigHash string
	var targetPort int
	err:=v.pool.QueryRow(ctx,`
		SELECT c.claim_id::text,c.agent_id::text,c.server_id::text,
		       r.vpn_account_id::text,c.baseline_version_id::text,
		       r.baseline_runtime_hash,c.candidate_runtime_hash,
		       c.candidate_hash,s.vless_port,c.agent_credential_generation,
		       a.credential_generation,cv.config_hash
		FROM vpn_credential_revocation_dispatch_claims c
		JOIN vpn_credential_revocations r ON r.id=c.operation_id
		JOIN vpn_credential_revocation_confirmations confirmation ON confirmation.operation_id=c.operation_id
		JOIN agents a ON a.id=c.agent_id AND a.server_id=c.server_id
		JOIN servers s ON s.id=c.server_id
		JOIN config_versions cv ON cv.id=$3::uuid AND cv.server_id=c.server_id
		WHERE c.operation_id=$1::uuid AND a.token_hash=$2
		  AND c.state='outcome_unknown' AND r.state='prepared'
		  AND confirmation.agent_id=c.agent_id
		  AND confirmation.agent_credential_generation=c.agent_credential_generation
		  AND confirmation.candidate_hash=c.candidate_hash
		  AND confirmation.candidate_runtime_hash=c.candidate_runtime_hash
		  AND confirmation.baseline_version_id=c.baseline_version_id
		  AND c.confirmation_at=confirmation.confirmed_at
		  AND a.credential_generation=c.agent_credential_generation
		  AND cv.status='applied' AND cv.config_hash=c.candidate_hash
	`,operationID,agents.HashToken(rawAgentToken),untrusted.ConfigVersionID).Scan(
		&claimID,&agentID,&serverID,&accountID,&baselineID,&oldRuntimeHash,
		&newRuntimeHash,&candidateConfigHash,&targetPort,&claimed,&currentGeneration,&bound.ConfigHash)
	if err!=nil||claimed<=0||currentGeneration!=claimed||bound.ConfigHash!=candidateConfigHash{
		return fail()
	}
	bound=EvidenceBinding{
		OperationID:operationID,JobID:claimID,AccountID:accountID,
		ServerID:serverID,AgentID:agentID,BaselineVersionID:baselineID,
		ConfigVersionID:untrusted.ConfigVersionID,ConfigHash:candidateConfigHash,
		BaselineRuntimeHash:oldRuntimeHash,CandidateRuntimeHash:newRuntimeHash,
		ListenerPort:targetPort,
	}
	if _,err=VerifySuccess(report,bound);err!=nil {
		return fail()
	}
	digest:=sha256.Sum256(report)
	return AgentEvidenceReadbackResult{OperationID:operationID,ClaimID:claimID,ReportSHA256:hex.EncodeToString(digest[:]),Matched:true},nil
}
