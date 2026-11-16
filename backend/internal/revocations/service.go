package revocations

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/ikaevus/routegate/backend/internal/configs"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var ErrPreflight = errors.New("credential revocation preparation blocked")

type Preparation struct {
	ID                string    `json:"id"`
	AccountID         string    `json:"accountId"`
	ServerID          string    `json:"serverId"`
	AgentID           string    `json:"agentId"`
	BaselineVersionID string    `json:"baselineVersionId"`
	State             string    `json:"state"`
	RequestedBy       string    `json:"requestedBy"`
	CreatedAt         time.Time `json:"createdAt"`
}
type PrepareInput struct {
	AccountID, ServerID, BaselineVersionID, BaselineHash, Actor string
}
type Service struct{ pool *pgxpool.Pool }

func NewService(pool *pgxpool.Pool) *Service { return &Service{pool} }

const selection = `SELECT id::text,vpn_account_id::text,server_id::text,agent_id::text,baseline_version_id::text,state,requested_by,created_at FROM vpn_credential_revocations `

func scan(row pgx.Row) (Preparation, error) {
	var p Preparation
	err := row.Scan(&p.ID, &p.AccountID, &p.ServerID, &p.AgentID, &p.BaselineVersionID, &p.State, &p.RequestedBy, &p.CreatedAt)
	return p, err
}

// Prepare is deliberately not connected to HTTP or Agent queues. It reserves
// and snapshots only: no status/token changes, config versions or tasks. Future
// confirmation must recheck freshness, capability and the exact preview binding.
func (s *Service) Prepare(ctx context.Context, in PrepareInput) (Preparation, error) {
	p, err := s.prepare(ctx, in)
	if err != nil {
		return Preparation{}, ErrPreflight
	}
	return p, nil
}
func (s *Service) prepare(ctx context.Context, in PrepareInput) (Preparation, error) {
	if !evidenceID.MatchString(in.AccountID) || !evidenceID.MatchString(in.ServerID) || !evidenceID.MatchString(in.BaselineVersionID) || !validRemovalHash(in.BaselineHash) || strings.TrimSpace(in.Actor) == "" || len(in.Actor) > 200 {
		return Preparation{}, ErrPreflight
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Preparation{}, err
	}
	defer tx.Rollback(ctx)
	// Same ordering as transfer admission; shared advisory lock fences Manager updates.
	var node, status, uuid string
	err = tx.QueryRow(ctx, `SELECT COALESCE(server_id::text,''),status,COALESCE(vless_uuid::text,'') FROM vpn_accounts WHERE id=$1::uuid FOR UPDATE`, in.AccountID).Scan(&node, &status, &uuid)
	if err != nil {
		return Preparation{}, err
	}
	if node != in.ServerID || (status != "active" && status != "suspended" && status != "revoked") {
		return Preparation{}, ErrPreflight
	}
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(140159)`); err != nil {
		return Preparation{}, err
	}
	// Stabilize routing reads even when no profile is attached to this node.
	if _, err = tx.Exec(ctx, `LOCK TABLE routing_profiles,routing_profile_rules,server_routing_profiles,managed_routing_rule_sets IN SHARE MODE`); err != nil {
		return Preparation{}, err
	}
	if _, err = tx.Exec(ctx, `SELECT routegate_transfer_node_guard($1::uuid)`, node); err != nil {
		return Preparation{}, err
	}
	existing, e := scan(tx.QueryRow(ctx, selection+`WHERE server_id=$1::uuid AND state='prepared'`, node))
	if e == nil {
		var hash string
		if err = tx.QueryRow(ctx, `SELECT baseline_hash FROM vpn_credential_revocations WHERE id=$1::uuid`, existing.ID).Scan(&hash); err != nil {
			return Preparation{}, err
		}
		if existing.AccountID != in.AccountID || existing.BaselineVersionID != in.BaselineVersionID || hash != in.BaselineHash || existing.RequestedBy != in.Actor {
			return Preparation{}, ErrPreflight
		}
		return existing, tx.Commit(ctx)
	}
	if !errors.Is(e, pgx.ErrNoRows) {
		return Preparation{}, e
	}
	var busy bool
	err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM config_apply_jobs WHERE server_id=$1::uuid AND status IN ('pending','in_progress'))
 OR EXISTS(SELECT 1 FROM agent_operation_jobs WHERE server_id=$1::uuid AND status IN ('pending','in_progress'))
 OR EXISTS(SELECT 1 FROM agent_platform_update_jobs WHERE server_id=$1::uuid AND status IN ('pending','in_progress','mutation_dispatched','outcome_unknown'))
 OR EXISTS(SELECT 1 FROM update_jobs WHERE operation='apply' AND status IN ('pending','running'))`, node).Scan(&busy)
	if err != nil {
		return Preparation{}, err
	}
	if busy {
		return Preparation{}, ErrPreflight
	}
	var active, agent string
	var generation int64
	var ready bool
	err = tx.QueryRow(ctx, `SELECT COALESCE(s.active_config_version_id::text,''),a.id::text,a.credential_generation,
 COALESCE(s.status='active' AND s.deployment_role IN ('vpn','hybrid') AND s.vpn_protocol='vless'
 AND a.status='online' AND a.last_seen_at>now()-interval '90 seconds'
 AND a.last_authenticated_heartbeat_at>now()-interval '90 seconds'
 AND a.last_authenticated_heartbeat_generation=a.credential_generation
 AND a.capabilities @> '{"sing-box":true,"systemctl":true,"ss":true}'::jsonb
 AND EXISTS(SELECT 1 FROM jsonb_array_elements(COALESCE(a.capabilities->'routegate'->'vpnCoreAdapters','[]'::jsonb)) c WHERE c->>'core'='sing-box' AND c->>'protocol'='vless' AND c->'transports' ? 'tcp' AND c->'securityModes' ? 'reality')
 AND (a.capabilities->'vpnCore'->>'state'='running' OR EXISTS(SELECT 1 FROM jsonb_array_elements(COALESCE(a.capabilities->'vpnCores','[]'::jsonb)) c WHERE c->>'type'='sing-box' AND c->>'state'='running')),false)
 FROM servers s JOIN agents a ON a.server_id=s.id WHERE s.id=$1::uuid FOR UPDATE OF a`, node).Scan(&active, &agent, &generation, &ready)
	if err != nil {
		return Preparation{}, ErrPreflight
	}
	if !ready || active != in.BaselineVersionID {
		return Preparation{}, ErrPreflight
	}
	repo := configs.NewTransactionRepository(tx)
	baseline, err := repo.GetConfigVersion(ctx, node, active)
	if err != nil {
		return Preparation{}, err
	}
	if baseline.ConfigHash != in.BaselineHash {
		return Preparation{}, ErrPreflight
	}
	candidate, err := configs.PrepareVLESSRemoval(baseline, configs.VLESSRemovalTarget{ServerID: node, AccountID: in.AccountID, VersionID: active, ConfigHash: in.BaselineHash, VLESSUUID: uuid})
	if err != nil {
		return Preparation{}, ErrPreflight
	}
	if err = repo.CheckRemovalDesiredBaseline(ctx, baseline, in.AccountID); err != nil {
		return Preparation{}, ErrPreflight
	}
	var old configs.RenderedConfig
	if json.Unmarshal(baseline.RenderedConfig, &old) != nil || old.Agent == nil || old.Agent.ID != agent {
		return Preparation{}, ErrPreflight
	}
	before, _ := json.Marshal(old.SingBox)
	after, _ := json.Marshal(candidate.RenderedConfig.SingBox)
	baselineRuntime, err := RuntimeDigest(before)
	if err != nil {
		return Preparation{}, ErrPreflight
	}
	candidateRuntime, err := RuntimeDigest(after)
	if err != nil {
		return Preparation{}, ErrPreflight
	}
	payload, err := json.Marshal(candidate.RenderedConfig)
	if err != nil {
		return Preparation{}, err
	}
	prepared, err := scan(tx.QueryRow(ctx, `WITH inserted AS (INSERT INTO vpn_credential_revocations
 (vpn_account_id,server_id,agent_id,agent_credential_generation,baseline_version_id,baseline_hash,baseline_runtime_hash,candidate_runtime_hash,candidate_hash,candidate_config,account_status,requested_by,last_action_by)
 VALUES($1::uuid,$2::uuid,$3::uuid,$4,$5::uuid,$6,$7,$8,$9,$10::jsonb,$11,$12,$12) RETURNING *)
 SELECT id::text,vpn_account_id::text,server_id::text,agent_id::text,baseline_version_id::text,state,requested_by,created_at FROM inserted`, in.AccountID, node, agent, generation, active, in.BaselineHash, baselineRuntime, candidateRuntime, candidate.ConfigHash, payload, status, in.Actor))
	if err != nil {
		return Preparation{}, err
	}
	return prepared, tx.Commit(ctx)
}

