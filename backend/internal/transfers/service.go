// Package transfers owns the durable RG-140 account migration lifecycle.
package transfers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strconv"
	"time"

	"github.com/ikaevus/routegate/backend/internal/configs"
	"github.com/ikaevus/routegate/backend/internal/platform"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var errNodeReserved = errors.New("node reserved for account transfer")

var ErrGuard = errors.New("transfer safety gate blocked the action")

type Transfer struct {
	ID                    string           `json:"id"`
	AccountID             string           `json:"accountId"`
	SourceID              string           `json:"sourceServerId"`
	TargetID              string           `json:"targetServerId"`
	SourceVersionID       string           `json:"sourceVersionId"`
	TargetVersionID       string           `json:"targetVersionId"`
	TargetJobID           string           `json:"targetJobId"`
	CleanupVersionID      string           `json:"cleanupVersionId"`
	CleanupJobID          string           `json:"cleanupJobId"`
	State                 string           `json:"state"`
	RequestedBy           string           `json:"requestedBy"`
	LastActionBy          string           `json:"lastActionBy"`
	LastError             string           `json:"lastError"`
	CreatedAt             time.Time        `json:"createdAt"`
	UpdatedAt             time.Time        `json:"updatedAt"`
	CutoverAt             *time.Time       `json:"cutoverAt"`
	CleanupAcknowledgedAt *time.Time       `json:"cleanupAcknowledgedAt"`
	CompletedAt           *time.Time       `json:"completedAt"`
	Devices               []DeviceEvidence `json:"devices"`
}

type deviceInventory struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	TokenID string `json:"tokenId"`
}

type DeviceEvidence struct {
	ID                    string     `json:"id"`
	Name                  string     `json:"name"`
	LastRequestedAt       *time.Time `json:"lastRequestedAt"`
	RequestedAfterCutover bool       `json:"requestedAfterCutover"`
	LinkChanged           bool       `json:"linkChanged"`
}

type ReachabilityCheck func(context.Context, string, int) error

type Service struct {
	pool  *pgxpool.Pool
	check ReachabilityCheck
}

func NewService(pool *pgxpool.Pool) *Service {
	return NewServiceWithCheck(pool, func(ctx context.Context, host string, port int) error {
		// This proves a TCP connection only. Agent evidence proves its local runtime;
		// actual VPN use must be verified by the operator before cleanup.
		conn, err := (&net.Dialer{Timeout: 3 * time.Second}).DialContext(ctx, "tcp", net.JoinHostPort(host, strconv.Itoa(port)))
		if err != nil {
			return errors.New("public_listener_unreachable")
		}
		return conn.Close()
	})
}

// NewServiceWithCheck allows a controlled network probe in integration tests.
func NewServiceWithCheck(pool *pgxpool.Pool, check ReachabilityCheck) *Service {
	return &Service{pool: pool, check: check}
}

const selection = `SELECT id::text, vpn_account_id::text, source_server_id::text, target_server_id::text,
 source_version_id::text, COALESCE(target_version_id::text,''), COALESCE(target_job_id::text,''),
 COALESCE(cleanup_version_id::text,''), COALESCE(cleanup_job_id::text,''), state, requested_by, last_action_by, last_error,
 created_at, updated_at, cutover_at, cleanup_acknowledged_at, completed_at FROM vpn_account_transfers `

func scan(row pgx.Row) (Transfer, error) {
	var tr Transfer
	err := row.Scan(&tr.ID, &tr.AccountID, &tr.SourceID, &tr.TargetID, &tr.SourceVersionID, &tr.TargetVersionID, &tr.TargetJobID,
		&tr.CleanupVersionID, &tr.CleanupJobID, &tr.State, &tr.RequestedBy, &tr.LastActionBy, &tr.LastError,
		&tr.CreatedAt, &tr.UpdatedAt, &tr.CutoverAt, &tr.CleanupAcknowledgedAt, &tr.CompletedAt)
	return tr, err
}

