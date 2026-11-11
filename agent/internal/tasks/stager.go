package tasks

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

const (
	TaskKindConfigApply     = "config_apply"
	TaskKindVPNCoreService  = "vpn_core_service"
	TaskKindVPNCoreInstall  = "vpn_core_install"
	TaskKindMaintenance     = "maintenance"
	InstallOperationSingBox = "install_sing_box"
)

type ConfigTask struct {
	ID                string                    `json:"id"`
	Kind              string                    `json:"kind,omitempty"`
	ServerID          string                    `json:"serverId"`
	AgentID           string                    `json:"agentId"`
	ConfigVersionID   string                    `json:"configVersionId,omitempty"`
	Action            string                    `json:"action,omitempty"`
	Operation         string                    `json:"operation,omitempty"`
	Status            string                    `json:"status"`
	RenderedConfig    json.RawMessage           `json:"renderedConfig,omitempty"`
	ConfigHash        string                    `json:"configHash,omitempty"`
	CredentialRemoval *CredentialRemovalRequest `json:"credentialRemoval,omitempty"`
}

func (t ConfigTask) EffectiveKind() string {
	if t.Kind == "" {
		return TaskKindConfigApply
	}
	return t.Kind
}

type StageResult struct {
	StagedPath      string `json:"stagedPath"`
	ConfigHash      string `json:"configHash"`
	ConfigVersionID string `json:"configVersionId"`
}

type Stager struct {
	dir string
}

func NewStager(dir string) Stager {
	return Stager{dir: dir}
}

func (s Stager) Stage(task ConfigTask) (StageResult, error) {
	if task.EffectiveKind() != TaskKindConfigApply {
		return StageResult{}, fmt.Errorf("task kind %q cannot be staged as config", task.EffectiveKind())
	}
	if task.ID == "" {
		return StageResult{}, fmt.Errorf("task id is required")
	}
	if task.ConfigVersionID == "" {
		return StageResult{}, fmt.Errorf("config version id is required")
	}
	if !json.Valid(task.RenderedConfig) {
		return StageResult{}, fmt.Errorf("rendered config must be valid JSON")
	}

	configBytes, err := extractSingBoxConfig(task.RenderedConfig)
	if err != nil {
		return StageResult{}, err
	}

	if err := os.MkdirAll(s.dir, 0o750); err != nil {
		return StageResult{}, fmt.Errorf("create config staging dir: %w", err)
	}

	var pretty bytes.Buffer
	if err := json.Indent(&pretty, configBytes, "", "  "); err != nil {
		return StageResult{}, fmt.Errorf("format rendered config: %w", err)
	}
	pretty.WriteByte('\n')

	path := filepath.Join(s.dir, task.ConfigVersionID+".json")
	tmpPath := path + ".tmp"
	if err := os.WriteFile(tmpPath, pretty.Bytes(), 0o600); err != nil {
		return StageResult{}, fmt.Errorf("write staged config: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		_ = os.Remove(tmpPath)
		return StageResult{}, fmt.Errorf("commit staged config: %w", err)
	}

	return StageResult{StagedPath: path, ConfigHash: task.ConfigHash, ConfigVersionID: task.ConfigVersionID}, nil
}

func extractSingBoxConfig(renderedConfig json.RawMessage) (json.RawMessage, error) {
	var envelope struct {
		SchemaVersion string          `json:"schemaVersion"`
		SingBox       json.RawMessage `json:"singBox"`
	}
	if err := json.Unmarshal(renderedConfig, &envelope); err != nil {
		return nil, fmt.Errorf("decode rendered config envelope: %w", err)
	}

	// Keep backward compatibility with tasks that contain a plain sing-box
	// configuration instead of the RouteGate config envelope.
	if envelope.SchemaVersion == "" {
		return renderedConfig, nil
	}

	if len(envelope.SingBox) == 0 || !json.Valid(envelope.SingBox) {
		return nil, fmt.Errorf("rendered config envelope must contain valid singBox JSON")
	}
	return envelope.SingBox, nil
}
