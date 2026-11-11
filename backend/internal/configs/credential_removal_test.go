package configs

import (
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"
)

func removalFixture(t *testing.T) (ConfigVersion, VLESSRemovalTarget) {
	t.Helper()
	key, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	config := buildRenderedConfig(ServerConfigInfo{
		ID: "node", Name: "hybrid", DeploymentRole: "hybrid", Status: "active", PublicIP: "203.0.113.10",
		VLESSPort: 8443, VPNProtocol: "vless", RealityPrivateKey: base64.RawURLEncoding.EncodeToString(key.Bytes()),
		RealityPublicKey: base64.RawURLEncoding.EncodeToString(key.PublicKey().Bytes()),
		RealityShortID:   "0123456789abcdef", RealityServerName: "example.com",
		Agent: &AgentConfigInfo{ID: "agent", Status: "online", AgentVersion: "test"},
		VPNAccounts: []VPNAccountConfigInfo{
			{ID: "remove", DisplayName: "Remove", Status: "active", VPNProtocol: "vless", VLESSUUID: "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa", VLESSFlow: "xtls-rprx-vision"},
			{ID: "keep", DisplayName: "Keep", Status: "active", VPNProtocol: "vless", VLESSUUID: "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb", VLESSFlow: "xtls-rprx-vision"},
		},
	}, time.Date(2026, 10, 9, 0, 0, 0, 0, time.UTC))
	config.SingBox.Log.Output = ""
	baseline := ConfigVersion{ID: "version", ServerID: "node", Status: StatusApplied, AppliedAt: &config.Metadata.RenderedAt}
	baseline = pinRemovalConfig(t, baseline, config)
	return baseline, VLESSRemovalTarget{ServerID: "node", AccountID: "remove", VersionID: "version", ConfigHash: baseline.ConfigHash, VLESSUUID: config.VPNAccounts[0].VLESSUUID}
}

func pinRemovalConfig(t *testing.T, version ConfigVersion, config RenderedConfig) ConfigVersion {
	t.Helper()
	version.RenderedConfig = mustMarshalRaw(t, config)
	var err error
	version.ConfigHash, err = hashRenderedConfig(config)
	if err != nil {
		t.Fatal(err)
	}
	return version
}

func TestVLESSRemovalPreservesAppliedBaseline(t *testing.T) {
	baseline, target := removalFixture(t)
	before := append([]byte(nil), baseline.RenderedConfig...)
	var original RenderedConfig
	if err := json.Unmarshal(before, &original); err != nil {
		t.Fatal(err)
	}
	// Preserve independent other-protocol payloads and unknown nested runtime
	// options; the planner must not regenerate any part from desired settings.
	original.WireGuard = "applied wireguard payload\n"
	original.Hysteria2 = "applied hysteria payload\n"
	original.SingBox.Inbounds = append(original.SingBox.Inbounds, map[string]any{"type": "shadowsocks", "tag": "ss-in", "listen_port": 9443, "users": []any{map[string]any{"name": "ss-account", "password": "test-ss-key"}}, "custom_field": "preserve"})
	baseline = pinRemovalConfig(t, baseline, original)
	target.ConfigHash = baseline.ConfigHash
	before = append([]byte(nil), baseline.RenderedConfig...)
	result, err := PrepareVLESSRemoval(baseline, target)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, []byte(baseline.RenderedConfig)) {
		t.Fatal("mutated caller baseline")
	}
	if result.RemainingUsers != 1 || len(result.RenderedConfig.VPNAccounts) != 1 || result.RenderedConfig.VPNAccounts[0].ID != "keep" {
		t.Fatal("wrong account membership")
	}
	var expected RenderedConfig
	if err = json.Unmarshal(before, &expected); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(expected.Server, result.RenderedConfig.Server) || !reflect.DeepEqual(expected.Agent, result.RenderedConfig.Agent) ||
		!reflect.DeepEqual(expected.Metadata, result.RenderedConfig.Metadata) || !reflect.DeepEqual(expected.RoutingProfile, result.RenderedConfig.RoutingProfile) ||
		expected.WireGuard != result.RenderedConfig.WireGuard || expected.Hysteria2 != result.RenderedConfig.Hysteria2 {
		t.Fatal("unrelated envelope changed")
	}
	if !reflect.DeepEqual(expected.SingBox.Inbounds[1], result.RenderedConfig.SingBox.Inbounds[1]) ||
		!reflect.DeepEqual(expected.SingBox.Outbounds, result.RenderedConfig.SingBox.Outbounds) ||
		!reflect.DeepEqual(expected.SingBox.Route, result.RenderedConfig.SingBox.Route) {
		t.Fatal("unrelated runtime changed")
	}
	beforeUsers := expected.SingBox.Inbounds[0]["users"].([]any)
	afterUsers := result.RenderedConfig.SingBox.Inbounds[0]["users"].([]any)
	if len(afterUsers) != 1 || !reflect.DeepEqual(beforeUsers[1], afterUsers[0]) {
		t.Fatal("unrelated authenticator changed")
	}
	delete(expected.SingBox.Inbounds[0], "users")
	afterInbound := result.RenderedConfig.SingBox.Inbounds[0]
	kept := afterInbound["users"]
	delete(afterInbound, "users")
	if !reflect.DeepEqual(expected.SingBox.Inbounds[0], afterInbound) {
		t.Fatal("listener settings changed")
	}
	afterInbound["users"] = kept
	if err = ValidateVLESSRemovalDelta(baseline, target, mustMarshalRaw(t, result.RenderedConfig)); err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(result)
	if err != nil || string(encoded) != "{}" {
		t.Fatal("candidate exposed in JSON")
	}
	// A distinct invocation is independent of mutations to the first result.
	afterUsers[0].(map[string]any)["uuid"] = "tampered"
	if _, err = PrepareVLESSRemoval(baseline, target); err != nil {
		t.Fatal("candidate aliased baseline")
	}
}

