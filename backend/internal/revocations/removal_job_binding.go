package revocations

import (
	"context"
)

// PreparedRemovalJobIDs are immutable PROPOSED identifiers only. No
// config_version or config_apply_job exists and no dispatch is authorized.
type PreparedRemovalJobIDs struct {
	OperationID          string
	CandidateVersionID    string
	ApplyJobID            string
	AgentGeneration       int64
	BaselineVersionID     string
	CandidateHash         string
	CandidateRuntimeHash  string
}

// ReservePreparedRemovalJobIDs assigns stable future IDs to a separately
// validated prepared operation. This MUST NOT be used as an authority for
// dispatch: a future coordinator must re-check the exact human authorization,
// Agent generation, candidate, node locks and DB guards in the same transaction
// that actually creates a config version and an Agent job.
func (s *Service) ReservePreparedRemovalJobIDs(ctx context.Context, operation string) (PreparedRemovalJobIDs, error) {
	var zero PreparedRemovalJobIDs
	inspected, err := s.InspectPreparedRemovalIntegrity(ctx, operation)
	if err != nil {
		return zero, ErrPreflight
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return zero, err
	}
	defer tx.Rollback(ctx)
	var node string
	if err = tx.QueryRow(ctx, `SELECT server_id::text FROM vpn_credential_revocations WHERE id=$1::uuid`, operation).Scan(&node); err != nil {
		return zero, ErrPreflight
	}
	if node != inspected.ServerID {
		return zero, ErrPreflight
	}
	if _, err = tx.Exec(ctx, `SELECT id FROM servers WHERE id=$1::uuid FOR UPDATE`, node); err != nil {
		return zero, err
	}
	var state, agent, baselineID, candidateHash, candidateRuntimeHash string
	var generation int64
	if err = tx.QueryRow(ctx, `SELECT state,agent_id::text,agent_credential_generation,
		baseline_version_id::text,candidate_hash,candidate_runtime_hash
		FROM vpn_credential_revocations WHERE id=$1::uuid FOR UPDATE`, operation).Scan(
			&state, &agent, &generation, &baselineID, &candidateHash, &candidateRuntimeHash); err != nil {
		return zero, ErrPreflight
	}
	if state != "prepared" || agent != inspected.AgentID ||
		generation != inspected.AgentGeneration ||
		baselineID != inspected.BaselineVersionID ||
		candidateHash != inspected.CandidateHash ||
		candidateRuntimeHash != inspected.CandidateRuntimeHash {
		return zero, ErrPreflight
	}
	// The SQL insertion trigger independently verifies reservation state,
	// original generation, applied pointer and hashes under original locks.
	// ON CONFLICT never replaces allocated IDs, even if two callers race.
	if _, err = tx.Exec(ctx, `INSERT INTO vpn_credential_revocation_dispatch_bindings
	 (operation_id,agent_credential_generation,baseline_version_id,candidate_hash,candidate_runtime_hash)
	 VALUES ($1::uuid,$2,$3::uuid,$4,$5)
	 ON CONFLICT (operation_id) DO NOTHING`, operation, generation, baselineID, candidateHash, candidateRuntimeHash); err != nil {
		return zero, ErrPreflight
	}
	var ids PreparedRemovalJobIDs
	if err = tx.QueryRow(ctx, `SELECT operation_id::text,candidate_version_id::text,
	 apply_job_id::text,agent_credential_generation,baseline_version_id::text,
	 candidate_hash,candidate_runtime_hash
	 FROM vpn_credential_revocation_dispatch_bindings WHERE operation_id=$1::uuid`, operation).Scan(
		&ids.OperationID, &ids.CandidateVersionID, &ids.ApplyJobID,
		&ids.AgentGeneration, &ids.BaselineVersionID,
		&ids.CandidateHash, &ids.CandidateRuntimeHash); err != nil {
		return zero, ErrPreflight
	}
	if ids.OperationID != inspected.OperationID ||
		ids.CandidateVersionID == ids.ApplyJobID ||
		ids.CandidateVersionID == ids.BaselineVersionID ||
		ids.ApplyJobID == ids.BaselineVersionID ||
		ids.AgentGeneration != inspected.AgentGeneration ||
		ids.BaselineVersionID != inspected.BaselineVersionID ||
		ids.CandidateHash != inspected.CandidateHash ||
		ids.CandidateRuntimeHash != inspected.CandidateRuntimeHash ||
		!evidenceID.MatchString(ids.CandidateVersionID) ||
		!evidenceID.MatchString(ids.ApplyJobID) {
		return zero, ErrPreflight
	}
	if err = tx.Commit(ctx); err != nil {
		return zero, err
	}
	return ids, nil
}

