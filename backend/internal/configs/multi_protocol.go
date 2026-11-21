package configs

import (
	"context"
	"strings"

	"github.com/ikaevus/routegate/backend/internal/platform"
	wgcredentials "github.com/ikaevus/routegate/backend/internal/wireguard"
)

type accountProtocolSelection struct {
	Primary   string
	Protocols []string
}

type accountProtocolResolver interface {
	ResolveServerAccountProtocols(context.Context, string) (map[string]accountProtocolSelection, error)
}

var accountProtocolOrder = []string{
	platform.VPNProtocolVLESS,
	platform.VPNProtocolWireGuard,
	platform.VPNProtocolHysteria2,
	platform.VPNProtocolShadowsocks,
	platform.VPNProtocolMTProto,
}

func normalizeAccountProtocol(value string) string {
	protocol := strings.ToLower(strings.TrimSpace(value))
	if protocol == "" || protocol == "auto" {
		return platform.VPNProtocolVLESS
	}
	return protocol
}

func orderedAccountProtocols(values []string) []string {
	wanted := map[string]bool{}
	for _, value := range values {
		protocol := strings.ToLower(strings.TrimSpace(value))
		if protocol == "" {
			continue
		}
		if protocol == "auto" {
			protocol = platform.VPNProtocolVLESS
		}
		wanted[protocol] = true
	}
	ordered := make([]string, 0, len(wanted))
	for _, protocol := range accountProtocolOrder {
		if wanted[protocol] {
			ordered = append(ordered, protocol)
			delete(wanted, protocol)
		}
	}
	for protocol := range wanted {
		ordered = append(ordered, protocol)
	}
	return ordered
}

func (r *Repository) ResolveServerAccountProtocols(ctx context.Context, serverID string) (map[string]accountProtocolSelection, error) {
	return r.resolveServerAccountProtocols(ctx, serverID, true)
}

