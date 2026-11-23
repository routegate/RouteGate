package vpnaccounts

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/ikaevus/routegate/backend/internal/httpx"
)

const (
	RoutingProfileSourceNone    = "none"
	RoutingProfileSourceAccount = "account"
	RoutingProfileSourceServer  = "server"
	RoutingProfileSourceDefault = "default"
)

type RoutingProfilePolicySummary struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	IsDefault   bool   `json:"isDefault"`
}

type NodeGroupPolicySummary struct {
	ID                string `json:"id"`
	Name              string `json:"name"`
	SelectionStrategy string `json:"selectionStrategy"`
	MemberCount       int    `json:"memberCount"`
}

type VPNAccountRoutingPolicy struct {
	VPNAccountID             string                       `json:"vpnAccountId"`
	ExplicitRoutingProfile   *RoutingProfilePolicySummary `json:"explicitRoutingProfile,omitempty"`
	EffectiveRoutingProfile  *RoutingProfilePolicySummary `json:"effectiveRoutingProfile,omitempty"`
	RoutingProfileSource     string                       `json:"routingProfileSource"`
	NodeGroup                *NodeGroupPolicySummary      `json:"nodeGroup,omitempty"`
	CurrentServerInGroup     bool                         `json:"currentServerInGroup"`
	AutomaticSelection       bool                         `json:"automaticSelection"`
	AutomaticSelectionPolicy AutomaticSelectionPolicy     `json:"automaticSelectionPolicy"`
	Protocol                 string                       `json:"protocol,omitempty"`
	ClientRoutingSupported   bool                         `json:"clientRoutingSupported"`
}

type AssignAccountRoutingProfileRequest struct {
	RoutingProfileID string `json:"routingProfileId"`
}

type AssignAccountNodeGroupRequest struct {
	NodeGroupID string `json:"nodeGroupId"`
}

type routingPolicyRepository interface {
	GetRoutingPolicy(context.Context, string) (VPNAccountRoutingPolicy, error)
	AssignRoutingProfile(context.Context, string, string) (VPNAccountRoutingPolicy, error)
	DeleteRoutingProfileAssignment(context.Context, string) (VPNAccountRoutingPolicy, error)
	AssignNodeGroup(context.Context, string, string) (VPNAccountRoutingPolicy, error)
	DeleteNodeGroupAssignment(context.Context, string) (VPNAccountRoutingPolicy, error)
	UpdateAutomaticSelectionPolicy(context.Context, string, UpdateAutomaticSelectionPolicyInput) (VPNAccountRoutingPolicy, error)
	PreviewAutomaticSelection(context.Context, string) (AutomaticSelectionDecision, error)
	ApplyAutomaticSelection(context.Context, string) (AutomaticSelectionApplyResponse, error)
}

func (r *Repository) GetRoutingPolicy(ctx context.Context, accountID string) (VPNAccountRoutingPolicy, error) {
	var accountProfileID, serverProfileID, defaultProfileID sql.NullString
	var protocol string
	err := r.db.QueryRow(ctx, `
		SELECT
			arp.routing_profile_id::text,
			srp.routing_profile_id::text,
			default_profile.id::text,
			COALESCE(s.vpn_protocol, '')
		FROM vpn_accounts a
		LEFT JOIN servers s ON s.id = a.server_id
		LEFT JOIN vpn_account_routing_profiles arp ON arp.vpn_account_id = a.id
		LEFT JOIN server_routing_profiles srp ON srp.server_id = a.server_id
		LEFT JOIN LATERAL (
			SELECT id
			FROM routing_profiles
			WHERE is_default = TRUE
			ORDER BY created_at
			LIMIT 1
		) default_profile ON TRUE
		WHERE a.id = $1::uuid
	`, accountID).Scan(&accountProfileID, &serverProfileID, &defaultProfileID, &protocol)
	if err != nil {
		return VPNAccountRoutingPolicy{}, err
	}

	policy := VPNAccountRoutingPolicy{
		VPNAccountID:           accountID,
		RoutingProfileSource:   RoutingProfileSourceNone,
		Protocol:               protocol,
		ClientRoutingSupported: protocol == "vless",
	}
	selectionPolicy, err := r.getAutomaticSelectionPolicy(ctx, accountID)
	if err != nil {
		return VPNAccountRoutingPolicy{}, err
	}
	policy.AutomaticSelectionPolicy = selectionPolicy
	policy.AutomaticSelection = selectionPolicy.Enabled
	if accountProfileID.Valid {
		profile, err := r.routingProfilePolicySummary(ctx, accountProfileID.String)
		if err != nil {
			return VPNAccountRoutingPolicy{}, err
		}
		policy.ExplicitRoutingProfile = &profile
		policy.EffectiveRoutingProfile = &profile
		policy.RoutingProfileSource = RoutingProfileSourceAccount
	} else {
		effectiveID := ""
		if serverProfileID.Valid {
			effectiveID = serverProfileID.String
			policy.RoutingProfileSource = RoutingProfileSourceServer
		} else if defaultProfileID.Valid {
			effectiveID = defaultProfileID.String
			policy.RoutingProfileSource = RoutingProfileSourceDefault
		}
		if effectiveID != "" {
			profile, err := r.routingProfilePolicySummary(ctx, effectiveID)
			if err != nil {
				return VPNAccountRoutingPolicy{}, err
			}
			policy.EffectiveRoutingProfile = &profile
		}
	}

	group, currentServerInGroup, err := r.nodeGroupPolicySummary(ctx, accountID)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return VPNAccountRoutingPolicy{}, err
	}
	if err == nil {
		policy.NodeGroup = &group
		policy.CurrentServerInGroup = currentServerInGroup
	}
	return policy, nil
}