func TestVLESSRemovalRejectsUnsafeIdentityAndBaseline(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*ConfigVersion, *VLESSRemovalTarget, *RenderedConfig)
	}{
		{"different_node", func(v *ConfigVersion, x *VLESSRemovalTarget, c *RenderedConfig) { x.ServerID = "other" }},
		{"different_version", func(v *ConfigVersion, x *VLESSRemovalTarget, c *RenderedConfig) { x.VersionID = "other" }},
		{"different_hash", func(v *ConfigVersion, x *VLESSRemovalTarget, c *RenderedConfig) { x.ConfigHash = "other" }},
		{"not_applied", func(v *ConfigVersion, x *VLESSRemovalTarget, c *RenderedConfig) { v.Status = StatusValidated }},
		{"missing_apply_time", func(v *ConfigVersion, x *VLESSRemovalTarget, c *RenderedConfig) { v.AppliedAt = nil }},
		{"wrong_envelope_node", func(v *ConfigVersion, x *VLESSRemovalTarget, c *RenderedConfig) { c.Server.ID = "other" }},
		{"management", func(v *ConfigVersion, x *VLESSRemovalTarget, c *RenderedConfig) {
			c.Server.DeploymentRole = "management"
		}},
		{"unknown_role", func(v *ConfigVersion, x *VLESSRemovalTarget, c *RenderedConfig) { c.Server.DeploymentRole = "" }},
		{"no_agent", func(v *ConfigVersion, x *VLESSRemovalTarget, c *RenderedConfig) { c.Agent = nil }},
		{"no_reality", func(v *ConfigVersion, x *VLESSRemovalTarget, c *RenderedConfig) { c.Metadata.RealityEnabled = false }},
		{"unsupported_descriptor", func(v *ConfigVersion, x *VLESSRemovalTarget, c *RenderedConfig) {
			c.Metadata.VPNCore = ConfigVPNCore{}
			c.Metadata.VPNCores = nil
		}},
		{"wrong_security_descriptor", func(v *ConfigVersion, x *VLESSRemovalTarget, c *RenderedConfig) {
			c.Metadata.VPNCore.Security = "none"
			c.Metadata.VPNCores = nil
		}},
		{"invalid_reality", func(v *ConfigVersion, x *VLESSRemovalTarget, c *RenderedConfig) { delete(c.SingBox.Inbounds[0], "tls") }},
		{"empty_listener_authority", func(v *ConfigVersion, x *VLESSRemovalTarget, c *RenderedConfig) { c.Metadata.TransferEmptyUsers = true }},
		{"shared_mtproto", func(v *ConfigVersion, x *VLESSRemovalTarget, c *RenderedConfig) { c.MTProto = "node-wide secret" }},
		{"absent_account", func(v *ConfigVersion, x *VLESSRemovalTarget, c *RenderedConfig) { x.AccountID = "other" }},
		{"wrong_uuid", func(v *ConfigVersion, x *VLESSRemovalTarget, c *RenderedConfig) {
			x.VLESSUUID = c.VPNAccounts[1].VLESSUUID
		}},
		{"invalid_uuid", func(v *ConfigVersion, x *VLESSRemovalTarget, c *RenderedConfig) { x.VLESSUUID = "invalid" }},
		{"duplicate_account", func(v *ConfigVersion, x *VLESSRemovalTarget, c *RenderedConfig) {
			c.VPNAccounts = append(c.VPNAccounts, c.VPNAccounts[0])
		}},
		{"shared_uuid_case_insensitive", func(v *ConfigVersion, x *VLESSRemovalTarget, c *RenderedConfig) {
			c.VPNAccounts[1].VLESSUUID = "AAAAAAAA-AAAA-AAAA-AAAA-AAAAAAAAAAAA"
		}},
		{"target_multiple_protocols", func(v *ConfigVersion, x *VLESSRemovalTarget, c *RenderedConfig) {
			c.VPNAccounts[0].Protocols = []string{"vless", "hysteria2"}
		}},
		{"target_hidden_protocol_identity", func(v *ConfigVersion, x *VLESSRemovalTarget, c *RenderedConfig) {
			c.VPNAccounts[0].Hysteria2Username = "remove"
		}},
		{"duplicate_listener", func(v *ConfigVersion, x *VLESSRemovalTarget, c *RenderedConfig) {
			c.SingBox.Inbounds = append(c.SingBox.Inbounds, c.SingBox.Inbounds[0])
		}},
		{"custom_listener", func(v *ConfigVersion, x *VLESSRemovalTarget, c *RenderedConfig) {
			c.SingBox.Inbounds[0]["tag"] = "custom"
		}},
		{"custom_transport", func(v *ConfigVersion, x *VLESSRemovalTarget, c *RenderedConfig) {
			c.SingBox.Inbounds[0]["transport"] = map[string]any{"type": "ws"}
		}},
		{"unknown_runtime_uuid", func(v *ConfigVersion, x *VLESSRemovalTarget, c *RenderedConfig) {
			c.SingBox.Inbounds[0]["users"].([]any)[0].(map[string]any)["uuid"] = "cccccccc-cccc-cccc-cccc-cccccccccccc"
		}},
		{"runtime_name_mismatch", func(v *ConfigVersion, x *VLESSRemovalTarget, c *RenderedConfig) {
			c.SingBox.Inbounds[0]["users"].([]any)[0].(map[string]any)["name"] = "keep"
		}},
		{"duplicate_runtime_uuid", func(v *ConfigVersion, x *VLESSRemovalTarget, c *RenderedConfig) {
			users := c.SingBox.Inbounds[0]["users"].([]any)
			c.SingBox.Inbounds[0]["users"] = append(users, users[0])
		}},
		{"metadata_without_runtime_user", func(v *ConfigVersion, x *VLESSRemovalTarget, c *RenderedConfig) {
			c.SingBox.Inbounds[0]["users"] = c.SingBox.Inbounds[0]["users"].([]any)[:1]
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			baseline, target := removalFixture(t)
			var config RenderedConfig
			if err := json.Unmarshal(baseline.RenderedConfig, &config); err != nil {
				t.Fatal(err)
			}
			tc.mutate(&baseline, &target, &config)
			// Re-pin mutated fixtures so these tests exercise structural guards,
			// rather than succeeding merely because the payload hash differs.
			prior := baseline.ConfigHash
			baseline = pinRemovalConfig(t, baseline, config)
			if target.ConfigHash == prior {
				target.ConfigHash = baseline.ConfigHash
			}
			result, err := PrepareVLESSRemoval(baseline, target)
			if !errors.Is(err, ErrCredentialRemovalUnsafe) || result.ConfigHash != "" {
				t.Fatal("unsafe candidate accepted")
			}
			if err.Error() != ErrCredentialRemovalUnsafe.Error() {
				t.Fatal("unsafe diagnostic text")
			}
		})
	}
}

