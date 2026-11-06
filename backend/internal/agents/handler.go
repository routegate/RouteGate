package agents

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ikaevus/routegate/backend/internal/audit"
	"github.com/ikaevus/routegate/backend/internal/httpx"
)

const activeServerStatus = "active"

type agentListRepository interface {
	List(context.Context) ([]Agent, error)
}

type agentAPIRepository interface {
	ConsumeValidRegistrationTokenByHash(context.Context, string) (ServerRegistrationToken, error)
	CreateOrReplaceAgentForServer(context.Context, CreateOrReplaceAgentInput) (Agent, error)
	ActivateServer(context.Context, string) error
	UpdateAgentHeartbeat(context.Context, UpdateAgentHeartbeatInput) (Agent, error)
	ClaimNextConfigTask(context.Context, string) (*AgentConfigTask, error)
	CompleteConfigTask(context.Context, CompleteConfigTaskInput) error
}

type agentOperationTaskRepository interface {
	ClaimNextAgentOperationTask(context.Context, string) (*AgentConfigTask, error)
	CompleteAgentOperationTask(context.Context, CompleteAgentOperationJobInput) (string, error)
}

type Handler struct {
	logger             *slog.Logger
	listRepository     agentListRepository
	repository         agentAPIRepository
	audit              *audit.Recorder
	generateAgentToken func() (string, error)
	now                func() time.Time
	recovery           RecoveryTransport
	removal            RemovalTransport
}

func NewHandler(logger *slog.Logger, pool *pgxpool.Pool) *Handler {
	repository := NewRepository(pool)

	return &Handler{
		logger:             logger,
		listRepository:     repository,
		repository:         repository,
		audit:              audit.NewRecorder(logger, pool),
		generateAgentToken: GenerateAgentToken,
		now:                time.Now,
	}
}

func (h *Handler) List(w http.ResponseWriter, r *http.Request) {
	items, err := h.listRepository.List(r.Context())
	if err != nil {
		h.logger.Error("list agents failed", "error", err)
		httpx.WriteJSON(w, http.StatusInternalServerError, httpx.Error(
			"database_error",
			"Failed to list agents.",
		))
		return
	}

	httpx.WriteJSON(w, http.StatusOK, ListAgentsResponse{Items: items})
}

func (h *Handler) Register(w http.ResponseWriter, r *http.Request) {
	var request AgentRegistrationRequest
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		h.recordRegistrationFailure(r, "", "invalid_request")
		httpx.WriteJSON(w, http.StatusBadRequest, httpx.Error(
			"invalid_request",
			"Request body must be valid JSON.",
		))
		return
	}

	request.RegistrationToken = strings.TrimSpace(request.RegistrationToken)
	if request.RegistrationToken == "" {
		h.recordRegistrationFailure(r, "", "registration_token_required")
		httpx.WriteJSON(w, http.StatusBadRequest, httpx.Error(
			"registration_token_required",
			"Registration token is required.",
		))
		return
	}

	registrationToken, err := h.repository.ConsumeValidRegistrationTokenByHash(
		r.Context(),
		HashToken(request.RegistrationToken),
	)
	if errors.Is(err, pgx.ErrNoRows) {
		h.recordRegistrationFailure(r, request.RegistrationToken, "invalid_or_expired_or_used")
		httpx.WriteJSON(w, http.StatusUnauthorized, httpx.Error(
			"invalid_registration_token",
			"Registration token is invalid, expired, or already used.",
		))
		return
	}
	if err != nil {
		h.recordRegistrationFailure(r, request.RegistrationToken, "database_error")
		h.logger.Error("validate agent registration token failed", "error", err)
		httpx.WriteJSON(w, http.StatusInternalServerError, httpx.Error(
			"database_error",
			"Failed to register agent.",
		))
		return
	}

	agentToken, err := h.generateAgentToken()
	if err != nil {
		h.logger.Error("generate agent token failed", "error", err)
		httpx.WriteJSON(w, http.StatusInternalServerError, httpx.Error(
			"token_generation_failed",
			"Failed to register agent.",
		))
		return
	}

	now := h.now().UTC()
	// TODO: Create the agent and activate its server in one transaction when the
	// repository exposes transaction-scoped registration operations. The one-time
	// registration token has already been consumed atomically.
	agent, err := h.repository.CreateOrReplaceAgentForServer(r.Context(), CreateOrReplaceAgentInput{
		ServerID:        registrationToken.ServerID,
		Hostname:        strings.TrimSpace(request.Hostname),
		OS:              strings.TrimSpace(request.OS),
		Arch:            strings.TrimSpace(request.Arch),
		AgentVersion:    strings.TrimSpace(request.AgentVersion),
		ProtocolVersion: request.ProtocolVersion,
		TokenHash:       HashToken(agentToken),
		Capabilities:    request.Capabilities,
		Status:          StatusOnline,
		RegisteredAt:    &now,
		LastSeenAt:      &now,
	})
	if err != nil {
		h.recordRegistrationFailure(r, request.RegistrationToken, "agent_create_failed")
		h.logger.Error("create agent registration failed", "error", err, "server_id", registrationToken.ServerID)
		httpx.WriteJSON(w, http.StatusInternalServerError, httpx.Error(
			"database_error",
			"Failed to register agent.",
		))
		return
	}

	if err := h.repository.ActivateServer(r.Context(), registrationToken.ServerID); err != nil {
		h.recordRegistrationFailure(r, request.RegistrationToken, "server_activate_failed")
		h.logger.Error("activate registered agent server failed", "error", err, "server_id", registrationToken.ServerID)
		httpx.WriteJSON(w, http.StatusInternalServerError, httpx.Error(
			"database_error",
			"Failed to register agent.",
		))
		return
	}

	agentTokenPreview := MaskToken(agentToken)
	h.recordAudit(r.Context(), audit.EventInput{
		ActorType:    audit.ActorTypeAgent,
		Action:       "agent.registered",
		ResourceType: "agent",
		ResourceID:   agent.ID,
		Result:       audit.ResultSuccess,
		Metadata: map[string]any{
			"server_id":                  agent.ServerID,
			"registration_token_id":      registrationToken.ID,
			"registration_token_preview": MaskToken(request.RegistrationToken),
			"agent_token_preview":        agentTokenPreview,
			"hostname":                   strings.TrimSpace(request.Hostname),
			"agent_version":              strings.TrimSpace(request.AgentVersion),
			"protocol_version":           request.ProtocolVersion,
			"os":                         strings.TrimSpace(request.OS),
			"arch":                       strings.TrimSpace(request.Arch),
		},
	})
	h.logger.Info("agent registered", "agent_id", agent.ID, "server_id", agent.ServerID)
	httpx.WriteJSON(w, http.StatusCreated, AgentRegistrationResponse{
		AgentID: agent.ID, ServerID: agent.ServerID, AgentToken: agentToken, AgentTokenPreview: agentTokenPreview,
	})
}