// Cancel releases a never-dispatched preparation. No timeout, worker restart or
// failure silently releases it. Later execution states need a different recovery
// path and cannot use this method or the schema's prepared -> cancelled transition.
func (s *Service) Cancel(ctx context.Context, id, actor string) (Preparation, error) {
	p, err := s.cancel(ctx, id, actor)
	if err != nil {
		return Preparation{}, ErrPreflight
	}
	return p, nil
}
func (s *Service) cancel(ctx context.Context, id, actor string) (Preparation, error) {
	if !evidenceID.MatchString(id) || strings.TrimSpace(actor) == "" || len(actor) > 200 {
		return Preparation{}, ErrPreflight
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Preparation{}, err
	}
	defer tx.Rollback(ctx)
	var node string
	if err = tx.QueryRow(ctx, `SELECT server_id::text FROM vpn_credential_revocations WHERE id=$1::uuid`, id).Scan(&node); err != nil {
		return Preparation{}, err
	}
	if _, err = tx.Exec(ctx, `SELECT id FROM servers WHERE id=$1::uuid FOR UPDATE`, node); err != nil {
		return Preparation{}, err
	}
	p, err := scan(tx.QueryRow(ctx, selection+`WHERE id=$1::uuid FOR UPDATE`, id))
	if err != nil {
		return Preparation{}, err
	}
	if p.State == "prepared" {
		if _, err = tx.Exec(ctx, `UPDATE vpn_credential_revocations SET state='cancelled',last_action_by=$2,cancelled_at=now() WHERE id=$1::uuid`, id, actor); err != nil {
			return Preparation{}, err
		}
		p.State = "cancelled"
	}
	return p, tx.Commit(ctx)
}
