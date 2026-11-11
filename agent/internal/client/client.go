package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/ikaevus/routegate/agent/internal/config"
	"github.com/ikaevus/routegate/agent/internal/diagnostics"
	"github.com/ikaevus/routegate/agent/internal/presence"
	"github.com/ikaevus/routegate/agent/internal/runtimecleanup"
	"github.com/ikaevus/routegate/agent/internal/systeminfo"
	"github.com/ikaevus/routegate/agent/internal/tasks"
	"github.com/ikaevus/routegate/agent/internal/traffic"
)

const (
	timeout                   = 10 * time.Second
	completeTaskMaxAttempts   = 4
	completeTaskRetryBaseWait = 500 * time.Millisecond
	errorResponseBodyLimit    = 4096
)

type Client struct {
	managerURL string
	httpClient *http.Client
}

type strictTaskAcknowledgementKey struct{}

// WithStrictTaskAcknowledgement requires positive Manager acceptance for
// safety-gated tasks. 404 after an uncertain attempt is not confirmation.
func WithStrictTaskAcknowledgement(ctx context.Context) context.Context {
	return context.WithValue(ctx, strictTaskAcknowledgementKey{}, true)
}

type httpStatusError struct {
	method     string
	path       string
	statusCode int
	body       string
}

func (e *httpStatusError) Error() string {
	return fmt.Sprintf("%s %s failed with status %d: %s", e.method, e.path, e.statusCode, e.body)
}

func New(managerURL string) *Client {
	return &Client{managerURL: strings.TrimRight(managerURL, "/"), httpClient: &http.Client{Timeout: timeout}}
}

type registerRequest struct {
	RegistrationToken string         `json:"registrationToken"`
	Hostname          string         `json:"hostname"`
	AgentVersion      string         `json:"agentVersion"`
	ProtocolVersion   int            `json:"protocolVersion"`
	OS                string         `json:"os"`
	Arch              string         `json:"arch"`
	Capabilities      map[string]any `json:"capabilities"`
}

type RegisterResponse struct {
	AgentID    string `json:"agentId"`
	ServerID   string `json:"serverId"`
	AgentToken string `json:"agentToken"`
}

type heartbeatRequest struct {
	AgentVersion    string                        `json:"agentVersion"`
	ProtocolVersion int                           `json:"protocolVersion"`
	Capabilities    map[string]any                `json:"capabilities"`
	Telemetry       *systeminfo.TelemetrySnapshot `json:"telemetry,omitempty"`
}

type HeartbeatResponse struct {
	RuntimeMutationFencingAccepted bool `json:"runtimeMutationFencingAccepted"`
	OK           bool   `json:"ok"`
	AgentID      string `json:"agentId"`
	ServerID     string `json:"serverId"`
	ServerStatus string `json:"serverStatus"`
}

type nextTaskResponse struct {
	Task *tasks.ConfigTask `json:"task,omitempty"`
}

type completeTaskRequest struct {
	Status        string         `json:"status"`
	ErrorMessage  string         `json:"errorMessage,omitempty"`
	ResultPayload map[string]any `json:"resultPayload,omitempty"`
}

type reportTrafficUsageRequest struct {
	StreamID string               `json:"streamId,omitempty"`
	Sequence int64                `json:"sequence,omitempty"`
	Events   []traffic.UsageEvent `json:"events"`
}

type ReportTrafficUsageResponse struct {
	OK       bool   `json:"ok"`
	AgentID  string `json:"agentId"`
	ServerID string `json:"serverId"`
	Accepted int    `json:"accepted"`
}

type ReportPresenceResponse struct {
	OK       bool   `json:"ok"`
	AgentID  string `json:"agentId"`
	ServerID string `json:"serverId"`
	Accepted int    `json:"accepted"`
}

func (c *Client) Register(ctx context.Context, cfg config.Config, info systeminfo.Info) (RegisterResponse, error) {
	req := registerRequest{RegistrationToken: cfg.RegistrationToken, Hostname: info.Hostname, AgentVersion: info.AgentVersion, ProtocolVersion: info.ProtocolVersion, OS: info.OS, Arch: info.Arch, Capabilities: advertisedCapabilities(info)}
	var res RegisterResponse
	if err := c.doJSON(ctx, http.MethodPost, "/api/v1/agent/register", "", req, &res); err != nil {
		return RegisterResponse{}, err
	}
	if res.AgentID == "" || res.ServerID == "" || res.AgentToken == "" {
		return RegisterResponse{}, fmt.Errorf("registration response missing credentials")
	}
	return res, nil
}

func (c *Client) Heartbeat(ctx context.Context, agentToken string, info systeminfo.Info) (HeartbeatResponse, error) {
	req := heartbeatRequest{
		AgentVersion:    info.AgentVersion,
		ProtocolVersion: info.ProtocolVersion,
		Capabilities:    heartbeatCapabilities(info),
		Telemetry:       info.Telemetry,
	}
	var res HeartbeatResponse
	if err := c.doJSON(ctx, http.MethodPost, "/api/v1/agent/heartbeat", agentToken, req, &res); err != nil {
		return HeartbeatResponse{}, err
	}
	return res, nil
}

func advertisedCapabilities(info systeminfo.Info) map[string]any {
	capabilities := make(map[string]any, len(info.Capabilities)+3)
	for key, value := range info.Capabilities {
		capabilities[key] = value
	}
	capabilities["diagnosticProfiles"] = []string{
		diagnostics.ProfileHostOverview,
		diagnostics.ProfileVPNCoreStatus,
		diagnostics.ProfileManagerCertificate,
	}
	capabilities["softwareUpdate"] = softwareUpdateCapability(tasks.PlatformUpdateRuntimeReady())
	capabilities["maintenanceOperations"] = []string{
		runtimecleanup.OperationAnalyze,
		runtimecleanup.OperationCleanup,
		runtimecleanup.OperationVerify,
	}
	return capabilities
}