func (r *Repository) AssignRoutingProfile(ctx context.Context, accountID, profileID string) (VPNAccountRoutingPolicy, error) {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return VPNAccountRoutingPolicy{}, err
	}
	defer tx.Rollback(ctx)

	result, err := tx.Exec(ctx, `
		INSERT INTO vpn_account_routing_profiles (vpn_account_id, routing_profile_id)
		SELECT a.id, p.id
		FROM vpn_accounts a
		CROSS JOIN routing_profiles p
		WHERE a.id = $1::uuid AND p.id = $2::uuid
		ON CONFLICT (vpn_account_id) DO UPDATE
		SET routing_profile_id = EXCLUDED.routing_profile_id, updated_at = now()
	`, accountID, profileID)
	if err != nil {
		return VPNAccountRoutingPolicy{}, err
	}
	if result.RowsAffected() == 0 {
		return VPNAccountRoutingPolicy{}, pgx.ErrNoRows
	}
	if _, err := tx.Exec(ctx, `UPDATE vpn_accounts SET config_updated_at = now(), updated_at = now() WHERE id = $1::uuid`, accountID); err != nil {
		return VPNAccountRoutingPolicy{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return VPNAccountRoutingPolicy{}, err
	}
	return r.GetRoutingPolicy(ctx, accountID)
}

func (r *Repository) DeleteRoutingProfileAssignment(ctx context.Context, accountID string) (VPNAccountRoutingPolicy, error) {
	if _, err := r.db.Exec(ctx, `DELETE FROM vpn_account_routing_profiles WHERE vpn_account_id = $1::uuid`, accountID); err != nil {
		return VPNAccountRoutingPolicy{}, err
	}
	if _, err := r.db.Exec(ctx, `UPDATE vpn_accounts SET config_updated_at = now(), updated_at = now() WHERE id = $1::uuid`, accountID); err != nil {
		return VPNAccountRoutingPolicy{}, err
	}
	return r.GetRoutingPolicy(ctx, accountID)
}

