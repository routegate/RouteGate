package db

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/ikaevus/routegate/backend/internal/configs"
	"github.com/ikaevus/routegate/backend/internal/revocations"
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
	ctx, pool, input, _, done := removalPreparationFixture(t)
	defer done()

	prepared, err := revocations.NewService(pool).Prepare(ctx, input)
	if err != nil || prepared.State != "prepared" {
		t.Fatal("Manager did not prepare the isolated revocation", err)
	}
	var candidate, baselineJSON []byte
	var candidateHash, baselineRuntimeHash, candidateRuntimeHash, uuid string
	if err := pool.QueryRow(ctx, `
		SELECT r.candidate_config, cv.rendered_config,
		       r.candidate_hash, r.baseline_runtime_hash,
		       r.candidate_runtime_hash, a.vless_uuid::text
		FROM vpn_credential_revocations r
		JOIN config_versions cv ON cv.id = r.baseline_version_id
		JOIN vpn_accounts a ON a.id = r.vpn_account_id
		WHERE r.id = $1::uuid
	`, prepared.ID).Scan(&candidate, &baselineJSON,
		&candidateHash, &baselineRuntimeHash, &candidateRuntimeHash, &uuid); err != nil {
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
		Task            map[string]any  `json:"task"`
	}{
		BaselineRuntime: baselineRuntime,
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
