package configs

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"reflect"
	"strings"

	"github.com/ikaevus/routegate/backend/internal/platform"
)

// ErrCredentialRemovalUnsafe intentionally carries no deployed credentials.
var ErrCredentialRemovalUnsafe = errors.New("credential removal candidate failed its safety gate")

// VLESSRemovalTarget pins an identity read by the operation from the account
// record to the actual applied snapshot. These values are not browser input.
type VLESSRemovalTarget struct {
	ServerID   string
	AccountID  string
	VersionID  string
	ConfigHash string
	VLESSUUID  string `json:"-"`
}

// VLESSRemovalCandidate is secret-bearing, in-memory preparation, not a config
// version, apply job, or proof of revocation. In particular, json.Marshal must
// never serialize it into an API response or diagnostic export.
type VLESSRemovalCandidate struct {
	RenderedConfig RenderedConfig `json:"-"`
	ConfigHash     string         `json:"-"`
	RemainingUsers int            `json:"-"`
}

// PrepareVLESSRemoval copies a pinned applied baseline and removes exactly one
// account entry and its VLESS authenticator. It never reads desired settings,
// writes a version, queues an Agent task, or changes account/subscription state.
// The caller still has to prove that this is the current applied version,
// reserve the node/account, check Agent freshness, and authorize an operation.
func PrepareVLESSRemoval(baseline ConfigVersion, target VLESSRemovalTarget) (VLESSRemovalCandidate, error) {
	deny := func() (VLESSRemovalCandidate, error) { return VLESSRemovalCandidate{}, ErrCredentialRemovalUnsafe }
	if target.ServerID == "" || target.AccountID == "" || target.VersionID == "" || target.ConfigHash == "" ||
		baseline.ServerID != target.ServerID || baseline.ID != target.VersionID || baseline.ConfigHash != target.ConfigHash ||
		baseline.Status != StatusApplied || baseline.AppliedAt == nil || baseline.AppliedAt.IsZero() {
		return deny()
	}
	identity, ok := canonicalVLESSIdentity(target.VLESSUUID)
	if !ok {
		return deny()
	}
	config, err := decodeRemovalBaseline(baseline.RenderedConfig)
	if err != nil {
		return deny()
	}
	if config.Server.ID != target.ServerID || (config.Server.DeploymentRole != "vpn" && config.Server.DeploymentRole != "hybrid") ||
		config.Agent == nil || config.Agent.ID == "" || !config.Metadata.RealityEnabled || config.Metadata.TransferEmptyUsers ||
		config.MTProto != "" || !ValidateRenderedConfig(config).Valid || !vpnServiceReady(config) {
		return deny()
	}
	cores := config.Metadata.VPNCores
	if len(cores) == 0 {
		cores = []ConfigVPNCore{config.Metadata.VPNCore}
	}
	managedReality := false
	for _, core := range cores {
		if core.Protocol == platform.VPNProtocolVLESS {
			if core.Core != platform.VPNCoreSingBox || core.Transport != platform.VPNTransportTCP || core.Security != platform.VPNSecurityReality {
				return deny()
			}
			managedReality = true
		}
	}
	if !managedReality {
		return deny()
	}
	// A shared MTProto secret cannot establish single-account isolation. Other
	// independently represented protocol payloads remain unchanged.
	hash, err := hashRenderedConfig(config)
	if err != nil || hash != target.ConfigHash {
		return deny()
	}

	accounts := make(map[string]ConfigVPNAccount, len(config.VPNAccounts))
	byUUID := map[string]string{}
	targetIndex := -1
	for index, account := range config.VPNAccounts {
		if account.ID == "" {
			return deny()
		}
		if _, duplicate := accounts[account.ID]; duplicate {
			return deny()
		}
		accounts[account.ID] = account
		if account.VLESSUUID != "" {
			uuid, valid := canonicalVLESSIdentity(account.VLESSUUID)
			if !valid {
				return deny()
			}
			if _, duplicate := byUUID[uuid]; duplicate {
				return deny()
			}
			byUUID[uuid] = account.ID
		}
		if account.ID == target.AccountID {
			protocols := renderedAccountProtocols(account)
			uuid, valid := canonicalVLESSIdentity(account.VLESSUUID)
			if !valid || uuid != identity || account.Status != "active" ||
				len(protocols) != 1 || protocols[0] != platform.VPNProtocolVLESS {
				return deny()
			}
			targetIndex = index
		}
	}
	if targetIndex < 0 {
		return deny()
	}

	var inbound map[string]any
	for _, candidate := range config.SingBox.Inbounds {
		if strings.EqualFold(strings.TrimSpace(stringValue(candidate["type"])), platform.VPNProtocolVLESS) {
			if inbound != nil {
				return deny()
			}
			inbound = candidate
		}
	}
	if inbound == nil || stringValue(inbound["tag"]) != singBoxVLESSInboundTag ||
		inbound["transport"] != nil || len(validateRealityInbound(inbound)) != 0 {
		return deny()
	}
	users, ok := inbound["users"].([]any)
	if !ok || len(users) == 0 {
		return deny()
	}
	seen := map[string]bool{}
	kept := make([]any, 0, len(users)-1)
	for _, raw := range users {
		user, valid := raw.(map[string]any)
		if !valid {
			return deny()
		}
		uuid, valid := canonicalVLESSIdentity(stringValue(user["uuid"]))
		accountID, known := byUUID[uuid]
		if !valid || !known || seen[uuid] || stringValue(user["name"]) != accountID {
			return deny()
		}
		seen[uuid] = true
		if uuid != identity {
			kept = append(kept, user)
		}
	}
	// Enforce a bijection; metadata alone is not evidence of the actual users.
	if len(seen) != len(byUUID) || !seen[identity] {
		return deny()
	}
	inbound["users"] = kept
	config.VPNAccounts = append(config.VPNAccounts[:targetIndex], config.VPNAccounts[targetIndex+1:]...)
	if !ValidateRenderedConfig(config).Valid {
		return deny()
	}
	hash, err = hashRenderedConfig(config)
	if err != nil {
		return deny()
	}
	return VLESSRemovalCandidate{RenderedConfig: config, ConfigHash: hash, RemainingUsers: len(kept)}, nil
}

