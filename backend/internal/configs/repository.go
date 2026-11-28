package configs

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	wgcredentials "github.com/ikaevus/routegate/backend/internal/wireguard"
)

// Membership is additive until the explicitly acknowledged cleanup side is
// rendered. The canonical account assignment remains the subscription source.
const accountPlacementSQL = `(a.server_id = $1::uuid OR EXISTS (
 SELECT 1 FROM vpn_account_transfers tr
 WHERE tr.vpn_account_id = a.id AND tr.completed_at IS NULL
 AND (($1::uuid = tr.target_server_id AND tr.state IN ('preparing','target_applying','target_ready','client_refresh_pending','source_cleaning'))
 OR ($1::uuid = tr.source_server_id AND tr.state IN ('preparing','target_applying','target_ready','client_refresh_pending','target_cleaning')))
 ))`

type repositoryDB interface {
	Begin(context.Context) (pgx.Tx, error)
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
	Query(context.Context, string, ...any) (pgx.Rows, error)
	QueryRow(context.Context, string, ...any) pgx.Row
}

type Repository struct{ pool repositoryDB }

// NewTransactionRepository keeps rendering and job creation inside the transfer
// transaction. Nested Begin calls are savepoints, not independently committed work.
func NewTransactionRepository(tx pgx.Tx) *Repository { return &Repository{pool: tx} }

func NewRepository(pool *pgxpool.Pool) *Repository {
	return &Repository{pool: pool}
}

func (r *Repository) GetServerConfigInfo(ctx context.Context, serverID string) (ServerConfigInfo, error) {
	return r.getServerConfigInfo(ctx, serverID, true)
}

func (r *Repository) getServerConfigInfo(ctx context.Context, serverID string, ensureCredentials bool) (ServerConfigInfo, error) {
	info, err := scanServerConfigInfo(r.pool.QueryRow(ctx, `
		SELECT
			s.id::text,
			s.name,
			s.deployment_role,
			COALESCE(s.hostname, ''),
			COALESCE(s.public_ip::text, ''),
			COALESCE(s.private_ip::text, ''),
			COALESCE(s.location, ''),
			COALESCE(s.provider, ''),
			s.status,
			COALESCE(s.vless_port, 443),
			COALESCE(s.vless_flow, ''),
			COALESCE(s.vless_network, 'tcp'),
			COALESCE(s.reality_private_key, ''),
			COALESCE(s.reality_public_key, ''),
			COALESCE(s.reality_short_id, ''),
			COALESCE(s.reality_server_name, ''),
			s.vpn_protocol,
			s.wireguard_port,
			s.wireguard_address::text,
			s.wireguard_dns::text,
			COALESCE(s.wireguard_private_key, ''),
			COALESCE(s.wireguard_public_key, ''),
			s.hysteria2_port,
			s.hysteria2_domain,
			s.hysteria2_acme_email,
			s.hysteria2_masquerade_url,
			s.shadowsocks_port,
			s.shadowsocks_method,
			s.shadowsocks_server_key,
			s.mtproto_port,
			s.mtproto_secret,
			s.mtproto_fronting_domain,
			a.id::text,
			COALESCE(a.hostname, ''),
			COALESCE(a.os, ''),
			COALESCE(a.arch, ''),
			COALESCE(a.agent_version, ''),
			COALESCE(a.status, ''),
			a.capabilities
		FROM servers s
		LEFT JOIN agents a ON a.server_id = s.id
		WHERE s.id = $1::uuid
	`, serverID))
	if err != nil {
		return ServerConfigInfo{}, err
	}
	if ensureCredentials && info.VPNProtocol == "wireguard" {
		if err := wgcredentials.EnsureServerPeerCredentials(ctx, r.pool, serverID); err != nil {
			return ServerConfigInfo{}, err
		}
	}

	accounts, err := r.listServerVPNAccounts(ctx, serverID)
	if err != nil {
		return ServerConfigInfo{}, err
	}
	info.VPNAccounts = accounts

	profile, err := r.getServerRoutingProfile(ctx, serverID)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return ServerConfigInfo{}, err
	}
	if err == nil {
		info.RoutingProfile = &profile
	}

	return info, nil
}

