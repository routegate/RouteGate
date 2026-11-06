package revocations

import (
	"bytes"
	"context"
	"encoding/json"
	"reflect"

	"github.com/ikaevus/routegate/backend/internal/configs"
	"github.com/jackc/pgx/v5"
)

// PreparedRemovalIntegrity is secret-free inspection metadata. It is neither
// a task dispatch authorization nor evidence of an applied credential removal.
type PreparedRemovalIntegrity struct {
	OperationID          string
	AccountID            string
	ServerID             string
	AgentID              string
	AgentGeneration      int64
	BaselineVersionID    string
	BaselineHash         string
	CandidateHash        string
	BaselineRuntimeHash  string
	CandidateRuntimeHash string
}

// InspectPreparedRemovalIntegrity checks one coherent database snapshot of
// the original applied VLESS/Reality baseline, the frozen one-account delta
// and their separately pinned runtime hashes. It never returns credentials,
// creates a version/job, renews authority or marks runtime removal confirmed.
//
// REPEATABLE READ / READ ONLY is deliberate: this check cannot reserve an
// admission or authorize a later execution, which must revalidate under its
// own exclusive dispatch transaction.
func (s *Service) InspectPreparedRemovalIntegrity(ctx context.Context, operation string) (PreparedRemovalIntegrity, error) {
	var zero PreparedRemovalIntegrity
	if !evidenceID.MatchString(operation) {
		return zero, ErrPreflight
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return zero, err
	}
	defer tx.Rollback(ctx)

	p, err := inspectPreparedRemovalIntegrity(ctx, tx, operation)
	if err != nil {
		return zero, err
	}
	if err := tx.Commit(ctx); err != nil {
		return zero, err
	}
	return p, nil
}

func inspectPreparedRemovalIntegrity(ctx context.Context, tx pgx.Tx, operation string) (PreparedRemovalIntegrity, error) {
	var zero PreparedRemovalIntegrity
	var p PreparedRemovalIntegrity
	var storedCandidate []byte
	var accountNode, accountStatus, originalAccountStatus, identity, activeVersion, agentNode string
	var currentGeneration int64
	err := tx.QueryRow(ctx, `
SELECT r.id::text, r.vpn_account_id::text, r.server_id::text,
 r.agent_id::text, r.agent_credential_generation,
 r.baseline_version_id::text, r.baseline_hash, r.candidate_hash,
 r.baseline_runtime_hash, r.candidate_runtime_hash, r.candidate_config,
 r.account_status, v.server_id::text, v.status,
 COALESCE(v.vless_uuid::text,''),
 COALESCE(s.active_config_version_id::text,''), a.server_id::text,
 a.credential_generation
FROM vpn_credential_revocations r
JOIN servers s ON s.id=r.server_id
JOIN agents a ON a.id=r.agent_id
JOIN vpn_accounts v ON v.id=r.vpn_account_id
WHERE r.id=$1::uuid AND r.state='prepared'`, operation).Scan(
		&p.OperationID, &p.AccountID, &p.ServerID, &p.AgentID,
		&p.AgentGeneration, &p.BaselineVersionID, &p.BaselineHash,
		&p.CandidateHash, &p.BaselineRuntimeHash, &p.CandidateRuntimeHash,
		&storedCandidate, &originalAccountStatus, &accountNode,
		&accountStatus, &identity, &activeVersion, &agentNode,
		&currentGeneration)
	if err != nil {
		return zero, ErrPreflight
	}
	if p.OperationID != operation || accountNode != p.ServerID ||
		agentNode != p.ServerID || currentGeneration != p.AgentGeneration ||
		p.AgentGeneration < 1 || activeVersion != p.BaselineVersionID ||
		accountStatus != originalAccountStatus ||
		(accountStatus != "active" && accountStatus != "suspended" && accountStatus != "revoked") ||
		p.CandidateHash == p.BaselineHash ||
		p.CandidateRuntimeHash == p.BaselineRuntimeHash {
		return zero, ErrPreflight
	}
	for _, hash := range []string{p.BaselineHash, p.CandidateHash, p.BaselineRuntimeHash, p.CandidateRuntimeHash} {
		if !validRemovalHash(hash) {
			return zero, ErrPreflight
		}
	}
	repo := configs.NewTransactionRepository(tx)
	baseline, err := repo.GetConfigVersion(ctx, p.ServerID, p.BaselineVersionID)
	if err != nil || baseline.ConfigHash != p.BaselineHash ||
		baseline.Status != configs.StatusApplied || baseline.AppliedAt == nil {
		return zero, ErrPreflight
	}
	if err = repo.CheckRemovalDesiredBaseline(ctx, baseline, p.AccountID); err != nil {
		return zero, ErrPreflight
	}
	var before configs.RenderedConfig
	if json.Unmarshal(baseline.RenderedConfig, &before) != nil ||
		before.Agent == nil || before.Agent.ID != p.AgentID {
		return zero, ErrPreflight
	}
	expected, err := configs.PrepareVLESSRemoval(baseline, configs.VLESSRemovalTarget{
		ServerID: p.ServerID, AccountID: p.AccountID,
		VersionID: p.BaselineVersionID, ConfigHash: p.BaselineHash,
		VLESSUUID: identity,
	})
	if err != nil || expected.ConfigHash != p.CandidateHash {
		return zero, ErrPreflight
	}
	candidateWire, err := json.Marshal(expected.RenderedConfig)
	if err != nil || !samePreparedRemovalJSON(candidateWire, storedCandidate) {
		return zero, ErrPreflight
	}
	beforeRuntime, err := json.Marshal(before.SingBox)
	if err != nil {
		return zero, ErrPreflight
	}
	afterRuntime, err := json.Marshal(expected.RenderedConfig.SingBox)
	if err != nil {
		return zero, ErrPreflight
	}
	oldHash, e1 := RuntimeDigest(beforeRuntime)
	newHash, e2 := RuntimeDigest(afterRuntime)
	if e1 != nil || e2 != nil || oldHash != p.BaselineRuntimeHash ||
		newHash != p.CandidateRuntimeHash {
		return zero, ErrPreflight
	}
	return p, nil
}

func samePreparedRemovalJSON(a, b []byte) bool {
	decode := func(src []byte) (any, error) {
		d := json.NewDecoder(bytes.NewReader(src))
		d.UseNumber()
		var value any
		if err := d.Decode(&value); err != nil {
			return nil, err
		}
		return value, nil
	}
	first, err := decode(a)
	if err != nil {
		return false
	}
	second, err := decode(b)
	return err == nil && reflect.DeepEqual(first, second)
}
