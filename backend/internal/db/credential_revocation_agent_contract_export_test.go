package db

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/ikaevus/routegate/backend/internal/configs"
	"github.com/ikaevus/routegate/backend/internal/revocations"
	"github.com/ikaevus/routegate/backend/internal/vpnaccounts"
	"github.com/jackc/pgx/v5/pgxpool"
)

// TestCredentialRevocationExportAgentContractFixture is invoked only by the
// isolated cross-module CI step. The file contains synthetic, secret-bearing
// applied runtime state and must never be uploaded or printed as an artifact.
// This is a preparation/Agent contract check, NOT authorized task dispatch.
func TestCredentialRevocationExportAgentContractFixture(t *testing.T) {
	path := os.Getenv("ROUTEGATE_TEST_REMOVAL_CONTRACT_EXPORT")
	if path == "" {
		t.Skip("cross-module Manager/Agent contract fixture export is opt-in")
	}
	if !filepath.IsAbs(path) {
		t.Fatal("fixture export path must be absolute")
	}
	var (
		ctx context.Context
		pool *pgxpool.Pool
		input revocations.PrepareInput
		done func()
	)
	if realityPort := os.Getenv("ROUTEGATE_TEST_REMOVAL_REALITY_PORT"); realityPort != "" {
		ctx, pool, input, done = preparedRemovalRealityFixture(t, realityPort)
	} else {
		var unusedTarget string
		ctx, pool, input, unusedTarget, done = removalPreparationFixture(t)
		_ = unusedTarget
	}
	defer done()

	prepared, err := revocations.NewService(pool).Prepare(ctx, input)
	if err != nil || prepared.State != "prepared" {
		t.Fatal("Manager did not prepare the isolated revocation", err)
	}
	var candidate, baselineJSON []byte
	var candidateHash, baselineRuntimeHash, candidateRuntimeHash, uuid, publicKey string
	if err := pool.QueryRow(ctx, `
		SELECT r.candidate_config, cv.rendered_config,
		       r.candidate_hash, r.baseline_runtime_hash,
		       r.candidate_runtime_hash, a.vless_uuid::text, s.reality_public_key
		FROM vpn_credential_revocations r
		JOIN config_versions cv ON cv.id = r.baseline_version_id
		JOIN vpn_accounts a ON a.id = r.vpn_account_id
		JOIN servers s ON s.id = r.server_id
		WHERE r.id = $1::uuid
	`, prepared.ID).Scan(&candidate, &baselineJSON,
		&candidateHash, &baselineRuntimeHash, &candidateRuntimeHash, &uuid, &publicKey); err != nil {
		t.Fatal("prepared Manager candidate not available", err)
	}
	var baseline configs.RenderedConfig
	if err := json.Unmarshal(baselineJSON, &baseline); err != nil {
		t.Fatal("invalid applied Manager baseline")
	}
	baselineRuntime, err := json.Marshal(baseline.SingBox)
	if err != nil {
		t.Fatal("could not encode applied sing-box runtime")
	}

	// Synthetic task IDs are never stored, claimed or executed by Manager.
	// The task shape is only a cross-module encoding test: real dispatch
	// still requires fresh human consent, reservation and authenticated claim.
	payload := struct {
		BaselineRuntime json.RawMessage `json:"baselineRuntime"`
		PublicKey string `json:"publicKey"`
		Task            map[string]any  `json:"task"`
	}{
		BaselineRuntime: baselineRuntime,
		PublicKey: publicKey,
		Task: map[string]any{
			"id": "99999999-9999-4999-8999-999999999999",
			"kind": revocations.TaskKindCredentialRemoval,
			"serverId": prepared.ServerID,
			"agentId": prepared.AgentID,
			"configVersionId": "aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee",
			"action": "apply",
			"status": "in_progress",
			"renderedConfig": json.RawMessage(candidate),
			"configHash": candidateHash,
			"credentialRemoval": map[string]any{
				"schemaVersion": 1,
				"operationId": prepared.ID,
				"accountId": prepared.AccountID,
				"baselineVersionId": prepared.BaselineVersionID,
				"baselineRuntimeHash": baselineRuntimeHash,
				"candidateRuntimeHash": candidateRuntimeHash,
				"vlessUuid": uuid,
				"sharedProcessRestartAcknowledged": true,
			},
		},
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		t.Fatal("cannot create private cross-module fixture", err)
	}
	defer file.Close()
	if err := json.NewEncoder(file).Encode(payload); err != nil {
		t.Fatal("could not write private cross-module fixture", err)
	}
	if err := file.Sync(); err != nil {
		t.Fatal("could not sync private cross-module fixture", err)
	}
}


// The already-applied baseline must itself use an isolated loopback Reality
// handshake target. Do not edit Manager's rendered JSON after preparation;
// doing that would defeat the exact-candidate hash and desired-baseline gate.
func preparedRemovalRealityFixture(t *testing.T, portText string) (context.Context, *pgxpool.Pool, revocations.PrepareInput, func()) {
	t.Helper()
	port, err := strconv.Atoi(portText)
	if err != nil || port <= 1024 || port > 65535 {
		t.Fatal("invalid disposable Reality listener port")
	}
	ctx, pool, done := setupAppliedSettingsTest(t)
	node, account, _ := createAppliedSettingsServer(t, ctx, pool, newRealityTestKey(t))
	if _, err = pool.Exec(ctx, `UPDATE servers SET reality_server_name='localhost',vless_port=$2
		WHERE id=$1::uuid`, node, port); err != nil {
		done()
		t.Fatal("cannot configure disposable Reality listener", err)
	}
	addTransferAgent(t, ctx, pool, node)
	accounts := vpnaccounts.NewRepository(pool)
	if _, err = pool.Exec(ctx, `INSERT INTO vpn_client_profiles(vpn_account_id,active_protocol)
		VALUES($1::uuid,'vless') ON CONFLICT(vpn_account_id) DO NOTHING`, account); err != nil {
		done()
		t.Fatal(err)
	}
	device, err := accounts.CreateDevice(ctx, vpnaccounts.CreateDeviceInput{
		VPNAccountID: account, Name:"Disposable phone",
		ClientType:vpnaccounts.ClientTypeHiddify,
		DeviceType:vpnaccounts.DevicePlatformIOS,
	})
	if err != nil {
		done()
		t.Fatal("cannot create disposable client device", err)
	}
	if _, err = accounts.CreateDeviceSubscriptionToken(ctx, device.ID,
		vpnaccounts.HashSubscriptionToken("manager-reality-contract-only"), nil); err != nil {
		done()
		t.Fatal("cannot create disposable subscription", err)
	}
	service := configs.NewService(configs.NewRepository(pool))
	version := renderVersion(t, ctx, service, node)
	apply, err := service.Apply(ctx, node, version, configs.ApplyConfigRequest{})
	if err != nil {
		done()
		t.Fatal("cannot issue isolated baseline apply", err)
	}
	completeTransferApply(t, ctx, pool, apply.Job.ID, "succeeded", true)
	input := revocations.PrepareInput{AccountID:account, ServerID:node, Actor:"isolated-test-operator"}
	if err = pool.QueryRow(ctx, `SELECT cv.id::text,cv.config_hash FROM servers s
		JOIN config_versions cv ON cv.id=s.active_config_version_id
		WHERE s.id=$1::uuid`,node).Scan(&input.BaselineVersionID,&input.BaselineHash); err != nil {
		done()
		t.Fatal("cannot read applied baseline pin",err)
	}
	return ctx,pool,input,done
}
