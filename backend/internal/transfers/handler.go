package transfers

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"

	"github.com/ikaevus/routegate/backend/internal/audit"
	"github.com/ikaevus/routegate/backend/internal/auth"
	"github.com/ikaevus/routegate/backend/internal/httpx"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Handler struct {
	service *Service
	audit   *audit.Recorder
	logger  *slog.Logger
}

func NewHandler(logger *slog.Logger, pool *pgxpool.Pool) *Handler {
	return &Handler{NewService(pool), audit.NewRecorder(logger, pool), logger}
}
func actor(r *http.Request) string {
	if user, ok := auth.UserFromContext(r.Context()); ok {
		return user.ID
	}
	return "system"
}
func (h *Handler) Latest(w http.ResponseWriter, r *http.Request) {
	tr, err := h.service.Latest(r.Context(), r.PathValue("id"))
	if err != nil {
		h.failure(w, err)
		return
	}
	var requires bool
	err = h.service.pool.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM servers s JOIN config_versions cv ON cv.id=s.active_config_version_id WHERE s.id=a.server_id AND (cv.client_settings->'accounts' ? a.id::text OR cv.client_settings->'accounts' IS NULL OR cv.client_settings->'accounts'='null'::jsonb)) FROM vpn_accounts a WHERE a.id=$1::uuid`, r.PathValue("id")).Scan(&requires)
	if err != nil {
		h.failure(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"transfer": tr, "requiresTransfer": requires})
}

// Reservations lists the transfers currently holding nodes, for operator guidance.
func (h *Handler) Reservations(w http.ResponseWriter, r *http.Request) {
	type reservation struct {
		AccountID   string   `json:"accountId"`
		AccountName string   `json:"accountName"`
		SourceName  string   `json:"sourceName"`
		TargetName  string   `json:"targetName"`
		State       string   `json:"state"`
		NodeIDs     []string `json:"nodeIds"`
		NodeNames   []string `json:"nodeNames"`
	}
	rows, err := h.service.pool.Query(r.Context(), `
		SELECT a.id::text, a.display_name, source.name, target.name, t.state,
		       array_agg(n.server_id::text ORDER BY n.server_id),
		       array_agg(held.name ORDER BY n.server_id)
		FROM vpn_account_transfer_nodes n
		JOIN servers held ON held.id = n.server_id
		JOIN vpn_account_transfers t ON t.id = n.transfer_id
		JOIN vpn_accounts a ON a.id = t.vpn_account_id
		JOIN servers source ON source.id = t.source_server_id
		JOIN servers target ON target.id = t.target_server_id
		GROUP BY t.id, a.id, source.id, target.id
		ORDER BY t.created_at, t.id`)
	if err != nil {
		h.failure(w, err)
		return
	}
	items, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (reservation, error) {
		var item reservation
		err := row.Scan(&item.AccountID, &item.AccountName, &item.SourceName, &item.TargetName, &item.State, &item.NodeIDs, &item.NodeNames)
		return item, err
	})
	if err != nil {
		h.failure(w, err)
		return
	}
	if items == nil {
		items = []reservation{}
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (h *Handler) Start(w http.ResponseWriter, r *http.Request) {
	var request struct {
		TargetServerID string `json:"targetServerId"`
	}
	if !decode(w, r, &request) {
		return
	}
	tr, err := h.service.Start(r.Context(), r.PathValue("id"), request.TargetServerID, actor(r))
	if err != nil {
		h.failure(w, err)
		return
	}
	h.record(r, tr, "start")
	httpx.WriteJSON(w, http.StatusAccepted, tr)
}
func (h *Handler) Act(w http.ResponseWriter, r *http.Request) {
	var request struct {
		Action    string `json:"action"`
		Confirmed bool   `json:"confirmed"`
	}
	if !decode(w, r, &request) {
		return
	}
	if request.Action == "rollback" {
		user, ok := auth.UserFromContext(r.Context())
		if !ok || !auth.HasPermission(user.UserProfile, "configs:rollback") {
			httpx.WriteJSON(w, http.StatusForbidden, httpx.Error("forbidden", "Config rollback permission is required."))
			return
		}
	}
	tr, err := h.service.Act(r.Context(), r.PathValue("id"), r.PathValue("transferId"), request.Action, actor(r), request.Confirmed)
	if err != nil {
		h.failure(w, err)
		return
	}
	h.record(r, tr, request.Action)
	httpx.WriteJSON(w, http.StatusOK, tr)
}
func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
	d.DisallowUnknownFields()
	if d.Decode(v) != nil || d.Decode(new(any)) != io.EOF {
		httpx.WriteJSON(w, http.StatusBadRequest, httpx.Error("invalid_request", "Invalid transfer request."))
		return false
	}
	return true
}
func (h *Handler) failure(w http.ResponseWriter, err error) {
	var p *pgconn.PgError
	switch {
	case errors.Is(err, errNodeReserved):
		httpx.WriteJSON(w, http.StatusConflict, httpx.Error("node_reserved_for_transfer", "A node is reserved by an active account transfer."))
	case errors.Is(err, ErrGuard):
		httpx.WriteJSON(w, http.StatusConflict, httpx.Error("transfer_blocked", err.Error()))
	case errors.As(err, &p) && (p.Code == "P0140" || p.Code == "23505"):
		httpx.WriteJSON(w, http.StatusConflict, httpx.Error("transfer_conflict", "Another operation holds this account or node."))
	case errors.Is(err, pgx.ErrNoRows):
		httpx.WriteJSON(w, http.StatusNotFound, httpx.Error("transfer_not_found", "Account or transfer not found."))
	case errors.As(err, &p) && p.Code == "22P02":
		httpx.WriteJSON(w, http.StatusBadRequest, httpx.Error("invalid_request", "Invalid account, node or transfer identifier."))
	default:
		h.logger.Error("account transfer failed", "error", err)
		httpx.WriteJSON(w, http.StatusInternalServerError, httpx.Error("transfer_failed", "The step did not commit. Reload the operation and retry."))
	}
}
func (h *Handler) record(r *http.Request, tr Transfer, action string) {
	user, _ := auth.UserFromContext(r.Context())
	h.audit.RecordSafe(r.Context(), audit.EventInput{ActorUserID: user.ID, ActorType: audit.ActorTypeUser, Action: "vpn_account.transfer_" + action, ResourceType: "vpn_account", ResourceID: tr.AccountID, Result: audit.ResultSuccess, Metadata: map[string]any{"transfer_id": tr.ID, "state": tr.State, "source_server_id": tr.SourceID, "target_server_id": tr.TargetID}})
}