func (s *Service) Latest(ctx context.Context, accountID string) (*Transfer, error) {
	tr, err := scan(s.pool.QueryRow(ctx, selection+`WHERE vpn_account_id=$1::uuid ORDER BY created_at DESC LIMIT 1`, accountID))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	tr.Devices = []DeviceEvidence{}
	var rawInventory []byte
	if err = s.pool.QueryRow(ctx, `SELECT device_inventory FROM vpn_account_transfers WHERE id=$1::uuid`, tr.ID).Scan(&rawInventory); err != nil {
		return nil, err
	}
	var inventory []deviceInventory
	if err = json.Unmarshal(rawInventory, &inventory); err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	for _, old := range inventory {
		seen[old.ID] = true
		d := DeviceEvidence{ID: old.ID, Name: old.Name}
		var tokenID string
		err = s.pool.QueryRow(ctx, `SELECT id::text,last_used_at FROM vpn_subscription_tokens WHERE vpn_account_id=$1::uuid AND status='active'
   AND (COALESCE(device_id::text,'legacy')=$2) ORDER BY created_at DESC LIMIT 1`, accountID, old.ID).Scan(&tokenID, &d.LastRequestedAt)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return nil, err
		}
		d.LinkChanged = tokenID != old.TokenID
		d.RequestedAfterCutover = !d.LinkChanged && tr.CutoverAt != nil && d.LastRequestedAt != nil && !d.LastRequestedAt.Before(*tr.CutoverAt)
		tr.Devices = append(tr.Devices, d)
	}
	rows, err := s.pool.Query(ctx, `SELECT COALESCE(d.id::text,'legacy'),COALESCE(d.name,'Account subscription'),t.last_used_at FROM vpn_subscription_tokens t LEFT JOIN vpn_account_devices d ON d.id=t.device_id WHERE t.vpn_account_id=$1::uuid AND t.status='active' AND (d.id IS NULL OR d.status='active')`, accountID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var d DeviceEvidence
		if err = rows.Scan(&d.ID, &d.Name, &d.LastRequestedAt); err != nil {
			return nil, err
		}
		if seen[d.ID] {
			continue
		}
		d.RequestedAfterCutover = tr.CutoverAt != nil && d.LastRequestedAt != nil && !d.LastRequestedAt.Before(*tr.CutoverAt)
		tr.Devices = append(tr.Devices, d)
	}
	return &tr, rows.Err()
}

func guard(reason string) error { return fmt.Errorf("%w: %s", ErrGuard, reason) }

// Start atomically reserves both nodes, renders staged membership, pins the
// target version and queues its apply. Assignment and tokens remain unchanged.
func (s *Service) Start(ctx context.Context, accountID, targetID, actor string) (Transfer, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Transfer{}, err
	}
	defer tx.Rollback(ctx)
	tr, err := s.StartInTransaction(ctx, tx, accountID, targetID, actor)
	if err != nil {
		return tr, err
	}
	if err = tx.Commit(ctx); err != nil {
		return tr, err
	}
	return tr, nil
}