func TestVLESSRemovalDeltaRejectsUnrelatedChanges(t *testing.T) {
	baseline, target := removalFixture(t)
	for _, tc := range []struct {
		name   string
		mutate func(*RenderedConfig)
	}{
		{"listener_port", func(c *RenderedConfig) { c.SingBox.Inbounds[0]["listen_port"] = 9443 }},
		{"other_uuid", func(c *RenderedConfig) {
			c.SingBox.Inbounds[0]["users"].([]any)[0].(map[string]any)["uuid"] = "cccccccc-cccc-cccc-cccc-cccccccccccc"
		}},
		{"other_status", func(c *RenderedConfig) { c.VPNAccounts[0].Status = "revoked" }},
		{"endpoint", func(c *RenderedConfig) { c.Server.PublicIP = "203.0.113.20" }},
		{"tls", func(c *RenderedConfig) {
			c.SingBox.Inbounds[0]["tls"].(map[string]any)["server_name"] = "other.example"
		}},
		{"routing", func(c *RenderedConfig) { c.SingBox.Route.Final = "block" }},
		{"other_protocol", func(c *RenderedConfig) { c.WireGuard = "changed" }},
		{"agent", func(c *RenderedConfig) { c.Agent.ID = "other-agent" }},
		{"timestamp", func(c *RenderedConfig) { c.Metadata.RenderedAt = c.Metadata.RenderedAt.Add(time.Second) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			candidate, err := PrepareVLESSRemoval(baseline, target)
			if err != nil {
				t.Fatal(err)
			}
			tc.mutate(&candidate.RenderedConfig)
			if !errors.Is(ValidateVLESSRemovalDelta(baseline, target, mustMarshalRaw(t, candidate.RenderedConfig)), ErrCredentialRemovalUnsafe) {
				t.Fatal("unrelated delta accepted")
			}
		})
	}
}