func (r *Repository) AssignNodeGroup(ctx context.Context, accountID, groupID string) (VPNAccountRoutingPolicy, error) {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return VPNAccountRoutingPolicy{}, err
	}
	defer tx.Rollback(ctx)
	if err := lockVPNAccount(ctx, tx, accountID); err != nil {
		return VPNAccountRoutingPolicy{}, err
	}
	var groupExists bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM node_groups WHERE id = $1::uuid)`, groupID).Scan(&groupExists); err != nil {
		return VPNAccountRoutingPolicy{}, err
	}
	if !groupExists {
		return VPNAccountRoutingPolicy{}, pgx.ErrNoRows
	}
	var currentGroupID string
	if err := tx.QueryRow(ctx, `
		SELECT COALESCE((
			SELECT node_group_id::text
			FROM vpn_account_node_groups
			WHERE vpn_account_id = $1::uuid
		), '')
	`, accountID).Scan(&currentGroupID); err != nil {
		return VPNAccountRoutingPolicy{}, err
	}
	if currentGroupID == groupID {
		if err := tx.Commit(ctx); err != nil {
			return VPNAccountRoutingPolicy{}, err
		}
		return r.GetRoutingPolicy(ctx, accountID)
	}
	_, err = tx.Exec(ctx, `
		INSERT INTO vpn_account_node_groups (vpn_account_id, node_group_id)
		VALUES ($1::uuid, $2::uuid)
		ON CONFLICT (vpn_account_id) DO UPDATE
		SET node_group_id = EXCLUDED.node_group_id, updated_at = now()
	`, accountID, groupID)
	if err != nil {
		return VPNAccountRoutingPolicy{}, err
	}
	if _, err := tx.Exec(ctx, `
		UPDATE vpn_account_automatic_selection_policies
		SET last_selected_at = NULL,
			last_selected_server_id = NULL,
			updated_at = now()
		WHERE vpn_account_id = $1::uuid
	`, accountID); err != nil {
		return VPNAccountRoutingPolicy{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return VPNAccountRoutingPolicy{}, err
	}
	return r.GetRoutingPolicy(ctx, accountID)
}

func (r *Repository) DeleteNodeGroupAssignment(ctx context.Context, accountID string) (VPNAccountRoutingPolicy, error) {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return VPNAccountRoutingPolicy{}, err
	}
	defer tx.Rollback(ctx)
	if err := lockVPNAccount(ctx, tx, accountID); err != nil {
		return VPNAccountRoutingPolicy{}, err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM vpn_account_node_groups WHERE vpn_account_id = $1::uuid`, accountID); err != nil {
		return VPNAccountRoutingPolicy{}, err
	}
	if _, err := tx.Exec(ctx, `
		UPDATE vpn_account_automatic_selection_policies
		SET enabled = FALSE, updated_at = now()
		WHERE vpn_account_id = $1::uuid
	`, accountID); err != nil {
		return VPNAccountRoutingPolicy{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return VPNAccountRoutingPolicy{}, err
	}
	return r.GetRoutingPolicy(ctx, accountID)
}

func (r *Repository) routingProfilePolicySummary(ctx context.Context, profileID string) (RoutingProfilePolicySummary, error) {
	var profile RoutingProfilePolicySummary
	err := r.db.QueryRow(ctx, `
		SELECT id::text, name, COALESCE(description, ''), is_default
		FROM routing_profiles
		WHERE id = $1::uuid
	`, profileID).Scan(&profile.ID, &profile.Name, &profile.Description, &profile.IsDefault)
	return profile, err
}

func (r *Repository) nodeGroupPolicySummary(ctx context.Context, accountID string) (NodeGroupPolicySummary, bool, error) {
	var group NodeGroupPolicySummary
	var currentServerInGroup bool
	err := r.db.QueryRow(ctx, `
		SELECT
			g.id::text,
			g.name,
			g.selection_strategy,
			COUNT(m.server_id)::int,
			COALESCE(BOOL_OR(m.server_id = a.server_id), FALSE)
		FROM vpn_accounts a
		JOIN vpn_account_node_groups ang ON ang.vpn_account_id = a.id
		JOIN node_groups g ON g.id = ang.node_group_id
		LEFT JOIN node_group_members m ON m.node_group_id = g.id
		WHERE a.id = $1::uuid
		GROUP BY g.id
	`, accountID).Scan(&group.ID, &group.Name, &group.SelectionStrategy, &group.MemberCount, &currentServerInGroup)
	return group, currentServerInGroup, err
}