func (h *Handler) Heartbeat(w http.ResponseWriter, r *http.Request) {
	token, ok := agentBearerToken(r.Header.Get("Authorization"))
	if !ok {
		writeAgentUnauthorized(w)
		return
	}

	var request AgentHeartbeatRequest
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		httpx.WriteJSON(w, http.StatusBadRequest, httpx.Error(
			"invalid_request",
			"Request body must be valid JSON.",
		))
		return
	}
	if request.Telemetry != nil {
		if err := validateHeartbeatTelemetry(*request.Telemetry); err != nil {
			httpx.WriteJSON(w, http.StatusBadRequest, httpx.Error(
				"invalid_telemetry",
				"Agent telemetry snapshot is invalid.",
			))
			return
		}
	}

	agent, err := h.repository.UpdateAgentHeartbeat(r.Context(), UpdateAgentHeartbeatInput{
		TokenHash: HashToken(token), AgentVersion: request.AgentVersion, ProtocolVersion: request.ProtocolVersion, Capabilities: request.Capabilities,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		writeAgentUnauthorized(w)
		return
	}
	if err != nil {
		h.logger.Error("agent heartbeat failed", "error", err)
		httpx.WriteJSON(w, http.StatusInternalServerError, httpx.Error(
			"database_error",
			"Failed to update agent heartbeat.",
		))
		return
	}

	if request.Telemetry != nil {
		writer, supported := h.repository.(telemetryHeartbeatWriter)
		if !supported {
			h.logger.Error("agent telemetry persistence unavailable", "agent_id", agent.ID, "server_id", agent.ServerID)
			httpx.WriteJSON(w, http.StatusInternalServerError, httpx.Error(
				"telemetry_unavailable",
				"Failed to store agent telemetry.",
			))
			return
		}
		if err := writer.UpsertAgentTelemetry(r.Context(), agent.ID, agent.ServerID, *request.Telemetry); err != nil {
			h.logger.Error("persist agent telemetry failed", "error", err, "agent_id", agent.ID, "server_id", agent.ServerID)
			httpx.WriteJSON(w, http.StatusInternalServerError, httpx.Error(
				"telemetry_persistence_failed",
				"Failed to store agent telemetry.",
			))
			return
		}
	}

	h.logger.Debug("agent heartbeat accepted", "agent_id", agent.ID, "server_id", agent.ServerID)
	httpx.WriteJSON(w, http.StatusOK, AgentHeartbeatResponse{
		RuntimeMutationFencingAccepted: agent.Capabilities.RuntimeMutationFencingEnabled(),
		OK:                             true, AgentID: agent.ID, ServerID: agent.ServerID, ServerStatus: activeServerStatus,
	})
}