func (r *Repository) listServerVPNAccounts(ctx context.Context, serverID string) ([]VPNAccountConfigInfo, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT
			a.id::text,
			a.display_name,
			a.status,
			COALESCE(a.vless_uuid::text, ''),
			COALESCE(s.vless_flow, ''),
			COALESCE(s.vless_network, 'tcp'),
			COALESCE(a.wireguard_public_key, ''),
			COALESCE(a.wireguard_address::text, ''),
			a.hysteria2_password,
			a.shadowsocks_user_key,
			COALESCE(tl.enforcement_status, 'not_enforced')
		FROM vpn_accounts a
		LEFT JOIN servers s ON s.id = $1::uuid
		LEFT JOIN traffic_limits tl ON tl.vpn_account_id = a.id
		WHERE `+accountPlacementSQL+`
		ORDER BY a.created_at ASC
	`, serverID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	accounts := make([]VPNAccountConfigInfo, 0)
	for rows.Next() {
		account, err := scanVPNAccountConfigInfo(rows)
		if err != nil {
			return nil, err
		}
		accounts = append(accounts, account)
	}
	return accounts, rows.Err()
}

func (r *Repository) getServerRoutingProfile(ctx context.Context, serverID string) (RoutingProfileConfigInfo, error) {
	profile, err := scanRoutingProfileConfigInfo(r.pool.QueryRow(ctx, `
		SELECT
			p.id::text,
			p.name,
			COALESCE(p.description, ''),
			p.is_default
		FROM routing_profiles p
		WHERE p.id = COALESCE(
			(
				SELECT srp.routing_profile_id
				FROM server_routing_profiles srp
				WHERE srp.server_id = $1::uuid
			),
			(
				SELECT rp.id
				FROM routing_profiles rp
				WHERE rp.is_default = TRUE
				ORDER BY rp.created_at ASC
				LIMIT 1
			)
		)
		LIMIT 1
	`, serverID))
	if err != nil {
		return RoutingProfileConfigInfo{}, err
	}

	rules, err := r.listRoutingProfileRules(ctx, profile.ID)
	if err != nil {
		return RoutingProfileConfigInfo{}, err
	}
	profile.Rules = rules
	return profile, nil
}

func (r *Repository) listRoutingProfileRules(ctx context.Context, profileID string) ([]RoutingProfileRuleConfigInfo, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT
			id::text,
			name,
			priority,
			action,
			domains,
			domain_suffixes,
			domain_keywords,
			ip_cidrs,
			geo_sites,
			geo_ips
		FROM routing_profile_rules
		WHERE routing_profile_id = $1::uuid
		  AND enabled = TRUE
		ORDER BY priority ASC, created_at ASC
	`, profileID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	rules := make([]RoutingProfileRuleConfigInfo, 0)
	for rows.Next() {
		rule, err := scanRoutingProfileRuleConfigInfo(rows)
		if err != nil {
			return nil, err
		}
		rules = append(rules, rule)
	}
	return rules, rows.Err()
}