func TestVLESSRemovalLastUserRequiresSeparateApplyAuthority(t *testing.T) {
	baseline, target := removalFixture(t)
	var config RenderedConfig
	if err := json.Unmarshal(baseline.RenderedConfig, &config); err != nil {
		t.Fatal(err)
	}
	config.VPNAccounts = config.VPNAccounts[:1]
	config.SingBox.Inbounds[0]["users"] = config.SingBox.Inbounds[0]["users"].([]any)[:1]
	baseline = pinRemovalConfig(t, baseline, config)
	target.ConfigHash = baseline.ConfigHash
	result, err := PrepareVLESSRemoval(baseline, target)
	if err != nil {
		t.Fatal(err)
	}
	if result.RemainingUsers != 0 || len(result.RenderedConfig.VPNAccounts) != 0 || result.RenderedConfig.Metadata.TransferEmptyUsers {
		t.Fatal("wrong empty listener semantics")
	}
	version := ConfigVersion{ConfigHash: result.ConfigHash, RenderedConfig: mustMarshalRaw(t, result.RenderedConfig)}
	if !errors.Is(ensureConfigVersionSafeForApply(version), ErrConfigApplyUnsafe) {
		t.Fatal("ordinary apply accepted empty removal candidate")
	}
}

func TestVLESSRemovalRejectsUnknownOrTamperedEnvelope(t *testing.T) {
	for _, unknown := range []bool{false, true} {
		baseline, target := removalFixture(t)
		var envelope map[string]any
		if err := json.Unmarshal(baseline.RenderedConfig, &envelope); err != nil {
			t.Fatal(err)
		}
		if unknown {
			envelope["futureProtocol"] = map[string]any{"credential": "test-secret"}
		} else {
			envelope["server"].(map[string]any)["publicIp"] = "203.0.113.99"
		}
		baseline.RenderedConfig = mustMarshalRaw(t, envelope)
		if _, err := PrepareVLESSRemoval(baseline, target); !errors.Is(err, ErrCredentialRemovalUnsafe) {
			t.Fatal("lossy/tampered envelope accepted")
		}
	}
}