func softwareUpdateCapability(ready bool) map[string]any {
	state := "contract_only"
	if ready {
		state = "ready"
	}
	return map[string]any{
		"schemaVersion": tasks.PlatformUpdateSchemaVersion,
		"state":         state,
		"request":       "version_only",
	}
}

func heartbeatCapabilities(info systeminfo.Info) map[string]any {
	capabilities := advertisedCapabilities(info)
	if info.RuntimeMetrics != nil {
		capabilities["runtimeMetrics"] = info.RuntimeMetrics
	}
	return capabilities
}

func (c *Client) NextTask(ctx context.Context, agentToken string) (*tasks.ConfigTask, error) {
	var res nextTaskResponse
	if err := c.doJSON(ctx, http.MethodGet, "/api/v1/agent/tasks/next", agentToken, nil, &res); err != nil {
		return nil, err
	}
	return res.Task, nil
}

func (c *Client) CompleteTask(ctx context.Context, agentToken, jobID string, req completeTaskRequest) error {
	strict, _ := ctx.Value(strictTaskAcknowledgementKey{}).(bool)
	if strict && req.Status != "succeeded" {
		// A failed envelope cannot resolve an uncertain runtime mutation.
		return errors.New("fenced task requires runtime reconciliation")
	}
	if strings.TrimSpace(jobID) == "" {
		return fmt.Errorf("job id is required")
	}
	path := "/api/v1/agent/tasks/" + url.PathEscape(jobID) + "/result"

	var lastErr error
	hadUncertainAttempt := false
	for attempt := 1; attempt <= completeTaskMaxAttempts; attempt++ {
		err := c.doJSON(ctx, http.MethodPost, path, agentToken, req, nil)
		if err == nil {
			return nil
		}

		if statusErr, ok := err.(*httpStatusError); ok {
			// A retry can race with a previously committed completion whose HTTP
			// acknowledgement was lost. The Agent owns a task ID only after Manager
			// handed it out, so a 404 after an uncertain prior completion attempt is
			// safe to treat as an idempotent acknowledgement rather than abandoning
			// the already-applied runtime state.
			if statusErr.statusCode == http.StatusNotFound && hadUncertainAttempt && !strict {
				return nil
			}
			if statusErr.statusCode != http.StatusRequestTimeout &&
				statusErr.statusCode != http.StatusTooManyRequests &&
				statusErr.statusCode < http.StatusInternalServerError {
				return err
			}
		}

		lastErr = err
		hadUncertainAttempt = true
		if attempt == completeTaskMaxAttempts {
			break
		}

		wait := completeTaskRetryBaseWait * time.Duration(1<<(attempt-1))
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}

	return fmt.Errorf("complete agent task after %d attempts: %w", completeTaskMaxAttempts, lastErr)
}

func (c *Client) CompleteTaskSucceeded(ctx context.Context, agentToken, jobID string, result map[string]any) error {
	return c.CompleteTask(ctx, agentToken, jobID, completeTaskRequest{Status: "succeeded", ResultPayload: result})
}

func (c *Client) CompleteTaskFailed(ctx context.Context, agentToken, jobID string, message string, result map[string]any) error {
	return c.CompleteTask(ctx, agentToken, jobID, completeTaskRequest{Status: "failed", ErrorMessage: message, ResultPayload: result})
}

func (c *Client) ReportTrafficUsage(ctx context.Context, agentToken string, events []traffic.UsageEvent) (ReportTrafficUsageResponse, error) {
	if len(events) == 0 {
		return ReportTrafficUsageResponse{OK: true, Accepted: 0}, nil
	}
	var res ReportTrafficUsageResponse
	if err := c.doJSON(ctx, http.MethodPost, "/api/v1/agent/traffic-usage", agentToken, reportTrafficUsageRequest{Events: events}, &res); err != nil {
		return ReportTrafficUsageResponse{}, err
	}
	return res, nil
}

func (c *Client) ReportTrafficBatch(ctx context.Context, agentToken string, box *traffic.Outbox) (ReportTrafficUsageResponse, error) {
	var res ReportTrafficUsageResponse
	err := c.doJSON(ctx, http.MethodPost, "/api/v1/agent/traffic-batches", agentToken, reportTrafficUsageRequest{Events: box.Pending, StreamID: box.StreamID, Sequence: box.Sequence}, &res)
	return res, err
}

func (c *Client) ReportClientPresence(ctx context.Context, agentToken string, snapshot presence.Snapshot) (ReportPresenceResponse, error) {
	var res ReportPresenceResponse
	if err := c.doJSON(ctx, http.MethodPost, "/api/v1/agent/client-presence", agentToken, snapshot, &res); err != nil {
		return ReportPresenceResponse{}, err
	}
	return res, nil
}

func (c *Client) doJSON(ctx context.Context, method, path, bearer string, body any, out any) error {
	var reader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.managerURL+path, reader)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		// Error text is diagnostic only, so keep it bounded. Successful Manager
		// responses can contain rendered VPN configs that legitimately exceed
		// this size and must never be truncated before JSON decoding.
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, errorResponseBodyLimit))
		return &httpStatusError{
			method:     method,
			path:       path,
			statusCode: resp.StatusCode,
			body:       strings.TrimSpace(string(respBody)),
		}
	}
	if out == nil {
		_, _ = io.Copy(io.Discard, resp.Body)
		return nil
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return err
	}
	return nil
}