func (r *Repository) CreateConfigVersion(ctx context.Context, input CreateConfigVersionInput) (ConfigVersion, error) {
	configBytes, err := json.Marshal(input.RenderedConfig)
	if err != nil {
		return ConfigVersion{}, err
	}
	// A render that cannot yield client settings (for example a version that
	// failed validation) is stored without a snapshot and never becomes the
	// source of client material; see clientSettingsFromRenderedJSON.
	clientSettingsBytes := clientSettingsSnapshot(configBytes, input.AccountPrimaryProtocols)

	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return ConfigVersion{}, err
	}
	defer tx.Rollback(ctx)

	var nextVersion int
	if err := tx.QueryRow(ctx, `SELECT next_config_version FROM servers WHERE id = $1::uuid FOR UPDATE`, input.ServerID).Scan(&nextVersion); err != nil {
		return ConfigVersion{}, err
	}

	latest, latestErr := scanConfigVersion(tx.QueryRow(ctx, `
		SELECT
			id::text,
			server_id::text,
			version,
			config_hash,
			status,
			rendered_config,
			created_at,
			applied_at,
			pinned
		FROM config_versions
		WHERE server_id = $1::uuid
		ORDER BY version DESC
		LIMIT 1
	`, input.ServerID))
	if latestErr == nil {
		equivalent, err := renderedConfigsEquivalentForVersioning(latest.RenderedConfig, configBytes)
		if err != nil {
			return ConfigVersion{}, err
		}
		if equivalent {
			// An identical config can be rendered after a protocol preference was
			// saved again. The apply trigger only activates preferences whose
			// updated_at is no later than the version's created_at. Reusing an
			// older version would leave a successfully applied set pending.
			var newerPreference bool
			if err := tx.QueryRow(ctx, `
				SELECT EXISTS (
					SELECT 1 FROM vpn_accounts a
					LEFT JOIN vpn_client_profiles cp ON cp.vpn_account_id = a.id
					WHERE `+accountPlacementSQL+`
					  AND (cp.updated_at > $2 OR EXISTS (
						SELECT 1 FROM vpn_account_protocols pap
						WHERE pap.vpn_account_id = a.id AND pap.updated_at > $2
					  ))
				) OR EXISTS (
					SELECT 1 FROM servers s
					WHERE s.id = $1::uuid AND s.protocol_updated_at > $2
				)
			`, input.ServerID, latest.CreatedAt).Scan(&newerPreference); err != nil {
				return ConfigVersion{}, err
			}
			if !newerPreference {
				// An equivalent render deploys exactly the same node runtime;
				// make sure the reused version carries its own snapshot.
				if snapshot := clientSettingsSnapshot(latest.RenderedConfig, input.AccountPrimaryProtocols); snapshot != nil {
					if _, err := tx.Exec(ctx, `
						UPDATE config_versions
						SET client_settings = $2::jsonb
						WHERE id = $1::uuid AND client_settings IS NULL
					`, latest.ID, snapshot); err != nil {
						return ConfigVersion{}, err
					}
					if err := tx.Commit(ctx); err != nil {
						return ConfigVersion{}, err
					}
				}
				return latest, nil
			}
		}
	} else if !errors.Is(latestErr, pgx.ErrNoRows) {
		return ConfigVersion{}, latestErr
	}

	version, err := scanConfigVersion(tx.QueryRow(ctx, `
		INSERT INTO config_versions (
			server_id,
			version,
			config_hash,
			status,
			rendered_config,
			client_settings
		)
		VALUES ($1::uuid, $2, $3, $4, $5::jsonb, $6::jsonb)
		RETURNING
			id::text,
			server_id::text,
			version,
			config_hash,
			status,
			rendered_config,
			created_at,
			applied_at,
			pinned
	`, input.ServerID, nextVersion, input.ConfigHash, input.Status, configBytes, clientSettingsBytes))
	if err != nil {
		return ConfigVersion{}, err
	}

	if _, err := tx.Exec(ctx, `
		UPDATE servers
		SET next_config_version = $2
		WHERE id = $1::uuid
	`, input.ServerID, nextVersion+1); err != nil {
		return ConfigVersion{}, err
	}

	if err := tx.Commit(ctx); err != nil {
		return ConfigVersion{}, err
	}
	return version, nil
}