// ValidateVLESSRemovalDelta accepts only the exact expected transformation,
// including unchanged other protocol payloads, listeners, routing, metadata and
// Agent identity. This is structural evidence, never runtime/session evidence.
func ValidateVLESSRemovalDelta(baseline ConfigVersion, target VLESSRemovalTarget, candidateJSON []byte) error {
	expected, err := PrepareVLESSRemoval(baseline, target)
	if err != nil {
		return err
	}
	actual, err := decodeRemovalBaseline(candidateJSON)
	if err != nil {
		return ErrCredentialRemovalUnsafe
	}
	hash, err := hashRenderedConfig(actual)
	if err != nil || hash != expected.ConfigHash {
		return ErrCredentialRemovalUnsafe
	}
	return nil
}

// Reject unknown/unrepresentable envelope fields rather than silently dropping
// them through a typed JSON round trip. Nested runtime maps keep their fields.
// The applied JSONB payload is compared semantically, independent of key order.
func decodeRemovalBaseline(payload []byte) (RenderedConfig, error) {
	var config RenderedConfig
	if err := json.Unmarshal(payload, &config); err != nil {
		return config, ErrCredentialRemovalUnsafe
	}
	encoded, err := json.Marshal(config)
	if err != nil {
		return config, ErrCredentialRemovalUnsafe
	}
	decode := func(raw []byte) (any, error) {
		decoder := json.NewDecoder(bytes.NewReader(raw))
		decoder.UseNumber()
		var value any
		err := decoder.Decode(&value)
		return value, err
	}
	original, err := decode(payload)
	if err != nil {
		return config, ErrCredentialRemovalUnsafe
	}
	roundTrip, err := decode(encoded)
	if err != nil || !reflect.DeepEqual(original, roundTrip) {
		return config, ErrCredentialRemovalUnsafe
	}
	return config, nil
}

func canonicalVLESSIdentity(value string) (string, bool) {
	if len(value) != 36 || value[8] != '-' || value[13] != '-' || value[18] != '-' || value[23] != '-' {
		return "", false
	}
	raw, err := hex.DecodeString(strings.ReplaceAll(value, "-", ""))
	if err != nil || len(raw) != 16 {
		return "", false
	}
	return strings.ToLower(value), true
}