// StartInTransaction lets Automatic Selection keep its policy/candidate locks
// until the staging operation is queued, with no stale decision gap.
func (s *Service) StartInTransaction(ctx context.Context, tx pgx.Tx, accountID, targetID, actor string) (Transfer, error) {
	var err error
	var sourceID, status, sourceVersion string
	if err = tx.QueryRow(ctx, `SELECT COALESCE(server_id::text,''),status FROM vpn_accounts WHERE id=$1::uuid FOR UPDATE`, accountID).Scan(&sourceID, &status); err != nil {
		return Transfer{}, err
	}
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(140159)`); err != nil {
		return Transfer{}, err
	}
	existing, e := scan(tx.QueryRow(ctx, selection+`WHERE vpn_account_id=$1::uuid AND completed_at IS NULL`, accountID))
	if e == nil {
		if existing.TargetID == targetID {
			return existing, nil
		}
		return Transfer{}, guard("account_has_active_transfer")
	}
	if !errors.Is(e, pgx.ErrNoRows) {
		return Transfer{}, e
	}
	if sourceID == "" || targetID == "" || sourceID == targetID || status != "active" {
		return Transfer{}, guard("distinct_nodes_and_active_account_required")
	}
	if err = accountEligible(ctx, tx, accountID); err != nil {
		return Transfer{}, err
	}
	if err = lockNodes(ctx, tx, sourceID, targetID); err != nil {
		return Transfer{}, err
	}
	var reserved, busy bool
	err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM vpn_account_transfer_nodes WHERE server_id IN ($1::uuid,$2::uuid)),
 EXISTS(SELECT 1 FROM config_apply_jobs WHERE server_id IN ($1::uuid,$2::uuid) AND status IN ('pending','in_progress'))
 OR EXISTS(SELECT 1 FROM agent_operation_jobs WHERE server_id IN ($1::uuid,$2::uuid) AND status IN ('pending','in_progress'))
 OR EXISTS(SELECT 1 FROM agent_platform_update_jobs WHERE server_id IN ($1::uuid,$2::uuid) AND status IN ('pending','in_progress','mutation_dispatched','outcome_unknown'))
 OR EXISTS(SELECT 1 FROM update_jobs WHERE operation='apply' AND status IN ('pending','running'))`, sourceID, targetID).Scan(&reserved, &busy)
	if err != nil {
		return Transfer{}, err
	}
	if reserved {
		return Transfer{}, fmt.Errorf("%w: %w", ErrGuard, errNodeReserved)
	}
	if busy {
		return Transfer{}, guard("node_has_conflicting_operation")
	}
	var compatible bool
	err = tx.QueryRow(ctx, `SELECT
  a.vless_uuid IS NOT NULL AND a.config_updated_at<=cv.created_at
  AND COALESCE(NULLIF(cp.protocol,'auto'),'vless')='vless'
  AND NOT EXISTS(SELECT 1 FROM vpn_account_protocols p WHERE p.vpn_account_id=a.id AND p.desired_explicit AND p.desired_enabled AND p.protocol<>'vless')
  AND NOT EXISTS(SELECT 1 FROM vpn_account_protocols p WHERE p.vpn_account_id=a.id AND p.desired_explicit AND p.updated_at>cv.created_at)
 FROM vpn_accounts a JOIN servers s ON s.id=a.server_id JOIN config_versions cv ON cv.id=s.active_config_version_id
 LEFT JOIN vpn_client_profiles cp ON cp.vpn_account_id=a.id WHERE a.id=$1::uuid`, accountID).Scan(&compatible)
	if err != nil {
		return Transfer{}, guard("source_not_applied")
	}
	if !compatible {
		return Transfer{}, guard("only_applied_vless_accounts_supported")
	}
	if err = tx.QueryRow(ctx, `SELECT active_config_version_id::text FROM servers WHERE id=$1::uuid`, sourceID).Scan(&sourceVersion); err != nil {
		return Transfer{}, err
	}
	if err = s.nodeReady(ctx, tx, sourceID); err != nil {
		return Transfer{}, err
	}
	if err = s.nodeReady(ctx, tx, targetID); err != nil {
		return Transfer{}, err
	}
	// Seed the ordinary profile before node reservations; subsequent refreshes
	// perform only a no-op UPSERT and can continue while a transfer is pending.
	if _, err = tx.Exec(ctx, `INSERT INTO vpn_client_profiles(vpn_account_id,active_protocol) VALUES($1::uuid,'vless') ON CONFLICT(vpn_account_id) DO NOTHING`, accountID); err != nil {
		return Transfer{}, err
	}
	if err = membership(ctx, tx, sourceID, sourceVersion, accountID, true); err != nil {
		return Transfer{}, err
	}
	var sourceJob string
	if err = tx.QueryRow(ctx, `SELECT id::text FROM config_apply_jobs WHERE server_id=$1::uuid AND config_version_id=$2::uuid AND status='succeeded' AND action='apply' ORDER BY completed_at DESC LIMIT 1`, sourceID, sourceVersion).Scan(&sourceJob); err != nil {
		return Transfer{}, guard("source_apply_proof_missing")
	}
	if _, _, err = appliedProof(ctx, tx, sourceID, sourceVersion, sourceJob); err != nil {
		return Transfer{}, err
	}
	if _, err = tx.Exec(ctx, `LOCK TABLE routing_profiles,routing_profile_rules,server_routing_profiles IN SHARE MODE`); err != nil {
		return Transfer{}, err
	}
	if err = checkBaseline(ctx, tx, sourceID); err != nil {
		return Transfer{}, err
	}
	if err = checkBaseline(ctx, tx, targetID); err != nil {
		return Transfer{}, err
	}
	tr, err := scan(tx.QueryRow(ctx, `INSERT INTO vpn_account_transfers(vpn_account_id,source_server_id,target_server_id,source_version_id,state,requested_by,last_action_by)
 VALUES($1::uuid,$2::uuid,$3::uuid,$4::uuid,'preparing',$5,$5) RETURNING `+selection[len("SELECT "):len(selection)-len(" FROM vpn_account_transfers ")], accountID, sourceID, targetID, sourceVersion, actor))
	if err != nil {
		return Transfer{}, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO vpn_account_transfer_nodes(server_id,transfer_id) VALUES($1::uuid,$3::uuid),($2::uuid,$3::uuid)`, sourceID, targetID, tr.ID); err != nil {
		return Transfer{}, err
	}
	if _, err = tx.Exec(ctx, `UPDATE vpn_account_transfers SET device_inventory=COALESCE((SELECT jsonb_agg(jsonb_build_object('id',COALESCE(d.id::text,'legacy'),'name',COALESCE(d.name,'Account subscription'),'tokenId',t.id::text)) FROM vpn_subscription_tokens t LEFT JOIN vpn_account_devices d ON d.id=t.device_id WHERE t.vpn_account_id=$2::uuid AND t.status='active' AND (d.id IS NULL OR d.status='active')),'[]'::jsonb) WHERE id=$1::uuid`, tr.ID, accountID); err != nil {
		return Transfer{}, err
	}
	if err = authorize(ctx, tx, tr.ID); err != nil {
		return Transfer{}, err
	}
	if _, err = tx.Exec(ctx, `UPDATE config_versions SET pinned=true WHERE id=$1::uuid`, sourceVersion); err != nil {
		return Transfer{}, err
	}
	if err = queue(ctx, tx, &tr, targetID, false); err != nil {
		return Transfer{}, err
	}
	return tr, nil
}

func lockNodes(ctx context.Context, tx pgx.Tx, source, target string) error {
	rows, err := tx.Query(ctx, `SELECT id FROM servers WHERE id IN ($1::uuid,$2::uuid) ORDER BY id FOR UPDATE`, source, target)
	if err != nil {
		return err
	}
	defer rows.Close()
	n := 0
	for rows.Next() {
		n++
	}
	if err = rows.Err(); err != nil {
		return err
	}
	if n != 2 {
		return guard("node_not_found")
	}
	return nil
}
func authorize(ctx context.Context, tx pgx.Tx, id string) error {
	_, err := tx.Exec(ctx, `SELECT set_config('routegate.transfer_id',$1,true)`, id)
	return err
}

func (s *Service) nodeReady(ctx context.Context, tx pgx.Tx, id string) error {
	var ready bool
	err := tx.QueryRow(ctx, `SELECT s.status='active' AND s.deployment_role IN ('hybrid','vpn') AND s.vpn_protocol='vless'
 AND s.reality_private_key IS NOT NULL AND s.reality_public_key IS NOT NULL
 AND a.status='online' AND a.last_seen_at>now()-interval '90 seconds'
 AND a.last_authenticated_heartbeat_at>now()-interval '90 seconds'
 AND a.last_authenticated_heartbeat_generation=a.credential_generation
 AND a.capabilities @> '{"sing-box":true,"systemctl":true,"ss":true}'::jsonb
 AND (a.capabilities->'vpnCore'->>'state'='running' OR EXISTS(SELECT 1 FROM jsonb_array_elements(COALESCE(a.capabilities->'vpnCores','[]'::jsonb)) c WHERE c->>'type'='sing-box' AND c->>'state'='running'))
 AND EXISTS(SELECT 1 FROM jsonb_array_elements(COALESCE(a.capabilities->'routegate'->'vpnCoreAdapters','[]'::jsonb)) c WHERE c->>'core'='sing-box' AND c->>'protocol'='vless' AND c->'transports' ? 'tcp' AND c->'securityModes' ? 'reality')
 FROM servers s JOIN agents a ON a.server_id=s.id WHERE s.id=$1::uuid`, id).Scan(&ready)
	if err != nil || !ready {
		return guard("node_role_protocol_or_agent_not_ready")
	}
	return nil
}

func queue(ctx context.Context, tx pgx.Tx, tr *Transfer, node string, cleanup bool) error {
	svc := configs.NewService(configs.NewTransactionRepository(tx))
	var rendered configs.RenderConfigResponse
	var err error
	if cleanup {
		rendered, err = svc.RenderTransferCleanup(ctx, node, tr.ID)
	} else {
		rendered, err = svc.Render(ctx, node)
	}
	if err != nil {
		return err
	}
	if !rendered.ValidationResult.Valid {
		return guard("render_validation_failed")
	}
	validated, err := svc.Validate(ctx, node, rendered.ConfigVersion.ID)
	if err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `UPDATE config_versions SET pinned=true WHERE id=$1::uuid`, validated.ConfigVersion.ID); err != nil {
		return err
	}
	applied, err := svc.Apply(ctx, node, validated.ConfigVersion.ID, configs.ApplyConfigRequest{Comment: "RG-140 transfer " + tr.ID})
	if err != nil {
		return err
	}
	if cleanup {
		tr.CleanupVersionID = validated.ConfigVersion.ID
		tr.CleanupJobID = applied.Job.ID
		_, err = tx.Exec(ctx, `UPDATE vpn_account_transfers SET cleanup_version_id=$2::uuid,cleanup_job_id=$3::uuid,updated_at=now(),last_error='' WHERE id=$1::uuid`, tr.ID, tr.CleanupVersionID, tr.CleanupJobID)
	} else {
		tr.TargetVersionID = validated.ConfigVersion.ID
		tr.TargetJobID = applied.Job.ID
		tr.State = "target_applying"
		_, err = tx.Exec(ctx, `UPDATE vpn_account_transfers SET target_version_id=$2::uuid,target_job_id=$3::uuid,state='target_applying',updated_at=now(),last_error='' WHERE id=$1::uuid`, tr.ID, tr.TargetVersionID, tr.TargetJobID)
	}
	return err
}

func membership(ctx context.Context, tx pgx.Tx, node, version, account string, present bool) error {
	var current string
	var payload []byte
	err := tx.QueryRow(ctx, `SELECT COALESCE(s.active_config_version_id::text,''),cv.client_settings FROM servers s JOIN config_versions cv ON cv.id=$2::uuid AND cv.server_id=s.id WHERE s.id=$1::uuid`, node, version).Scan(&current, &payload)
	if err != nil || current != version {
		return guard("applied_version_changed")
	}
	var settings platform.AppliedClientSettings
	if json.Unmarshal(payload, &settings) != nil || settings.Accounts == nil {
		return guard("deployed_membership_unknown")
	}
	a, exists := settings.Accounts[account]
	if present {
		if !exists || len(a.Protocols) != 1 || a.Protocols[0] != "vless" || settings.RealityPublicKey == "" {
			return guard("applied_account_or_protocol_missing")
		}
	} else if exists {
		return guard("cleanup_still_contains_account")
	}
	return nil
}

// appliedProof rejects skipped service control and incomplete local listener
// evidence. It trusts only the job associated with this operation and version.
func appliedProof(ctx context.Context, tx pgx.Tx, node, version, job string) (string, int, error) {
	var status, host, hash string
	var result, settingsJSON []byte
	var applied bool
	err := tx.QueryRow(ctx, `SELECT j.status,COALESCE(host(s.public_ip),NULLIF(s.hostname,''),''),cv.config_hash,j.result_payload,cv.client_settings,
 s.active_config_version_id=cv.id AND cv.applied_at IS NOT NULL AND j.completed_at IS NOT NULL
 FROM config_apply_jobs j JOIN config_versions cv ON cv.id=j.config_version_id JOIN servers s ON s.id=j.server_id
 WHERE j.id=$1::uuid AND j.server_id=$2::uuid AND j.config_version_id=$3::uuid AND j.action='apply' AND j.agent_id IS NOT NULL`, job, node, version).Scan(&status, &host, &hash, &result, &settingsJSON, &applied)
	if err != nil {
		return "", 0, guard("apply_proof_missing")
	}
	if status == "pending" || status == "in_progress" {
		return "", 0, guard("apply_in_progress")
	}
	if status != "succeeded" || !applied {
		return "", 0, guard("apply_failed_or_version_changed")
	}
	var report struct {
		Stage      string `json:"stage"`
		Components []struct {
			Core         string `json:"core"`
			Protocol     string `json:"protocol"`
			Status       string `json:"status"`
			ListenerPort int    `json:"listenerPort"`
		} `json:"components"`
	}
	var settings platform.AppliedClientSettings
	if json.Unmarshal(result, &report) != nil || json.Unmarshal(settingsJSON, &settings) != nil || report.Stage != "succeeded" {
		return "", 0, guard("runtime_evidence_missing")
	}
	for _, c := range report.Components {
		if c.Core == "sing-box" && c.Protocol == "vless" && c.Status == "succeeded" && c.ListenerPort == settings.VLESSPort && c.ListenerPort > 0 && host != "" {
			return host, c.ListenerPort, nil
		}
	}
	return "", 0, guard("runtime_listener_not_verified")
}

// Act performs one guarded transition. Repeating a finished step never queues
// duplicate work; pending tasks retain the reservation across Manager restarts.
func (s *Service) Act(ctx context.Context, accountID, id, action, actor string, confirmed bool) (Transfer, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Transfer{}, err
	}
	defer tx.Rollback(ctx)
	// Match Start's lock order: account, ordered nodes, operation.
	var source, target string
	if err = tx.QueryRow(ctx, `SELECT source_server_id::text,target_server_id::text FROM vpn_account_transfers WHERE id=$1::uuid AND vpn_account_id=$2::uuid`, id, accountID).Scan(&source, &target); err != nil {
		return Transfer{}, err
	}
	var accountNode string
	if err = tx.QueryRow(ctx, `SELECT COALESCE(server_id::text,'') FROM vpn_accounts WHERE id=$1::uuid FOR UPDATE`, accountID).Scan(&accountNode); err != nil {
		return Transfer{}, err
	}
	if err = lockNodes(ctx, tx, source, target); err != nil {
		return Transfer{}, err
	}
	tr, err := scan(tx.QueryRow(ctx, selection+`WHERE id=$1::uuid FOR UPDATE`, id))
	if err != nil {
		return Transfer{}, err
	}
	if tr.CompletedAt != nil {
		return tr, nil
	}
	if err = authorize(ctx, tx, id); err != nil {
		return Transfer{}, err
	}
	before := tr
	if _, err = tx.Exec(ctx, `SAVEPOINT transfer_step`); err != nil {
		return tr, err
	}
	switch action {
	case "verify":
		if tr.State == "target_ready" || tr.State == "client_refresh_pending" {
			break
		}
		if tr.State != "target_applying" {
			return tr, guard("target_is_not_applying")
		}
		host, port, e := appliedProof(ctx, tx, target, tr.TargetVersionID, tr.TargetJobID)
		err = e
		if err == nil {
			err = accountEligible(ctx, tx, accountID)
		}
		if err == nil {
			err = membership(ctx, tx, target, tr.TargetVersionID, accountID, true)
		}
		if err == nil {
			err = s.nodeReady(ctx, tx, target)
		}
		if err == nil {
			err = s.reachable(ctx, host, port)
		}
		if err == nil {
			tr.State = "target_ready"
		}
	case "cutover":
		if tr.State == "client_refresh_pending" {
			break
		}
		if tr.State != "target_ready" || accountNode != source {
			return tr, guard("target_not_ready_or_assignment_changed")
		}
		host, port, e := appliedProof(ctx, tx, target, tr.TargetVersionID, tr.TargetJobID)
		err = e
		if err == nil {
			err = accountEligible(ctx, tx, accountID)
		}
		if err == nil {
			err = membership(ctx, tx, target, tr.TargetVersionID, accountID, true)
		}
		if err == nil {
			err = membership(ctx, tx, source, tr.SourceVersionID, accountID, true)
		}
		if err == nil {
			err = s.nodeReady(ctx, tx, target)
		}
		if err == nil {
			err = s.reachable(ctx, host, port)
		}
		if err == nil {
			_, err = tx.Exec(ctx, `UPDATE vpn_accounts SET server_id=$2::uuid,updated_at=now() WHERE id=$1::uuid`, accountID, target)
			if err == nil {
				tr.State = "client_refresh_pending"
				_, err = tx.Exec(ctx, `UPDATE vpn_account_transfers SET cutover_at=now() WHERE id=$1::uuid`, id)
			}
		}
	case "cleanup":
		if tr.State == "source_cleaning" {
			break
		}
		if tr.State != "client_refresh_pending" || !confirmed || accountNode != target {
			return tr, guard("explicit_client_verification_required")
		}
		if err = membership(ctx, tx, target, tr.TargetVersionID, accountID, true); err != nil {
			break
		}
		if _, _, err = appliedProof(ctx, tx, target, tr.TargetVersionID, tr.TargetJobID); err != nil {
			break
		}
		if err = s.nodeReady(ctx, tx, target); err != nil {
			break
		}
		tr.State = "source_cleaning"
		if _, err = tx.Exec(ctx, `UPDATE vpn_account_transfers SET state=$2,cleanup_acknowledged_at=now() WHERE id=$1::uuid`, id, tr.State); err != nil {
			break
		}
		err = queue(ctx, tx, &tr, source, true)
	case "rollback":
		if tr.State == "target_cleaning" {
			break
		}
		if tr.State != "client_refresh_pending" && tr.State != "target_ready" && tr.State != "target_applying" {
			return tr, guard("rollback_unavailable_after_source_cleanup_started")
		}
		if tr.State == "client_refresh_pending" && !confirmed {
			return tr, guard("rollback_requires_client_refresh_acknowledgement")
		}
		var taskStatus string
		if err = tx.QueryRow(ctx, `SELECT status FROM config_apply_jobs WHERE id=$1::uuid FOR UPDATE`, tr.TargetJobID).Scan(&taskStatus); err != nil {
			break
		}
		if taskStatus == "in_progress" {
			return tr, guard("wait_for_target_apply_before_rollback")
		}
		if taskStatus == "pending" {
			if _, err = tx.Exec(ctx, `UPDATE config_apply_jobs SET status='failed',error_message='Cancelled before Agent claim',completed_at=now(),updated_at=now() WHERE id=$1::uuid AND status='pending'`, tr.TargetJobID); err != nil {
				break
			}
		}
		if err = membership(ctx, tx, source, tr.SourceVersionID, accountID, true); err != nil {
			break
		}
		if err = s.nodeReady(ctx, tx, source); err != nil {
			break
		}
		var host string
		var port int
		if err = tx.QueryRow(ctx, `SELECT COALESCE(host(s.public_ip),NULLIF(s.hostname,''),''),(cv.client_settings->>'vlessPort')::int FROM servers s JOIN config_versions cv ON cv.id=s.active_config_version_id WHERE s.id=$1::uuid`, source).Scan(&host, &port); err != nil {
			break
		}
		if err = s.reachable(ctx, host, port); err != nil {
			break
		}
		if _, err = tx.Exec(ctx, `UPDATE vpn_accounts SET server_id=$2::uuid,updated_at=now() WHERE id=$1::uuid`, accountID, source); err != nil {
			break
		}
		tr.State = "target_cleaning"
		if _, err = tx.Exec(ctx, `UPDATE vpn_account_transfers SET state=$2 WHERE id=$1::uuid`, id, tr.State); err != nil {
			break
		}
		err = queue(ctx, tx, &tr, target, true)
	case "finish":
		if tr.State != "source_cleaning" && tr.State != "target_cleaning" {
			return tr, guard("cleanup_not_started")
		}
		node := source
		if tr.State == "target_cleaning" {
			node = target
		}
		if _, _, err = appliedProof(ctx, tx, node, tr.CleanupVersionID, tr.CleanupJobID); err != nil {
			break
		}
		if err = s.nodeReady(ctx, tx, node); err != nil {
			break
		}
		if err = membership(ctx, tx, node, tr.CleanupVersionID, accountID, false); err != nil {
			break
		}
		if tr.State == "source_cleaning" {
			tr.State = "complete"
		} else if tr.CutoverAt != nil {
			tr.State = "rolled_back"
		} else {
			tr.State = "cancelled"
		}
		if _, err = tx.Exec(ctx, `UPDATE vpn_account_transfers SET state=$2,completed_at=now() WHERE id=$1::uuid`, id, tr.State); err != nil {
			break
		}
		_, err = tx.Exec(ctx, `DELETE FROM vpn_account_transfer_nodes WHERE transfer_id=$1::uuid`, id)
	case "retry":
		job := tr.TargetJobID
		if tr.State == "source_cleaning" || tr.State == "target_cleaning" {
			job = tr.CleanupJobID
		} else if tr.State != "target_applying" {
			return tr, guard("nothing_to_retry")
		}
		var failed bool
		if err = tx.QueryRow(ctx, `SELECT status='failed' FROM config_apply_jobs WHERE id=$1::uuid`, job).Scan(&failed); err != nil {
			break
		}
		if !failed {
			return tr, guard("retry_requires_terminal_failed_job")
		}
		node := target
		cleanup := false
		if tr.State == "source_cleaning" {
			node = source
			cleanup = true
		} else if tr.State == "target_cleaning" {
			cleanup = true
		}
		err = queue(ctx, tx, &tr, node, cleanup)
	default:
		return tr, guard("unknown_transfer_action")
	}
	// Persist only safe, fixed error codes. SQL failures roll the whole step back;
	// they must never commit half a cutover or a state with no associated job.
	if err != nil {
		if _, e := tx.Exec(ctx, `ROLLBACK TO SAVEPOINT transfer_step`); e != nil {
			return tr, e
		}
		tr = before
		if errors.Is(err, ErrGuard) {
			tr.LastError = err.Error()
		} else {
			tr.LastError = "step_failed_safely"
		}
	} else {
		tr.LastError = ""
	}
	if _, e := tx.Exec(ctx, `UPDATE vpn_account_transfers SET state=$2,last_error=$3,last_action_by=$4,updated_at=now() WHERE id=$1::uuid`, id, tr.State, tr.LastError, actor); e != nil {
		return tr, e
	}
	fresh, readErr := scan(tx.QueryRow(ctx, selection+`WHERE id=$1::uuid`, id))
	if readErr != nil {
		return tr, readErr
	}
	tr = fresh
	if e := tx.Commit(ctx); e != nil {
		return tr, e
	}
	return tr, err
}

func (s *Service) reachable(ctx context.Context, host string, port int) error {
	if s.check == nil || s.check(ctx, host, port) != nil {
		return guard("public_listener_unreachable")
	}
	return nil
}

// Detect saved-but-unapplied changes before staging; applying a transfer must
// not silently deploy another account's pending edit. Ignore volatile Agent
// telemetry only; compare runtime, membership, keys and endpoint fields.
func checkBaseline(ctx context.Context, tx pgx.Tx, node string) error {
	var active string
	var oldPayload []byte
	var n int
	var settingsApplied bool
	err := tx.QueryRow(ctx, `SELECT COALESCE(s.active_config_version_id::text,''),cv.rendered_config,(SELECT count(*) FROM vpn_accounts a WHERE a.server_id=s.id),COALESCE(s.protocol_updated_at<=cv.created_at,TRUE) FROM servers s LEFT JOIN config_versions cv ON cv.id=s.active_config_version_id WHERE s.id=$1::uuid`, node).Scan(&active, &oldPayload, &n, &settingsApplied)
	if err != nil {
		return err
	}
	if !settingsApplied {
		return guard("node_has_pending_configuration_changes")
	}
	if active == "" && n > 0 {
		return guard("node_has_unapplied_accounts")
	}
	rendered, err := configs.NewService(configs.NewTransactionRepository(tx)).Render(ctx, node)
	if err != nil {
		return err
	}
	if active == "" {
		return nil
	}
	if !configs.TransferBaselinesEquivalent(oldPayload, rendered.ConfigVersion.RenderedConfig) {
		return guard("node_has_pending_configuration_changes")
	}
	return nil
}

func accountEligible(ctx context.Context, tx pgx.Tx, id string) error {
	var eligible bool
	if _, err := tx.Exec(ctx, `SELECT vpn_account_id FROM traffic_limits WHERE vpn_account_id=$1::uuid FOR UPDATE`, id); err != nil {
		return err
	}
	err := tx.QueryRow(ctx, `SELECT a.status='active' AND (a.expires_at IS NULL OR a.expires_at>now()) AND COALESCE(t.enforcement_status,'')<>'over_limit' FROM vpn_accounts a LEFT JOIN traffic_limits t ON t.vpn_account_id=a.id WHERE a.id=$1::uuid`, id).Scan(&eligible)
	if err != nil {
		return err
	}
	if !eligible {
		return guard("account_not_active_or_over_limit")
	}
	return nil
}