func (r *Repository) ListConfigVersions(ctx context.Context, serverID string) ([]ConfigVersion, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT
			id::text,
			server_id::text,
			version,
			config_hash,
			status,
			rendered_config,
			created_at,
			applied_at,
			pinned
		FROM config_versions
		WHERE server_id = $1::uuid
		ORDER BY version DESC
	`, serverID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	items := []ConfigVersion{}
	for rows.Next() {
		item, err := scanConfigVersion(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (r *Repository) GetConfigVersion(ctx context.Context, serverID, versionID string) (ConfigVersion, error) {
	return scanConfigVersion(r.pool.QueryRow(ctx, `
		SELECT
			id::text,
			server_id::text,
			version,
			config_hash,
			status,
			rendered_config,
			created_at,
			applied_at,
			pinned
		FROM config_versions
		WHERE server_id = $1::uuid
		  AND id = $2::uuid
	`, serverID, versionID))
}

func (r *Repository) MarkConfigVersionValidated(ctx context.Context, serverID, versionID string) (ConfigVersion, error) {
	return scanConfigVersion(r.pool.QueryRow(ctx, `
		UPDATE config_versions
		SET status = $3
		WHERE server_id = $1::uuid
		  AND id = $2::uuid
		RETURNING
			id::text,
			server_id::text,
			version,
			config_hash,
			status,
			rendered_config,
			created_at,
			applied_at,
			pinned
	`, serverID, versionID, StatusValidated))
}

func (r *Repository) CreateConfigApplyJob(ctx context.Context, input CreateConfigApplyJobInput) (ConfigApplyJob, error) {
	action := input.Action
	if action == "" {
		action = ApplyJobActionApply
	}
	requestPayload := input.RequestPayload
	if requestPayload == nil {
		requestPayload = map[string]any{}
	}

	return scanConfigApplyJob(r.pool.QueryRow(ctx, `
		INSERT INTO config_apply_jobs (
			server_id,
			agent_id,
			config_version_id,
			action,
			status,
			request_payload
		)
		VALUES ($1::uuid, NULLIF($2, '')::uuid, $3::uuid, $4, $5, $6::jsonb)
		RETURNING
			id::text,
			server_id::text,
			COALESCE(agent_id::text, ''),
			config_version_id::text,
			action,
			status,
			request_payload,
			result_payload,
			COALESCE(error_message, ''),
			created_at,
			updated_at,
			started_at,
			completed_at
	`, input.ServerID, input.AgentID, input.ConfigVersionID, action, ApplyJobStatusPending, requestPayload))
}

func (r *Repository) ListConfigApplyJobs(ctx context.Context, serverID string) ([]ConfigApplyJob, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT
			id::text,
			server_id::text,
			COALESCE(agent_id::text, ''),
			config_version_id::text,
			action,
			status,
			request_payload,
			result_payload,
			COALESCE(error_message, ''),
			created_at,
			updated_at,
			started_at,
			completed_at
		FROM config_apply_jobs
		WHERE server_id = $1::uuid
		ORDER BY created_at DESC
	`, serverID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	items := []ConfigApplyJob{}
	for rows.Next() {
		item, err := scanConfigApplyJob(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (r *Repository) GetConfigApplyJob(ctx context.Context, serverID, jobID string) (ConfigApplyJob, error) {
	return scanConfigApplyJob(r.pool.QueryRow(ctx, `
		SELECT
			id::text,
			server_id::text,
			COALESCE(agent_id::text, ''),
			config_version_id::text,
			action,
			status,
			request_payload,
			result_payload,
			COALESCE(error_message, ''),
			created_at,
			updated_at,
			started_at,
			completed_at
		FROM config_apply_jobs
		WHERE server_id = $1::uuid
		  AND id = $2::uuid
	`, serverID, jobID))
}

func (r *Repository) GetCurrentConfigVersionID(ctx context.Context, serverID string) (string, error) {
	var versionID string
	err := r.pool.QueryRow(ctx, `
		SELECT COALESCE(active_config_version_id::text, '')
		FROM servers
		WHERE id = $1::uuid
	`, serverID).Scan(&versionID)
	return versionID, err
}

func scanServerConfigInfo(row pgx.Row) (ServerConfigInfo, error) {
	var info ServerConfigInfo
	var vlessPort sql.NullInt32
	var agentID sql.NullString
	var agentHostname sql.NullString
	var agentOS sql.NullString
	var agentArch sql.NullString
	var agentVersion sql.NullString
	var agentStatus sql.NullString
	var capabilitiesBytes []byte

	err := row.Scan(
		&info.ID,
		&info.Name,
		&info.DeploymentRole,
		&info.Hostname,
		&info.PublicIP,
		&info.PrivateIP,
		&info.Location,
		&info.Provider,
		&info.Status,
		&vlessPort,
		&info.VLESSFlow,
		&info.VLESSNetwork,
		&info.RealityPrivateKey,
		&info.RealityPublicKey,
		&info.RealityShortID,
		&info.RealityServerName,
		&info.VPNProtocol,
		&info.WireGuardPort,
		&info.WireGuardAddress,
		&info.WireGuardDNS,
		&info.WireGuardPrivateKey,
		&info.WireGuardPublicKey,
		&info.Hysteria2Port,
		&info.Hysteria2Domain,
		&info.Hysteria2ACMEEmail,
		&info.Hysteria2MasqueradeURL,
		&info.ShadowsocksPort,
		&info.ShadowsocksMethod,
		&info.ShadowsocksServerKey,
		&info.MTProtoPort,
		&info.MTProtoSecret,
		&info.MTProtoFrontingDomain,
		&agentID,
		&agentHostname,
		&agentOS,
		&agentArch,
		&agentVersion,
		&agentStatus,
		&capabilitiesBytes,
	)
	if err != nil {
		return ServerConfigInfo{}, err
	}
	if vlessPort.Valid {
		info.VLESSPort = int(vlessPort.Int32)
	}
	if info.VLESSPort <= 0 {
		info.VLESSPort = 443
	}
	if info.VLESSNetwork == "" {
		info.VLESSNetwork = "tcp"
	}
	if !agentID.Valid {
		return info, nil
	}

	agent := &AgentConfigInfo{
		ID:           agentID.String,
		Hostname:     agentHostname.String,
		OS:           agentOS.String,
		Arch:         agentArch.String,
		AgentVersion: agentVersion.String,
		Status:       agentStatus.String,
		Capabilities: map[string]any{},
	}
	if len(capabilitiesBytes) > 0 {
		if err := json.Unmarshal(capabilitiesBytes, &agent.Capabilities); err != nil {
			return ServerConfigInfo{}, err
		}
	}
	info.Agent = agent
	return info, nil
}

type scanner interface {
	Scan(dest ...any) error
}

func scanVPNAccountConfigInfo(row scanner) (VPNAccountConfigInfo, error) {
	var account VPNAccountConfigInfo
	err := row.Scan(
		&account.ID,
		&account.DisplayName,
		&account.Status,
		&account.VLESSUUID,
		&account.VLESSFlow,
		&account.VLESSNetwork,
		&account.WireGuardPublicKey,
		&account.WireGuardAddress,
		&account.Hysteria2Password,
		&account.ShadowsocksUserKey,
		&account.TrafficEnforcementStatus,
	)
	if err != nil {
		return VPNAccountConfigInfo{}, err
	}
	return account, nil
}

func scanRoutingProfileConfigInfo(row scanner) (RoutingProfileConfigInfo, error) {
	var profile RoutingProfileConfigInfo
	err := row.Scan(
		&profile.ID,
		&profile.Name,
		&profile.Description,
		&profile.IsDefault,
	)
	if err != nil {
		return RoutingProfileConfigInfo{}, err
	}
	return profile, nil
}

func scanRoutingProfileRuleConfigInfo(row scanner) (RoutingProfileRuleConfigInfo, error) {
	var rule RoutingProfileRuleConfigInfo
	err := row.Scan(
		&rule.ID,
		&rule.Name,
		&rule.Priority,
		&rule.Action,
		&rule.Domains,
		&rule.DomainSuffixes,
		&rule.DomainKeywords,
		&rule.IPCIDRs,
		&rule.GeoSites,
		&rule.GeoIPs,
	)
	if err != nil {
		return RoutingProfileRuleConfigInfo{}, err
	}
	return rule, nil
}

func scanConfigVersion(row scanner) (ConfigVersion, error) {
	var version ConfigVersion
	var renderedConfig []byte
	err := row.Scan(
		&version.ID,
		&version.ServerID,
		&version.Version,
		&version.ConfigHash,
		&version.Status,
		&renderedConfig,
		&version.CreatedAt,
		&version.AppliedAt,
		&version.Pinned,
	)
	if err != nil {
		return ConfigVersion{}, err
	}
	version.RenderedConfig = renderedConfig
	return version, nil
}

func scanConfigApplyJob(row scanner) (ConfigApplyJob, error) {
	var job ConfigApplyJob
	var requestPayload []byte
	var resultPayload []byte
	err := row.Scan(
		&job.ID,
		&job.ServerID,
		&job.AgentID,
		&job.ConfigVersionID,
		&job.Action,
		&job.Status,
		&requestPayload,
		&resultPayload,
		&job.ErrorMessage,
		&job.CreatedAt,
		&job.UpdatedAt,
		&job.StartedAt,
		&job.CompletedAt,
	)
	if err != nil {
		return ConfigApplyJob{}, err
	}
	job.RequestPayload = requestPayload
	job.ResultPayload = resultPayload
	return job, nil
}

func clientSettingsSnapshot(renderedConfig []byte, primaries map[string]string) []byte {
	settings, err := clientSettingsFromRenderedJSON(renderedConfig, primaries)
	if err != nil {
		return nil
	}
	snapshot, err := json.Marshal(settings)
	if err != nil {
		return nil
	}
	return snapshot
}

// ClientSettingsBackfillFailure names a config version whose stored rendered
// config could not yield client settings.
type ClientSettingsBackfillFailure struct {
	ServerID  string
	VersionID string
	Version   int
	Active    bool
	Reason    string
}

// BackfillClientSettings derives client settings for config versions rendered
// before snapshots existed, from each version's own rendered config. It never
// touches servers, apply jobs or node runtimes, and only fills empty
// snapshots, so it is safe to run on every Manager start.
func (r *Repository) BackfillClientSettings(ctx context.Context) (int, []ClientSettingsBackfillFailure, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT cv.id::text, cv.server_id::text, cv.version, cv.rendered_config,
		       COALESCE(s.active_config_version_id = cv.id, FALSE)
		FROM config_versions cv
		LEFT JOIN servers s ON s.id = cv.server_id
		WHERE cv.client_settings IS NULL
		ORDER BY cv.server_id, cv.version
	`)
	if err != nil {
		return 0, nil, err
	}
	type pending struct {
		failure  ClientSettingsBackfillFailure
		snapshot []byte
	}
	var candidates []pending
	for rows.Next() {
		var item pending
		var renderedConfig []byte
		if err := rows.Scan(&item.failure.VersionID, &item.failure.ServerID, &item.failure.Version, &renderedConfig, &item.failure.Active); err != nil {
			rows.Close()
			return 0, nil, err
		}
		settings, deriveErr := clientSettingsFromRenderedJSON(renderedConfig, nil)
		if deriveErr != nil {
			item.failure.Reason = deriveErr.Error()
		} else if item.snapshot, err = json.Marshal(settings); err != nil {
			rows.Close()
			return 0, nil, err
		}
		candidates = append(candidates, item)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, nil, err
	}

	filled := 0
	var failures []ClientSettingsBackfillFailure
	for _, item := range candidates {
		if item.snapshot == nil {
			failures = append(failures, item.failure)
			continue
		}
		tag, err := r.pool.Exec(ctx, `
			UPDATE config_versions SET client_settings = $2::jsonb
			WHERE id = $1::uuid AND client_settings IS NULL
		`, item.failure.VersionID, item.snapshot)
		if err != nil {
			return filled, failures, err
		}
		filled += int(tag.RowsAffected())
	}
	return filled, failures, nil
}

func (r *Repository) TransferCleanupAuthorized(ctx context.Context, node, id string) (bool, error) {
	var allowed bool
	err := r.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM vpn_account_transfers t JOIN vpn_account_transfer_nodes n ON n.transfer_id=t.id
 WHERE t.id=$1::uuid AND n.server_id=$2::uuid AND t.completed_at IS NULL
 AND current_setting('routegate.transfer_id',true)=t.id::text
 AND ((t.state='source_cleaning' AND t.source_server_id=n.server_id) OR (t.state='target_cleaning' AND t.target_server_id=n.server_id)))`, id, node).Scan(&allowed)
	return allowed, err
}