func (h *Handler) GetRoutingPolicy(w http.ResponseWriter, r *http.Request) {
	repository, ok := h.accounts.(routingPolicyRepository)
	if !ok {
		writeRoutingPolicyUnavailable(w)
		return
	}
	policy, err := repository.GetRoutingPolicy(r.Context(), r.PathValue("id"))
	if errors.Is(err, pgx.ErrNoRows) {
		writeAccountNotFound(w)
		return
	}
	if err != nil {
		h.databaseError(w, "get VPN account routing policy", err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, policy)
}

// ExportV2RayNRouting delivers only routing rules to an authenticated operator.
func (h *Handler) ExportV2RayNRouting(w http.ResponseWriter, r *http.Request) {
	repository, ok := h.accounts.(interface {
		GetRoutingPolicy(context.Context, string) (VPNAccountRoutingPolicy, error)
		listRoutingProfileRules(context.Context, string) ([]RoutingProfileRule, error)
	})
	if !ok {
		writeRoutingPolicyUnavailable(w)
		return
	}
	ctx := r.Context()
	policy, err := repository.GetRoutingPolicy(ctx, r.PathValue("id"))
	if errors.Is(err, pgx.ErrNoRows) {
		writeAccountNotFound(w)
		return
	}
	if err != nil {
		h.databaseError(w, "resolve v2rayN routing policy", err)
		return
	}
	if policy.EffectiveRoutingProfile == nil {
		httpx.WriteJSON(w, http.StatusNotFound, httpx.Error("routing_profile_not_found", "No routing profile is assigned."))
		return
	}
	rules, err := repository.listRoutingProfileRules(ctx, policy.EffectiveRoutingProfile.ID)
	if err != nil {
		h.databaseError(w, "load v2rayN routing rules", err)
		return
	}
	export, ok := renderV2RayNCustomRoutingRules(&RoutingProfile{Rules: rules})
	if !ok {
		httpx.WriteJSON(w, http.StatusUnprocessableEntity, httpx.Error("routing_rules_unavailable", "The profile has no rules available for v2rayN."))
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	httpx.WriteJSON(w, http.StatusOK, export)
}

func (h *Handler) AssignRoutingProfile(w http.ResponseWriter, r *http.Request) {
	var request AssignAccountRoutingProfileRequest
	if !decodeRoutingPolicyJSON(w, r, &request) {
		return
	}
	request.RoutingProfileID = strings.TrimSpace(request.RoutingProfileID)
	if request.RoutingProfileID == "" {
		writeInvalidRequest(w, "routingProfileId is required")
		return
	}
	h.writeRoutingPolicyMutation(w, r, func(repository routingPolicyRepository) (VPNAccountRoutingPolicy, error) {
		return repository.AssignRoutingProfile(r.Context(), r.PathValue("id"), request.RoutingProfileID)
	})
}

func (h *Handler) DeleteRoutingProfileAssignment(w http.ResponseWriter, r *http.Request) {
	h.writeRoutingPolicyMutation(w, r, func(repository routingPolicyRepository) (VPNAccountRoutingPolicy, error) {
		return repository.DeleteRoutingProfileAssignment(r.Context(), r.PathValue("id"))
	})
}

func (h *Handler) AssignNodeGroup(w http.ResponseWriter, r *http.Request) {
	var request AssignAccountNodeGroupRequest
	if !decodeRoutingPolicyJSON(w, r, &request) {
		return
	}
	request.NodeGroupID = strings.TrimSpace(request.NodeGroupID)
	if request.NodeGroupID == "" {
		writeInvalidRequest(w, "nodeGroupId is required")
		return
	}
	h.writeRoutingPolicyMutation(w, r, func(repository routingPolicyRepository) (VPNAccountRoutingPolicy, error) {
		return repository.AssignNodeGroup(r.Context(), r.PathValue("id"), request.NodeGroupID)
	})
}

func (h *Handler) DeleteNodeGroupAssignment(w http.ResponseWriter, r *http.Request) {
	h.writeRoutingPolicyMutation(w, r, func(repository routingPolicyRepository) (VPNAccountRoutingPolicy, error) {
		return repository.DeleteNodeGroupAssignment(r.Context(), r.PathValue("id"))
	})
}

func (h *Handler) writeRoutingPolicyMutation(
	w http.ResponseWriter,
	r *http.Request,
	mutate func(routingPolicyRepository) (VPNAccountRoutingPolicy, error),
) {
	repository, ok := h.accounts.(routingPolicyRepository)
	if !ok {
		writeRoutingPolicyUnavailable(w)
		return
	}
	policy, err := mutate(repository)
	if errors.Is(err, pgx.ErrNoRows) {
		httpx.WriteJSON(w, http.StatusNotFound, httpx.Error("routing_policy_target_not_found", "VPN account or routing policy target not found."))
		return
	}
	if err != nil {
		h.databaseError(w, "update VPN account routing policy", err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, policy)
}

func decodeRoutingPolicyJSON(w http.ResponseWriter, r *http.Request, target any) bool {
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 32<<10))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		writeInvalidRequest(w, "Request body must be valid JSON.")
		return false
	}
	return true
}

func writeRoutingPolicyUnavailable(w http.ResponseWriter) {
	httpx.WriteJSON(w, http.StatusInternalServerError, httpx.Error("routing_policy_unavailable", "VPN account routing policy storage is unavailable."))
}
