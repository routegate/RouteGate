package client

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
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
type durableTaskResultKey struct{}

// WithDurableTaskResult persists the exact JSON envelope before any HTTP attempt.
// The callback must be bound to the live task lease. It never receives a token.
func WithDurableTaskResult(ctx context.Context, save func(string, []byte) error) context.Context {
	return context.WithValue(WithStrictTaskAcknowledgement(ctx), durableTaskResultKey{}, save)
}

// WithStrictTaskAcknowledgement disables the legacy uncertain-attempt/404
// shortcut for safety-gated operations. A missing job is not positive evidence
// that Manager accepted this exact result. No global client state is changed.
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
	RuntimeMutationFencingAccepted bool   `json:"runtimeMutationFencingAccepted"`
	CredentialRemovalAccepted      bool   `json:"credentialRemovalAccepted"`
	OK                             bool   `json:"ok"`
	AgentID                        string `json:"agentId"`
	ServerID                       string `json:"serverId"`
	ServerStatus                   string `json:"serverStatus"`
}

type nextTaskResponse struct {
	Task *tasks.ConfigTask `json:"task,omitempty"`
}

type NextWorkResponse struct {
	Task              *tasks.ConfigTask               `json:"task,omitempty"`
	RecoveryChallenge *tasks.RuntimeRecoveryChallenge `json:"recoveryChallenge,omitempty"`
}

// NextWork reuses authenticated polling. An observation is not a runtime task;
// ambiguous responses cannot cause task dispatch.
func (c *Client) NextWork(ctx context.Context, token string) (NextWorkResponse, error) {
	var result NextWorkResponse
	if c.doJSON(ctx, http.MethodGet, "/api/v1/agent/tasks/next", token, nil, &result) != nil || (result.Task != nil && result.RecoveryChallenge != nil) {
		return NextWorkResponse{}, errors.New("agent work polling failed")
	}
	return result, nil
}

// SubmitRecoveryEvidence retries the identical typed response. A matching
// acknowledgement only confirms Manager storage and never permits an unlock.
func (c *Client) SubmitRecoveryEvidence(ctx context.Context, token string, challenge tasks.RuntimeRecoveryChallenge, response tasks.RuntimeRecoveryChallengeResponse) error {
	deny := errors.New("recovery observation evidence was not acknowledged")
	challengeData, err := json.Marshal(challenge)
	if err != nil {
		return deny
	}
	challengeSum := sha256.Sum256(challengeData)
	if response.OperationID != challenge.OperationID || response.ChallengeHash != hex.EncodeToString(challengeSum[:]) || response.SchemaVersion != 1 || response.Observation != challenge.Expected {
		return deny
	}
	data, err := json.Marshal(response)
	if err != nil {
		return deny
	}
	sum := sha256.Sum256(data)
	bounded, cancel := context.WithDeadline(ctx, challenge.ExpiresAt)
	defer cancel()
	for attempt := 0; attempt < completeTaskMaxAttempts; attempt++ {
		if bounded.Err() != nil || time.Now().Before(challenge.IssuedAt) || !time.Now().Before(challenge.ExpiresAt) {
			return deny
		}
		var ack struct {
			SchemaVersion int    `json:"schemaVersion"`
			Accepted      bool   `json:"accepted"`
			OperationID   string `json:"operationId"`
			ChallengeHash string `json:"challengeHash"`
			ResponseHash  string `json:"responseHash"`
		}
		err = c.doJSON(bounded, http.MethodPost, "/api/v1/agent/recovery-observations/"+url.PathEscape(response.OperationID)+"/evidence", token, json.RawMessage(data), &ack)
		if err == nil {
			if bounded.Err() != nil || !time.Now().Before(challenge.ExpiresAt) || ack.SchemaVersion != 1 || !ack.Accepted || ack.OperationID != response.OperationID || ack.ChallengeHash != response.ChallengeHash || ack.ResponseHash != hex.EncodeToString(sum[:]) {
				return deny
			}
			return nil
		}
		var status *httpStatusError
		if errors.As(err, &status) && status.statusCode < 500 {
			return deny
		}
		if attempt+1 < completeTaskMaxAttempts {
			timer := time.NewTimer(completeTaskRetryBaseWait)
			select {
			case <-bounded.Done():
				timer.Stop()
				return deny
			case <-timer.C:
			}
		}
	}
	return deny
}

