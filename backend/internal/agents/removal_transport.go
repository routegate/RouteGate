package agents

import (
	"context"
	"errors"
	"io"
	"net/http"

	"github.com/ikaevus/routegate/backend/internal/httpx"
	"github.com/jackc/pgx/v5"
)

var ErrRemovalExchangeRejected = errors.New("credential removal exchange rejected")

// RemovalTransport must use the isolated coordinator connection. The regular
// Agent task repository does not have access to held-removal claims or results.
type RemovalTransport interface {
	ClaimHeldRemoval(context.Context, string) (*AgentDeliveryTask, error)
	AcceptHeldRemovalResult(context.Context, string, string, []byte) error
}

// MountHeldRemoval explicitly enables the exchange on an isolated Manager.
// Call during setup, before the mux can serve requests.
func (h *Handler) MountHeldRemoval(mux *http.ServeMux, transport RemovalTransport) {
	h.removal = transport
	mux.HandleFunc("POST /api/v1/agent/credential-removals/claim", h.NextHeldRemoval)
	mux.HandleFunc("POST /api/v1/agent/credential-removals/{job_id}/result", h.CompleteHeldRemoval)
}

func (h *Handler) NextHeldRemoval(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	token, ok := agentBearerToken(r.Header.Get("Authorization"))
	if !ok {
		writeAgentUnauthorized(w)
		return
	}
	if h.removal == nil {
		httpx.WriteJSON(w, http.StatusNotImplemented, httpx.Error("unsupported", "Credential removal delivery is unavailable."))
		return
	}
	task, err := h.removal.ClaimHeldRemoval(r.Context(), HashToken(token))
	if !writeRemovalExchangeError(w, err) {
		httpx.WriteJSON(w, http.StatusOK, AgentNextTaskResponse{Task: task})
	}
}

func (h *Handler) CompleteHeldRemoval(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	token, ok := agentBearerToken(r.Header.Get("Authorization"))
	if !ok {
		writeAgentUnauthorized(w)
		return
	}
	if h.removal == nil {
		httpx.WriteJSON(w, http.StatusNotImplemented, httpx.Error("unsupported", "Credential removal results are unavailable."))
		return
	}
	data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 16384))
	if err != nil {
		httpx.WriteJSON(w, http.StatusBadRequest, httpx.Error("invalid_request", "A bounded removal result is required."))
		return
	}
	if !writeRemovalExchangeError(w, h.removal.AcceptHeldRemovalResult(r.Context(), HashToken(token), r.PathValue("job_id"), data)) {
		w.WriteHeader(http.StatusNoContent)
	}
}

func writeRemovalExchangeError(w http.ResponseWriter, err error) bool {
	switch {
	case err == nil:
		return false
	case errors.Is(err, pgx.ErrNoRows):
		writeAgentUnauthorized(w)
	case errors.Is(err, ErrRemovalExchangeRejected):
		httpx.WriteJSON(w, http.StatusConflict, httpx.Error("removal_rejected", "The credential removal exchange was refused."))
	default:
		httpx.WriteJSON(w, http.StatusInternalServerError, httpx.Error("removal_unavailable", "The credential removal exchange is unavailable."))
	}
	return true
}
