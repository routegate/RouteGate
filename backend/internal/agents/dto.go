package agents

type ListAgentsResponse struct {
	Items []Agent `json:"items"`
}

type AgentRegistrationRequest struct {
	RegistrationToken string       `json:"registrationToken"`
	Hostname          string       `json:"hostname"`
	AgentVersion      string       `json:"agentVersion"`
	ProtocolVersion   *int         `json:"protocolVersion,omitempty"`
	OS                string       `json:"os"`
	Arch              string       `json:"arch"`
	Capabilities      Capabilities `json:"capabilities"`
}

type AgentRegistrationResponse struct {
	AgentID           string `json:"agentId"`
	ServerID          string `json:"serverId"`
	AgentToken        string `json:"agentToken"`
	AgentTokenPreview string `json:"agentTokenPreview"`
}

type AgentHeartbeatRequest struct {
	AgentVersion    *string             `json:"agentVersion,omitempty"`
	ProtocolVersion *int                `json:"protocolVersion,omitempty"`
	Capabilities    Capabilities        `json:"capabilities,omitempty"`
	Telemetry       *HeartbeatTelemetry `json:"telemetry,omitempty"`
}

type AgentHeartbeatResponse struct {
	RuntimeMutationFencingAccepted bool   `json:"runtimeMutationFencingAccepted,omitempty"`
	OK           bool   `json:"ok"`
	AgentID      string `json:"agentId"`
	ServerID     string `json:"serverId"`
	ServerStatus string `json:"serverStatus"`
}

type AgentNextTaskResponse struct {
	Task *AgentConfigTask `json:"task,omitempty"`
}

type CompleteAgentTaskRequest struct {
	Status        string         `json:"status"`
	ErrorMessage  string         `json:"errorMessage,omitempty"`
	ResultPayload map[string]any `json:"resultPayload,omitempty"`
}

type CompleteAgentTaskResponse struct {
	OK     bool   `json:"ok"`
	TaskID string `json:"taskId"`
	Status string `json:"status"`
}