func (r *Repository) resolveServerAccountProtocols(ctx context.Context, serverID string, ensureCredentials bool) (map[string]accountProtocolSelection, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT
			a.id::text,
			COALESCE(NULLIF(cp.protocol, 'auto'), NULLIF(s.vpn_protocol, 'auto'), 'vless') AS primary_protocol,
			COALESCE(pap.protocol, '') AS enabled_protocol
		FROM vpn_accounts a
		JOIN servers s ON s.id = $1::uuid
		LEFT JOIN vpn_client_profiles cp ON cp.vpn_account_id = a.id
		-- Only administrator choices decide what is deployed; seeded rows
		-- (desired_explicit FALSE) must not keep a previous node protocol,
		-- such as a seeded MTProto row keeping the node-wide proxy running.
		-- Without explicit choices the account deploys its primary protocol.
		LEFT JOIN vpn_account_protocols pap
			ON pap.vpn_account_id = a.id
			AND pap.desired_enabled = TRUE
			AND pap.desired_explicit = TRUE
		WHERE `+accountPlacementSQL+`
		ORDER BY a.created_at ASC, a.id ASC, pap.protocol ASC
	`, serverID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	resolved := map[string]accountProtocolSelection{}
	for rows.Next() {
		var accountID, primaryProtocol, enabledProtocol string
		if err := rows.Scan(&accountID, &primaryProtocol, &enabledProtocol); err != nil {
			return nil, err
		}
		selection := resolved[accountID]
		selection.Primary = normalizeAccountProtocol(primaryProtocol)
		if strings.TrimSpace(enabledProtocol) != "" {
			selection.Protocols = append(selection.Protocols, enabledProtocol)
		}
		resolved[accountID] = selection
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	wireGuardRequired := false
	for accountID, selection := range resolved {
		if len(selection.Protocols) == 0 {
			selection.Protocols = []string{selection.Primary}
		}
		selection.Protocols = orderedAccountProtocols(selection.Protocols)
		if !protocolListContains(selection.Protocols, selection.Primary) {
			selection.Primary = selection.Protocols[0]
		}
		wireGuardRequired = wireGuardRequired || protocolListContains(selection.Protocols, platform.VPNProtocolWireGuard)
		resolved[accountID] = selection
	}
	if ensureCredentials && wireGuardRequired {
		if err := wgcredentials.EnsureServerPeerCredentials(ctx, r.pool, serverID); err != nil {
			return nil, err
		}
	}
	return resolved, nil
}

func resolveServerAccountProtocols(ctx context.Context, repository configRepository, serverID string) (map[string]accountProtocolSelection, error) {
	resolver, ok := repository.(accountProtocolResolver)
	if !ok {
		return map[string]accountProtocolSelection{}, nil
	}
	return resolver.ResolveServerAccountProtocols(ctx, serverID)
}

func applyResolvedAccountProtocols(info *ServerConfigInfo, resolved map[string]accountProtocolSelection) {
	defaultProtocol := normalizeAccountProtocol(info.VPNProtocol)
	for index := range info.VPNAccounts {
		selection, ok := resolved[info.VPNAccounts[index].ID]
		if !ok || len(selection.Protocols) == 0 {
			selection = accountProtocolSelection{Primary: defaultProtocol, Protocols: []string{defaultProtocol}}
		}
		info.VPNAccounts[index].VPNProtocol = selection.Primary
		info.VPNAccounts[index].VPNProtocols = append([]string(nil), selection.Protocols...)
	}
}

func selectedVPNCoreAdapters(info ServerConfigInfo) []vpnCoreAdapter {
	wanted := map[string]bool{}
	defaultProtocol := normalizeAccountProtocol(info.VPNProtocol)
	for _, account := range info.VPNAccounts {
		if account.Status != "active" || account.TrafficEnforcementStatus == "over_limit" {
			continue
		}
		protocols := account.VPNProtocols
		if len(protocols) == 0 {
			legacyProtocol := strings.ToLower(strings.TrimSpace(account.VPNProtocol))
			if legacyProtocol == "" || legacyProtocol == "auto" {
				legacyProtocol = defaultProtocol
			}
			protocols = []string{legacyProtocol}
		}
		for _, protocol := range protocols {
			wanted[normalizeAccountProtocol(protocol)] = true
		}
	}
	if len(wanted) == 0 {
		wanted[defaultProtocol] = true
	}

	adapters := make([]vpnCoreAdapter, 0, len(wanted))
	for _, protocol := range accountProtocolOrder {
		if !wanted[protocol] {
			continue
		}
		candidateInfo := info
		candidateInfo.VPNProtocol = protocol
		adapters = append(adapters, selectedVPNCoreAdapter(candidateInfo))
	}
	return adapters
}

func configVPNCoreFromAdapter(adapter vpnCoreAdapter, realityEnabled bool) ConfigVPNCore {
	descriptor := adapter.Descriptor()
	transport := ""
	if len(descriptor.Transports) > 0 {
		transport = descriptor.Transports[0]
	}
	return ConfigVPNCore{Core: descriptor.Core, Protocol: descriptor.Protocol, Transport: transport, Security: selectedAdapterSecurity(descriptor, realityEnabled)}
}

func renderSelectedVPNCoreAdapters(config *RenderedConfig, info ServerConfigInfo) {
	adapters := selectedVPNCoreAdapters(info)
	if len(adapters) != 1 || configVPNCoreFromAdapter(adapters[0], config.Metadata.RealityEnabled) != config.Metadata.VPNCore {
		config.Metadata.VPNCores = make([]ConfigVPNCore, 0, len(adapters))
		for _, adapter := range adapters {
			config.Metadata.VPNCores = append(config.Metadata.VPNCores, configVPNCoreFromAdapter(adapter, config.Metadata.RealityEnabled))
		}
	}
	for _, adapter := range adapters {
		adapter.Render(config, info)
	}
	mergeRenderedVPNAccounts(config)
	configureSingBoxTraffic(config, info)
}

func configuredVPNCoreAdapters(config RenderedConfig) ([]vpnCoreAdapter, bool) {
	registry := defaultVPNCoreAdapterRegistry()
	descriptors := config.Metadata.VPNCores
	if len(descriptors) == 0 {
		descriptors = []ConfigVPNCore{config.Metadata.VPNCore}
	}
	adapters := make([]vpnCoreAdapter, 0, len(descriptors))
	seen := map[string]struct{}{}
	for _, descriptor := range descriptors {
		if strings.TrimSpace(descriptor.Core) == "" {
			adapter, ok := adapterForRenderedConfig(config)
			if !ok { return nil, false }
			key := adapter.Descriptor().Core + "/" + adapter.Descriptor().Protocol
			if _, exists := seen[key]; !exists { adapters = append(adapters, adapter); seen[key] = struct{}{} }
			continue
		}
		adapter, ok := registry.Resolve(descriptor.Core, descriptor.Protocol, descriptor.Transport, descriptor.Security)
		if !ok { return nil, false }
		key := descriptor.Core + "/" + descriptor.Protocol
		if _, exists := seen[key]; exists { continue }
		seen[key] = struct{}{}
		adapters = append(adapters, adapter)
	}
	return adapters, len(adapters) > 0
}

func validateConfiguredVPNCoreAdapters(config RenderedConfig, result *ValidationResult) {
	adapters, ok := configuredVPNCoreAdapters(config)
	if !ok {
		result.Valid = false
		result.Errors = append(result.Errors, "metadata.vpnCores selects an unsupported adapter composition.")
		return
	}
	for _, adapter := range adapters { adapter.Validate(config, result) }
}

func configuredVPNServicesReady(config RenderedConfig) bool {
	adapters, ok := configuredVPNCoreAdapters(config)
	if !ok { return false }
	for _, adapter := range adapters { if !adapter.Ready(config) { return false } }
	return true
}

func ensureSingBoxBase(config *RenderedConfig) {
	if config.SingBox.Log.Level == "" { config.SingBox.Log.Level = "info" }
	// The distro sing-box unit may discard stdout/stderr, which makes its
	// journal unusable for correlating authenticated VLESS sessions with the
	// configured RouteGate account. Keep a root-readable local runtime log in
	// sing-box's own state directory; the Agent consumes it locally and reports
	// only account IDs and aggregate presence observations.
	if config.SingBox.Log.Output == "" { config.SingBox.Log.Output = "/var/lib/sing-box/routegate-presence.log" }
	if config.SingBox.Inbounds == nil { config.SingBox.Inbounds = []map[string]any{} }
	if config.SingBox.Outbounds == nil { config.SingBox.Outbounds = []SingBoxOutbound{} }
	ensureSingBoxOutbound(config, SingBoxOutbound{Type: "direct", Tag: singBoxDirectTag})
	if config.SingBox.Route.Rules == nil { config.SingBox.Route.Rules = []map[string]any{} }
	if config.SingBox.Route.Final == "" { config.SingBox.Route.Final = singBoxDirectTag }
}

func protocolListContains(protocols []string, protocol string) bool {
	protocol = strings.ToLower(strings.TrimSpace(protocol))
	for _, candidate := range protocols {
		if strings.ToLower(strings.TrimSpace(candidate)) == protocol { return true }
	}
	return false
}

func accountUsesProtocol(account VPNAccountConfigInfo, protocol string) bool {
	if len(account.VPNProtocols) > 0 { return protocolListContains(account.VPNProtocols, protocol) }
	selected := strings.ToLower(strings.TrimSpace(account.VPNProtocol))
	return selected == "" || selected == strings.ToLower(strings.TrimSpace(protocol))
}

func renderedAccountProtocols(account ConfigVPNAccount) []string {
	protocols := append([]string(nil), account.Protocols...)
	if strings.TrimSpace(account.Protocol) != "" { protocols = append(protocols, account.Protocol) }
	if strings.TrimSpace(account.VLESSUUID) != "" { protocols = append(protocols, platform.VPNProtocolVLESS) }
	if strings.TrimSpace(account.WireGuardPublicKey) != "" || strings.TrimSpace(account.WireGuardAddress) != "" { protocols = append(protocols, platform.VPNProtocolWireGuard) }
	if strings.TrimSpace(account.Hysteria2Username) != "" { protocols = append(protocols, platform.VPNProtocolHysteria2) }
	if strings.TrimSpace(account.ShadowsocksUsername) != "" { protocols = append(protocols, platform.VPNProtocolShadowsocks) }
	return orderedAccountProtocols(protocols)
}

func mergeRenderedVPNAccounts(config *RenderedConfig) {
	if config == nil || len(config.VPNAccounts) == 0 { return }
	merged := make([]ConfigVPNAccount, 0, len(config.VPNAccounts))
	byID := map[string]int{}
	for _, account := range config.VPNAccounts {
		account.Protocols = renderedAccountProtocols(account)
		index, exists := byID[account.ID]
		if !exists {
			merged = append(merged, account)
			byID[account.ID] = len(merged) - 1
			continue
		}
		target := &merged[index]
		target.Protocols = orderedAccountProtocols(append(target.Protocols, account.Protocols...))
		if target.VLESSUUID == "" { target.VLESSUUID = account.VLESSUUID }
		if target.WireGuardPublicKey == "" { target.WireGuardPublicKey = account.WireGuardPublicKey }
		if target.WireGuardAddress == "" { target.WireGuardAddress = account.WireGuardAddress }
		if target.Hysteria2Username == "" { target.Hysteria2Username = account.Hysteria2Username }
		if target.ShadowsocksUsername == "" { target.ShadowsocksUsername = account.ShadowsocksUsername }
	}
	config.VPNAccounts = merged
}
