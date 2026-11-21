package agents

import (
	"encoding/json"
	"time"
)

const (
	StatusRegistered = "registered"
	StatusOnline     = "online"
	StatusOffline    = "offline"
	StatusDisabled   = "disabled"
	StatusError      = "error"
)

const (
	ConfigApplyJobStatusInProgress = "in_progress"
	ConfigApplyJobStatusSucceeded  = "succeeded"
	ConfigApplyJobStatusFailed     = "failed"
)

const (
	AgentTaskKindConfigApply    = "config_apply"
	AgentTaskKindVPNCoreService = "vpn_core_service"
	AgentTaskKindVPNCoreInstall = "vpn_core_install"
	AgentTaskKindDiagnostic     = "diagnostic"
	AgentTaskKindMaintenance    = "maintenance"
)

const (
	VPNCoreOperationStart                 = "start"
	VPNCoreOperationStop                  = "stop"
	VPNCoreOperationRestart               = "restart"
	VPNCoreOperationInstallSingBox        = "install_sing_box"
	DiagnosticOperationHostOverview       = "host_overview"
	DiagnosticOperationVPNCoreStatus      = "vpn_core_status"
	DiagnosticOperationManagerCertificate = "manager_certificate"
	MaintenanceOperationAnalyze           = "analyze_runtime_artifacts"
	MaintenanceOperationCleanup           = "cleanup_runtime_artifacts"
	MaintenanceOperationVerify            = "verify_runtime_artifacts"
)

type Capabilities map[string]any

// RuntimeMutationFencingEnabled reports only explicitly persisted boolean intent.
func (c Capabilities) RuntimeMutationFencingEnabled() bool {
	enabled, _ := c["runtimeMutationFencingV1"].(bool)
	return enabled
}

type Agent struct {
	ID              string        `json:"id"`
	ServerID        string        `json:"serverId"`
	Hostname        string        `json:"hostname,omitempty"`
	OS              string        `json:"os,omitempty"`
	Arch            string        `json:"arch,omitempty"`
	AgentVersion    string        `json:"agentVersion"`
	ProtocolVersion *int          `json:"protocolVersion,omitempty"`
	Compatibility   Compatibility `json:"compatibility"`
	Status          string        `json:"status"`
	TokenHash       string        `json:"-"`
	Capabilities    Capabilities  `json:"capabilities"`
	RegisteredAt    time.Time     `json:"registeredAt"`
	LastSeenAt      *time.Time    `json:"lastSeenAt,omitempty"`
	CreatedAt       time.Time     `json:"createdAt"`
	UpdatedAt       time.Time     `json:"updatedAt"`

	Name     string    `json:"name,omitempty"`
	Version  string    `json:"version,omitempty"`
	LastSeen time.Time `json:"lastSeen,omitempty"`
}

type AgentConfigTask struct {
	ID              string          `json:"id"`
	Kind            string          `json:"kind,omitempty"`
	ServerID        string          `json:"serverId"`
	AgentID         string          `json:"agentId"`
	ConfigVersionID string          `json:"configVersionId,omitempty"`
	Action          string          `json:"action,omitempty"`
	Operation       string          `json:"operation,omitempty"`
	Status          string          `json:"status"`
	RenderedConfig  json.RawMessage `json:"renderedConfig,omitempty"`
	ConfigHash      string          `json:"configHash,omitempty"`
	ResultPayload   map[string]any  `json:"resultPayload,omitempty"`
	ErrorMessage    string          `json:"errorMessage,omitempty"`
	CreatedAt       time.Time       `json:"createdAt"`
	UpdatedAt       *time.Time      `json:"updatedAt,omitempty"`
	StartedAt       *time.Time      `json:"startedAt,omitempty"`
	CompletedAt     *time.Time      `json:"completedAt,omitempty"`
}

func (t AgentConfigTask) EffectiveKind() string {
	if t.Kind == "" {
		return AgentTaskKindConfigApply
	}
	return t.Kind
}

type CompleteConfigTaskInput struct {
	TokenHash     string
	JobID         string
	Status        string
	ErrorMessage  string
	ResultPayload map[string]any
}

type CreateOrReplaceAgentInput struct {
	ServerID        string
	Hostname        string
	OS              string
	Arch            string
	AgentVersion    string
	ProtocolVersion *int
	TokenHash       string
	Capabilities    Capabilities
	Status          string
	RegisteredAt    *time.Time
	LastSeenAt      *time.Time
}

type UpdateAgentHeartbeatInput struct {
	AgentID         string
	TokenHash       string
	AgentVersion    *string
	ProtocolVersion *int
	Capabilities    Capabilities
}

type ServerRegistrationToken struct {
	ID        string     `json:"id"`
	ServerID  string     `json:"serverId"`
	TokenHash string     `json:"-"`
	ExpiresAt *time.Time `json:"expiresAt,omitempty"`
	UsedAt    *time.Time `json:"usedAt,omitempty"`
	CreatedAt time.Time  `json:"createdAt"`
}

type CreateRegistrationTokenInput struct {
	ServerID  string
	TokenHash string
	ExpiresAt *time.Time
}