func (h *Handler) NextTask(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	token, ok := agentBearerToken(r.Header.Get("Authorization"))
	if !ok {
		writeAgentUnauthorized(w)
		return
	}
	tokenHash := HashToken(token)
	if h.recovery != nil {
		challenge, recoveryErr := h.recovery.NextRecoveryChallenge(r.Context(), tokenHash)
		if errors.Is(recoveryErr, pgx.ErrNoRows) {
			writeAgentUnauthorized(w)
			return
		}
		if recoveryErr != nil && !errors.Is(recoveryErr, ErrObservationRejected) {
			h.logger.Error("poll recovery observation failed")
			httpx.WriteJSON(w, http.StatusInternalServerError, httpx.Error("database_error", "Failed to fetch next agent task."))
			return
		}
		if recoveryErr == nil && challenge != nil {
			httpx.WriteJSON(w, http.StatusOK, AgentNextTaskResponse{RecoveryChallenge: challenge})
			return
		}
	}

	var task *AgentConfigTask
	var err error
	if operationRepository, supported := h.repository.(agentOperationTaskRepository); supported {
		task, err = operationRepository.ClaimNextAgentOperationTask(r.Context(), tokenHash)
		if errors.Is(err, pgx.ErrNoRows) {
			writeAgentUnauthorized(w)
			return
		}
		if err != nil {
			h.logger.Error("claim next agent operation task failed", "error", err)
			httpx.WriteJSON(w, http.StatusInternalServerError, httpx.Error("database_error", "Failed to fetch next agent task."))
			return
		}
	}
	if task == nil {
		task, err = h.repository.ClaimNextConfigTask(r.Context(), tokenHash)
		if errors.Is(err, pgx.ErrNoRows) {
			writeAgentUnauthorized(w)
			return
		}
		if err != nil {
			h.logger.Error("claim next agent config task failed", "error", err)
			httpx.WriteJSON(w, http.StatusInternalServerError, httpx.Error("database_error", "Failed to fetch next agent task."))
			return
		}
	}
	var delivery *AgentDeliveryTask
	if task != nil {
		h.recordTaskClaimed(r, *task)
		// Only the authenticated Agent response may grow a secret-bearing
		// credentialRemoval payload. Ordinary admin job DTOs remain clean.
		delivery = &AgentDeliveryTask{AgentConfigTask: *task}
	}

	httpx.WriteJSON(w, http.StatusOK, AgentNextTaskResponse{Task: delivery})
}

func (h *Handler) CompleteTask(w http.ResponseWriter, r *http.Request) {
	token, ok := agentBearerToken(r.Header.Get("Authorization"))
	if !ok {
		writeAgentUnauthorized(w)
		return
	}

	var request CompleteAgentTaskRequest
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		h.recordTaskCompletionRejected(r, r.PathValue("job_id"), "invalid_request")
		httpx.WriteJSON(w, http.StatusBadRequest, httpx.Error("invalid_request", "Request body must be valid JSON."))
		return
	}
	request.Status = strings.TrimSpace(request.Status)
	if !validConfigTaskCompletionStatus(request.Status) {
		h.recordTaskCompletionRejected(r, r.PathValue("job_id"), "invalid_status")
		httpx.WriteJSON(w, http.StatusBadRequest, httpx.Error("invalid_status", "Task status must be one of: succeeded, failed."))
		return
	}

	jobID := r.PathValue("job_id")
	tokenHash := HashToken(token)
	var err error
	var operationKind string
	if operationRepository, supported := h.repository.(agentOperationTaskRepository); supported {
		operationKind, err = operationRepository.CompleteAgentOperationTask(r.Context(), CompleteAgentOperationJobInput{
			TokenHash:     tokenHash,
			JobID:         jobID,
			Status:        request.Status,
			ErrorMessage:  strings.TrimSpace(request.ErrorMessage),
			ResultPayload: request.ResultPayload,
		})
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			h.logger.Error("complete agent operation task failed", "error", err, "job_id", jobID)
			httpx.WriteJSON(w, http.StatusInternalServerError, httpx.Error("database_error", "Failed to complete agent task."))
			return
		}
	}
	if err == nil {
		h.recordTaskCompleted(r, jobID, request.Status, operationKind)
		httpx.WriteJSON(w, http.StatusOK, CompleteAgentTaskResponse{OK: true, TaskID: jobID, Status: request.Status})
		return
	}

	err = h.repository.CompleteConfigTask(r.Context(), CompleteConfigTaskInput{
		TokenHash:     tokenHash,
		JobID:         jobID,
		Status:        request.Status,
		ErrorMessage:  strings.TrimSpace(request.ErrorMessage),
		ResultPayload: request.ResultPayload,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		h.recordTaskCompletionRejected(r, jobID, "task_not_found")
		httpx.WriteJSON(w, http.StatusNotFound, httpx.Error("task_not_found", "Task not found for this agent."))
		return
	}
	if err != nil {
		h.logger.Error("complete agent config task failed", "error", err, "job_id", jobID)
		httpx.WriteJSON(w, http.StatusInternalServerError, httpx.Error("database_error", "Failed to complete agent task."))
		return
	}

	h.recordTaskCompleted(r, jobID, request.Status, AgentTaskKindConfigApply)
	httpx.WriteJSON(w, http.StatusOK, CompleteAgentTaskResponse{OK: true, TaskID: jobID, Status: request.Status})
}

