package agents

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/ikaevus/routegate/backend/internal/httpx"
	"github.com/jackc/pgx/v5"
)

const runtimeResultVerificationLimit = 512 * 1024

type RuntimeResultVerificationInput struct {
	TokenHash     string
	JobID         string
	ResultPayload json.RawMessage
}

type RuntimeResultVerification struct {
	SchemaVersion  int    `json:"schemaVersion"`
	Verified       bool   `json:"verified"`
	TaskID         string `json:"taskId"`
	AgentID        string `json:"agentId"`
	ServerID       string `json:"serverId"`
	EnvelopeSHA256 string `json:"envelopeSha256"`
}

type runtimeResultVerificationRepository interface {
	VerifyRuntimeResult(context.Context, RuntimeResultVerificationInput) (RuntimeResultVerification, error)
}

// VerifyRuntimeResult observes a historical completion. It cannot complete a
// task, update a timestamp or authorize unlocking the Agent's runtime fence.
func (r *Repository) VerifyRuntimeResult(ctx context.Context, in RuntimeResultVerificationInput) (RuntimeResultVerification, error) {
	var result RuntimeResultVerification
	err := r.pool.QueryRow(ctx, `
		WITH eligible AS (
			SELECT j.id, j.agent_id, j.server_id, j.status, j.error_message, j.result_payload
			FROM config_apply_jobs j JOIN agents a ON a.id=j.agent_id AND a.server_id=j.server_id
			WHERE j.id=$1::uuid AND a.token_hash=$2
			  AND a.capabilities @> '{"runtimeMutationFencingV1":true}'::jsonb
			UNION ALL
			SELECT j.id, j.agent_id, j.server_id, j.status, j.error_message, j.result_payload
			FROM agent_operation_jobs j JOIN agents a ON a.id=j.agent_id AND a.server_id=j.server_id
			WHERE j.id=$1::uuid AND a.token_hash=$2
			  AND a.capabilities @> '{"runtimeMutationFencingV1":true}'::jsonb
			  AND j.kind IN ('vpn_core_service','vpn_core_install','maintenance')
		)
		SELECT id::text,agent_id::text,server_id::text,
		       status='succeeded' AND COALESCE(error_message,'')=''
		       AND COALESCE(result_payload,'{}'::jsonb)=$3::jsonb
		FROM eligible WHERE (SELECT count(*) FROM eligible)=1
	`, in.JobID, in.TokenHash, in.ResultPayload).Scan(&result.TaskID, &result.AgentID, &result.ServerID, &result.Verified)
	return result, err
}

// VerifyTaskResult accepts the original success envelope. PostgreSQL JSONB
// equality compares structured payload values without a float64 round trip.
// The response binds that observation to the exact request bytes and identity.
func (h *Handler) VerifyTaskResult(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	token, ok := agentBearerToken(r.Header.Get("Authorization"))
	if !ok {
		writeAgentUnauthorized(w)
		return
	}
	repo, ok := h.repository.(runtimeResultVerificationRepository)
	if !ok {
		httpx.WriteJSON(w, http.StatusNotImplemented, httpx.Error("unsupported", "Result verification is unavailable."))
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, runtimeResultVerificationLimit))
	var envelope struct {
		Status        string          `json:"status"`
		ErrorMessage  string          `json:"errorMessage"`
		ResultPayload json.RawMessage `json:"resultPayload"`
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err != nil || !json.Valid(body) || decoder.Decode(&envelope) != nil || envelope.Status != "succeeded" || envelope.ErrorMessage != "" {
		httpx.WriteJSON(w, http.StatusBadRequest, httpx.Error("invalid_request", "A bounded successful task result is required."))
		return
	}
	envelope.ResultPayload = bytes.TrimSpace(envelope.ResultPayload)
	if len(envelope.ResultPayload) == 0 || bytes.Equal(envelope.ResultPayload, []byte("null")) {
		envelope.ResultPayload = json.RawMessage(`{}`)
	}
	if len(envelope.ResultPayload) == 0 || envelope.ResultPayload[0] != '{' {
		httpx.WriteJSON(w, http.StatusBadRequest, httpx.Error("invalid_request", "Result payload must be an object."))
		return
	}
	result, err := repo.VerifyRuntimeResult(r.Context(), RuntimeResultVerificationInput{TokenHash: HashToken(token), JobID: r.PathValue("job_id"), ResultPayload: envelope.ResultPayload})
	if errors.Is(err, pgx.ErrNoRows) {
		httpx.WriteJSON(w, http.StatusNotFound, httpx.Error("task_not_found", "Task not found for this agent."))
		return
	}
	if err != nil {
		// Never log the request or database error: either can contain result data.
		h.logger.Error("verify historical runtime result failed")
		httpx.WriteJSON(w, http.StatusInternalServerError, httpx.Error("database_error", "Result verification failed."))
		return
	}
	digest := sha256.Sum256(body)
	result.SchemaVersion = 1
	result.EnvelopeSHA256 = hex.EncodeToString(digest[:])
	httpx.WriteJSON(w, http.StatusOK, result)
}