type completeTaskRequest struct {
	Status        string         `json:"status"`
	ErrorMessage  string         `json:"errorMessage,omitempty"`
	ResultPayload map[string]any `json:"resultPayload,omitempty"`
}

type reportTrafficUsageRequest struct {
	Events []traffic.UsageEvent `json:"events"`
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
		// An ordinary failed envelope cannot represent an uncertain mutation.
		// Retain Manager's active job until explicit reconciliation exists.
		return errors.New("fenced task requires runtime reconciliation")
	}
	if strings.TrimSpace(jobID) == "" {
		return fmt.Errorf("job id is required")
	}
	if save, ok := ctx.Value(durableTaskResultKey{}).(func(string, []byte) error); ok {
		data, err := json.Marshal(req)
		if err != nil || save(jobID, data) != nil {
			return errors.New("durable task result unavailable")
		}
		// Freeze the envelope so retries match the durable bytes exactly.
		return c.deliverTaskResult(ctx, agentToken, jobID, json.RawMessage(data), true)
	}
	return c.deliverTaskResult(ctx, agentToken, jobID, req, strict)
}

// ReplayTaskResult reports a historical success without executing its task.
// A 404 is never acknowledgement, including after an uncertain HTTP attempt.
func (c *Client) ReplayTaskResult(ctx context.Context, agentToken, jobID string, data []byte, binding tasks.RuntimeResultBinding) error {
	if binding.ManagerURL != c.managerURL || binding.AgentID == "" || binding.ServerID == "" {
		return errors.New("result identity mismatch")
	}
	var envelope completeTaskRequest
	if json.Unmarshal(data, &envelope) != nil || envelope.Status != "succeeded" || envelope.ErrorMessage != "" || strings.TrimSpace(jobID) == "" {
		return errors.New("invalid durable task result")
	}
	err := c.deliverTaskResult(ctx, agentToken, jobID, json.RawMessage(data), true)
	if err == nil {
		return nil
	}
	// Manager may have committed before the response was lost. Read back the
	// exact historical result; a missing task alone is never acknowledgement.
	if ctx.Err() != nil || c.verifyRuntimeResult(ctx, agentToken, jobID, data, binding) != nil {
		return errors.New("historical runtime result remains unverified")
	}
	return nil
}

func (c *Client) verifyRuntimeResult(ctx context.Context, token, jobID string, data []byte, binding tasks.RuntimeResultBinding) error {
	var result struct {
		SchemaVersion  int    `json:"schemaVersion"`
		Verified       bool   `json:"verified"`
		TaskID         string `json:"taskId"`
		AgentID        string `json:"agentId"`
		ServerID       string `json:"serverId"`
		EnvelopeSHA256 string `json:"envelopeSha256"`
	}
	if binding.ManagerURL != c.managerURL || binding.AgentID == "" || binding.ServerID == "" {
		return errors.New("result identity mismatch")
	}
	path := "/api/v1/agent/tasks/" + url.PathEscape(jobID) + "/result/verify"
	if err := c.doJSON(ctx, http.MethodPost, path, token, json.RawMessage(data), &result); err != nil {
		return err
	}
	digest := sha256.Sum256(data)
	if result.SchemaVersion != 1 || !result.Verified || result.TaskID != jobID || result.AgentID != binding.AgentID || result.ServerID != binding.ServerID || result.EnvelopeSHA256 != hex.EncodeToString(digest[:]) {
		return errors.New("result evidence mismatch")
	}
	return nil
}

func (c *Client) deliverTaskResult(ctx context.Context, agentToken, jobID string, req any, strict bool) error {
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

func (c *Client) ReportClientPresence(ctx context.Context, agentToken string, snapshot presence.Snapshot) (ReportPresenceResponse, error) {
	var res ReportPresenceResponse
	if err := c.doJSON(ctx, http.MethodPost, "/api/v1/agent/client-presence", agentToken, snapshot, &res); err != nil {
		return ReportPresenceResponse{}, err
	}
	return res, nil
}

func (c *Client) doJSON(ctx context.Context, method, path, bearer string, body any, out any) error {
	return c.doJSONExpect(ctx, method, path, bearer, body, out, 0)
}

func (c *Client) doJSONExpect(ctx context.Context, method, path, bearer string, body any, out any, expectedStatus int) error {
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

	if resp.StatusCode < 200 || resp.StatusCode >= 300 || (expectedStatus != 0 && resp.StatusCode != expectedStatus) {
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