func (h *Handler) recordRegistrationFailure(r *http.Request, registrationToken string, reason string) {
	h.recordAudit(r.Context(), audit.EventInput{
		ActorType:    audit.ActorTypeAnonymous,
		Action:       "agent.registration.failed",
		ResourceType: "agent",
		Result:       audit.ResultFailure,
		Metadata: map[string]any{
			"reason":                     reason,
			"registration_token_preview": MaskToken(registrationToken),
		},
	})
}

func (h *Handler) recordTaskClaimed(r *http.Request, task AgentConfigTask) {
	kind := task.EffectiveKind()
	resourceType := "config_apply_job"
	if kind == AgentTaskKindVPNCoreService || kind == AgentTaskKindVPNCoreInstall || kind == AgentTaskKindDiagnostic {
		resourceType = "agent_operation_job"
	}
	h.recordAudit(r.Context(), audit.EventInput{
		ActorType:    audit.ActorTypeAgent,
		Action:       "agent.task.claimed",
		ResourceType: resourceType,
		ResourceID:   task.ID,
		Result:       audit.ResultSuccess,
		Metadata: map[string]any{
			"server_id":         task.ServerID,
			"agent_id":          task.AgentID,
			"kind":              kind,
			"config_version_id": task.ConfigVersionID,
			"action":            task.Action,
			"operation":         task.Operation,
			"status":            task.Status,
		},
	})
}

func (h *Handler) recordTaskCompleted(r *http.Request, jobID string, status string, kind string) {
	resourceType := "config_apply_job"
	if kind == AgentTaskKindVPNCoreService || kind == AgentTaskKindVPNCoreInstall || kind == AgentTaskKindDiagnostic {
		resourceType = "agent_operation_job"
	}
	h.recordAudit(r.Context(), audit.EventInput{
		ActorType:    audit.ActorTypeAgent,
		Action:       "agent.task.completed",
		ResourceType: resourceType,
		ResourceID:   jobID,
		Result:       audit.ResultSuccess,
		Metadata: map[string]any{
			"job_id": jobID,
			"kind":   kind,
			"status": status,
		},
	})
}

func (h *Handler) recordTaskCompletionRejected(r *http.Request, jobID string, reason string) {
	h.recordAudit(r.Context(), audit.EventInput{
		ActorType:    audit.ActorTypeAgent,
		Action:       "agent.task.completion_rejected",
		ResourceType: "agent_task",
		ResourceID:   jobID,
		Result:       audit.ResultFailure,
		Metadata: map[string]any{
			"job_id": jobID,
			"reason": reason,
		},
	})
}

func (h *Handler) recordAudit(ctx context.Context, input audit.EventInput) {
	if h.audit == nil {
		return
	}
	h.audit.RecordSafe(ctx, input)
}

func bearerToken(header string) (string, bool) {
	parts := strings.Fields(header)
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") || parts[1] == "" {
		return "", false
	}
	return strings.TrimSpace(parts[1]), true
}

func agentBearerToken(header string) (string, bool) {
	token, ok := bearerToken(header)
	if !ok || !strings.HasPrefix(token, "rg_agent_") {
		return "", false
	}
	return token, true
}

func writeAgentUnauthorized(w http.ResponseWriter) {
	httpx.WriteJSON(w, http.StatusUnauthorized, httpx.Error("unauthorized", "A valid agent bearer token is required."))
}

func validConfigTaskCompletionStatus(status string) bool {
	switch status {
	case ConfigApplyJobStatusSucceeded, ConfigApplyJobStatusFailed:
		return true
	default:
		return false
	}
}
