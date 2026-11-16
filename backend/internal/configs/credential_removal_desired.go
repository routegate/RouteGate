package configs

import (
	"context"
	"encoding/json"
)

// CheckRemovalDesiredBaseline is read-only, including for mixed-protocol nodes.
// A target's suspended/revoked status is the one intentional difference from
// the applied baseline. It never writes a version or generates peer keys.
func (r *Repository) CheckRemovalDesiredBaseline(ctx context.Context, baseline ConfigVersion, accountID string) error {
	info, err := r.getServerConfigInfo(ctx, baseline.ServerID, false)
	if err != nil {
		return err
	}
	resolved, err := r.resolveServerAccountProtocols(ctx, baseline.ServerID, false)
	if err != nil {
		return err
	}
	applyResolvedAccountProtocols(&info, resolved)
	found := false
	for i := range info.VPNAccounts {
		a := &info.VPNAccounts[i]
		if a.ID == accountID {
			if a.Status != "active" && a.Status != "suspended" && a.Status != "revoked" {
				return ErrCredentialRemovalUnsafe
			}
			a.Status = "active"
			found = true
		}
	}
	if !found {
		return ErrCredentialRemovalUnsafe
	}
	var old RenderedConfig
	if json.Unmarshal(baseline.RenderedConfig, &old) != nil {
		return ErrCredentialRemovalUnsafe
	}
	current := buildRenderedConfig(info, old.Metadata.RenderedAt)
	raw, err := json.Marshal(current)
	if err != nil || !TransferBaselinesEquivalent(baseline.RenderedConfig, raw) {
		return ErrCredentialRemovalUnsafe
	}
	// Disabled accounts may not appear in the renderer. Do not silently ignore
	// their unapplied edits. Active-only timestamps are advanced at apply, so use
	// applied_at for preference rows and created_at for account config changes.
	var hiddenEdits bool
	err = r.pool.QueryRow(ctx, `SELECT EXISTS(
 SELECT 1 FROM vpn_accounts a WHERE a.server_id=$1::uuid AND a.id<>$2::uuid
 AND a.status<>'active' AND (a.config_updated_at>$3 OR
 EXISTS(SELECT 1 FROM vpn_client_profiles p WHERE p.vpn_account_id=a.id AND p.updated_at>$4) OR
 EXISTS(SELECT 1 FROM vpn_account_protocols p WHERE p.vpn_account_id=a.id AND p.desired_explicit AND p.updated_at>$4) OR
 EXISTS(SELECT 1 FROM vpn_account_routing_profiles p WHERE p.vpn_account_id=a.id AND p.updated_at>$4)))`, baseline.ServerID, accountID, baseline.CreatedAt, baseline.AppliedAt).Scan(&hiddenEdits)
	if err != nil {
		return err
	}
	if hiddenEdits {
		return ErrCredentialRemovalUnsafe
	}
	return nil
}
