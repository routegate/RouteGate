package config

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const DefaultPath = "/etc/routegate/agent.yaml"
const DefaultConfigStagingDir = "/var/lib/routegate-agent/configs"
const DefaultActiveConfigPath = "/etc/sing-box/config.json"
const DefaultConfigBackupDir = "/var/lib/routegate-agent/backups"
const DefaultSingBoxPath = "sing-box"
const DefaultSingBoxServiceName = "sing-box"
const DefaultServiceControlEnabled = true
const DefaultTrafficCollectionIntervalSeconds = 60
const DefaultTrafficUsageFilePath = "/var/lib/routegate-agent/traffic-usage.json"
const DefaultClientPresenceIntervalSeconds = 30
const DefaultClientPresenceFilePath = "/var/lib/routegate-agent/client-presence.json"
const DefaultWireGuardStagingDir = "/var/lib/routegate-agent/wireguard-configs"
const DefaultWireGuardActiveConfigPath = "/etc/wireguard/routegate-wg0.conf"
const DefaultWireGuardBackupDir = "/var/lib/routegate-agent/wireguard-backups"
const DefaultWGQuickPath = "wg-quick"
const DefaultWGPath = "wg"
const DefaultWireGuardServiceName = "wg-quick@routegate-wg0"
const DefaultWireGuardInterface = "routegate-wg0"
const DefaultHysteria2StagingDir = "/var/lib/routegate-agent/hysteria2-configs"
const DefaultHysteria2ActiveConfigPath = "/etc/hysteria/config.json"
const DefaultHysteria2BackupDir = "/var/lib/routegate-agent/hysteria2-backups"
const DefaultHysteria2Path = "hysteria"
const DefaultHysteria2ServiceName = "hysteria-server"
const DefaultSSPath = "ss"
const DefaultMTProtoStagingDir = "/var/lib/routegate-agent/mtproto-configs"
const DefaultMTProtoActiveConfigPath = "/etc/routegate-mtproto/config.toml"
const DefaultMTProtoBackupDir = "/var/lib/routegate-agent/mtproto-backups"
const DefaultMTGPath = "mtg"
const DefaultMTProtoServiceName = "routegate-mtproto"

type Config struct {
	// Disposable-test-only gate; never enabled by the installer or production defaults.
	ExperimentalRuntimeMutationFencing bool
	ManagerURL                       string
	RegistrationToken                string
	AgentID                          string
	ServerID                         string
	AgentToken                       string
	HeartbeatIntervalSeconds         int
	ConfigStagingDir                 string
	ActiveConfigPath                 string
	ConfigBackupDir                  string
	SingBoxPath                      string
	SingBoxServiceName               string
	ServiceControlEnabled            bool
	TrafficCollectionEnabled         bool
	TrafficCollectionIntervalSeconds int
	TrafficUsageFilePath             string
	ClientPresenceEnabled            bool
	ClientPresenceIntervalSeconds    int
	ClientPresenceFilePath           string
	WireGuardStagingDir              string
	WireGuardActiveConfigPath        string
	WireGuardBackupDir               string
	WGQuickPath                      string
	WGPath                           string
	WireGuardServiceName             string
	WireGuardInterface               string
	Hysteria2StagingDir              string
	Hysteria2ActiveConfigPath        string
	Hysteria2BackupDir               string
	Hysteria2Path                    string
	Hysteria2ServiceName             string
	SSPath                           string
	MTProtoStagingDir                string
	MTProtoActiveConfigPath          string
	MTProtoBackupDir                 string
	MTGPath                          string
	MTProtoServiceName               string
}

func Load(path string) (Config, error) {
	file, err := os.Open(path)
	if err != nil {
		return Config{}, fmt.Errorf("read config: %w", err)
	}
	defer file.Close()
	cfg := Config{ServiceControlEnabled: DefaultServiceControlEnabled, ClientPresenceEnabled: true}
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := stripComment(scanner.Text())
		if strings.TrimSpace(line) == "" {
			continue
		}
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			return Config{}, fmt.Errorf("parse config: invalid line %q", scanner.Text())
		}
		key = strings.TrimSpace(key)
		value = trimYAMLScalar(value)
		switch key {
		case "experimental_runtime_mutation_fencing":
			parsed, err := strconv.ParseBool(value)
			if err != nil {
				return Config{}, errors.New("invalid experimental_runtime_mutation_fencing")
			}
			cfg.ExperimentalRuntimeMutationFencing = parsed
		case "manager_url":
			cfg.ManagerURL = value
		case "registration_token":
			cfg.RegistrationToken = value
		case "agent_id":
			cfg.AgentID = value
		case "server_id":
			cfg.ServerID = value
		case "agent_token":
			cfg.AgentToken = value
		case "heartbeat_interval_seconds":
			if value == "" {
				continue
			}
			parsed, err := strconv.Atoi(value)
			if err != nil {
				return Config{}, fmt.Errorf("parse heartbeat_interval_seconds: %w", err)
			}
			cfg.HeartbeatIntervalSeconds = parsed
		case "config_staging_dir":
			cfg.ConfigStagingDir = value
		case "active_config_path":
			cfg.ActiveConfigPath = value
		case "config_backup_dir":
			cfg.ConfigBackupDir = value
		case "sing_box_path":
			cfg.SingBoxPath = value
		case "sing_box_service_name":
			cfg.SingBoxServiceName = value
		case "service_control_enabled":
			if value == "" {
				continue
			}
			parsed, err := strconv.ParseBool(value)
			if err != nil {
				return Config{}, fmt.Errorf("parse service_control_enabled: %w", err)
			}
			cfg.ServiceControlEnabled = parsed
		case "traffic_collection_enabled":
			if value == "" {
				continue
			}
			parsed, err := strconv.ParseBool(value)
			if err != nil {
				return Config{}, fmt.Errorf("parse traffic_collection_enabled: %w", err)
			}
			cfg.TrafficCollectionEnabled = parsed
		case "traffic_collection_interval_seconds":
			if value == "" {
				continue
			}
			parsed, err := strconv.Atoi(value)
			if err != nil {
				return Config{}, fmt.Errorf("parse traffic_collection_interval_seconds: %w", err)
			}
			cfg.TrafficCollectionIntervalSeconds = parsed
		case "traffic_usage_file_path":
			cfg.TrafficUsageFilePath = value
		case "client_presence_enabled":
			if value == "" { continue }
			parsed, err := strconv.ParseBool(value)
			if err != nil { return Config{}, fmt.Errorf("parse client_presence_enabled: %w", err) }
			cfg.ClientPresenceEnabled = parsed
		case "client_presence_interval_seconds":
			if value == "" { continue }
			parsed, err := strconv.Atoi(value)
			if err != nil { return Config{}, fmt.Errorf("parse client_presence_interval_seconds: %w", err) }
			cfg.ClientPresenceIntervalSeconds = parsed
		case "client_presence_file_path":
			cfg.ClientPresenceFilePath = value
		case "wireguard_staging_dir":
			cfg.WireGuardStagingDir = value
		case "wireguard_active_config_path":
			cfg.WireGuardActiveConfigPath = value
		case "wireguard_backup_dir":
			cfg.WireGuardBackupDir = value
		case "wg_quick_path":
			cfg.WGQuickPath = value
		case "wg_path":
			cfg.WGPath = value
		case "wireguard_service_name":
			cfg.WireGuardServiceName = value
		case "wireguard_interface":
			cfg.WireGuardInterface = value
		case "hysteria2_staging_dir":
			cfg.Hysteria2StagingDir = value
		case "hysteria2_active_config_path":
			cfg.Hysteria2ActiveConfigPath = value
		case "hysteria2_backup_dir":
			cfg.Hysteria2BackupDir = value
		case "hysteria2_path":
			cfg.Hysteria2Path = value
		case "hysteria2_service_name":
			cfg.Hysteria2ServiceName = value
		case "ss_path":
			cfg.SSPath = value
		case "mtproto_staging_dir":
			cfg.MTProtoStagingDir = value
		case "mtproto_active_config_path":
			cfg.MTProtoActiveConfigPath = value
		case "mtproto_backup_dir":
			cfg.MTProtoBackupDir = value
		case "mtg_path":
			cfg.MTGPath = value
		case "mtproto_service_name":
			cfg.MTProtoServiceName = value
		}
	}
	if err := scanner.Err(); err != nil {
		return Config{}, fmt.Errorf("read config: %w", err)
	}
	cfg.ManagerURL = strings.TrimRight(strings.TrimSpace(cfg.ManagerURL), "/")
	cfg.RegistrationToken = strings.TrimSpace(cfg.RegistrationToken)
	cfg.AgentID = strings.TrimSpace(cfg.AgentID)
	cfg.ServerID = strings.TrimSpace(cfg.ServerID)
	cfg.AgentToken = strings.TrimSpace(cfg.AgentToken)
	cfg.ConfigStagingDir = strings.TrimSpace(cfg.ConfigStagingDir)
	cfg.ActiveConfigPath = strings.TrimSpace(cfg.ActiveConfigPath)
	cfg.ConfigBackupDir = strings.TrimSpace(cfg.ConfigBackupDir)
	cfg.SingBoxPath = strings.TrimSpace(cfg.SingBoxPath)
	cfg.SingBoxServiceName = strings.TrimSpace(cfg.SingBoxServiceName)
	cfg.TrafficUsageFilePath = strings.TrimSpace(cfg.TrafficUsageFilePath)
	cfg.ClientPresenceFilePath = strings.TrimSpace(cfg.ClientPresenceFilePath)
	cfg.WireGuardStagingDir = strings.TrimSpace(cfg.WireGuardStagingDir)
	cfg.WireGuardActiveConfigPath = strings.TrimSpace(cfg.WireGuardActiveConfigPath)
	cfg.WireGuardBackupDir = strings.TrimSpace(cfg.WireGuardBackupDir)
	cfg.WGQuickPath = strings.TrimSpace(cfg.WGQuickPath)
	cfg.WGPath = strings.TrimSpace(cfg.WGPath)
	cfg.WireGuardServiceName = strings.TrimSpace(cfg.WireGuardServiceName)
	cfg.WireGuardInterface = strings.TrimSpace(cfg.WireGuardInterface)
	cfg.Hysteria2StagingDir = strings.TrimSpace(cfg.Hysteria2StagingDir)
	cfg.Hysteria2ActiveConfigPath = strings.TrimSpace(cfg.Hysteria2ActiveConfigPath)
	cfg.Hysteria2BackupDir = strings.TrimSpace(cfg.Hysteria2BackupDir)
	cfg.Hysteria2Path = strings.TrimSpace(cfg.Hysteria2Path)
	cfg.Hysteria2ServiceName = strings.TrimSpace(cfg.Hysteria2ServiceName)
	cfg.SSPath = strings.TrimSpace(cfg.SSPath)
	cfg.MTProtoStagingDir = strings.TrimSpace(cfg.MTProtoStagingDir)
	cfg.MTProtoActiveConfigPath = strings.TrimSpace(cfg.MTProtoActiveConfigPath)
	cfg.MTProtoBackupDir = strings.TrimSpace(cfg.MTProtoBackupDir)
	cfg.MTGPath = strings.TrimSpace(cfg.MTGPath)
	cfg.MTProtoServiceName = strings.TrimSpace(cfg.MTProtoServiceName)
	if cfg.HeartbeatIntervalSeconds <= 0 {
		cfg.HeartbeatIntervalSeconds = 30
	}
	if cfg.ConfigStagingDir == "" {
		cfg.ConfigStagingDir = DefaultConfigStagingDir
	}
	if cfg.ActiveConfigPath == "" {
		cfg.ActiveConfigPath = DefaultActiveConfigPath
	}
	if cfg.ConfigBackupDir == "" {
		cfg.ConfigBackupDir = DefaultConfigBackupDir
	}
	if cfg.SingBoxPath == "" {
		cfg.SingBoxPath = DefaultSingBoxPath
	}
	if cfg.SingBoxServiceName == "" {
		cfg.SingBoxServiceName = DefaultSingBoxServiceName
	}
	if cfg.TrafficCollectionIntervalSeconds <= 0 {
		cfg.TrafficCollectionIntervalSeconds = DefaultTrafficCollectionIntervalSeconds
	}
	if cfg.TrafficUsageFilePath == "" {
		cfg.TrafficUsageFilePath = DefaultTrafficUsageFilePath
	}
	if cfg.ClientPresenceIntervalSeconds <= 0 { cfg.ClientPresenceIntervalSeconds = DefaultClientPresenceIntervalSeconds }
	if cfg.ClientPresenceFilePath == "" { cfg.ClientPresenceFilePath = DefaultClientPresenceFilePath }
	if cfg.WireGuardStagingDir == "" { cfg.WireGuardStagingDir = DefaultWireGuardStagingDir }
	if cfg.WireGuardActiveConfigPath == "" { cfg.WireGuardActiveConfigPath = DefaultWireGuardActiveConfigPath }
	if cfg.WireGuardBackupDir == "" { cfg.WireGuardBackupDir = DefaultWireGuardBackupDir }
	if cfg.WGQuickPath == "" { cfg.WGQuickPath = DefaultWGQuickPath }
	if cfg.WGPath == "" { cfg.WGPath = DefaultWGPath }
	if cfg.WireGuardServiceName == "" { cfg.WireGuardServiceName = DefaultWireGuardServiceName }
	if cfg.WireGuardInterface == "" { cfg.WireGuardInterface = DefaultWireGuardInterface }
	if cfg.Hysteria2StagingDir == "" { cfg.Hysteria2StagingDir = DefaultHysteria2StagingDir }
	if cfg.Hysteria2ActiveConfigPath == "" { cfg.Hysteria2ActiveConfigPath = DefaultHysteria2ActiveConfigPath }
	if cfg.Hysteria2BackupDir == "" { cfg.Hysteria2BackupDir = DefaultHysteria2BackupDir }
	if cfg.Hysteria2Path == "" { cfg.Hysteria2Path = DefaultHysteria2Path }
	if cfg.Hysteria2ServiceName == "" { cfg.Hysteria2ServiceName = DefaultHysteria2ServiceName }
	if cfg.SSPath == "" { cfg.SSPath = DefaultSSPath }
	if cfg.MTProtoStagingDir == "" { cfg.MTProtoStagingDir = DefaultMTProtoStagingDir }
	if cfg.MTProtoActiveConfigPath == "" { cfg.MTProtoActiveConfigPath = DefaultMTProtoActiveConfigPath }
	if cfg.MTProtoBackupDir == "" { cfg.MTProtoBackupDir = DefaultMTProtoBackupDir }
	if cfg.MTGPath == "" { cfg.MTGPath = DefaultMTGPath }
	if cfg.MTProtoServiceName == "" { cfg.MTProtoServiceName = DefaultMTProtoServiceName }
	if cfg.ManagerURL == "" {
		return Config{}, errors.New("manager_url is required")
	}
	return cfg, nil
}

func (c Config) HeartbeatInterval() time.Duration {
	return time.Duration(c.HeartbeatIntervalSeconds) * time.Second
}

func (c Config) TrafficCollectionInterval() time.Duration {
	return time.Duration(c.TrafficCollectionIntervalSeconds) * time.Second
}

func (c Config) ClientPresenceInterval() time.Duration {
	return time.Duration(c.ClientPresenceIntervalSeconds) * time.Second
}

func (c Config) Save(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create config dir: %w", err)
	}
	stagingDir := c.ConfigStagingDir
	if stagingDir == "" {
		stagingDir = DefaultConfigStagingDir
	}
	activeConfigPath := c.ActiveConfigPath
	if activeConfigPath == "" {
		activeConfigPath = DefaultActiveConfigPath
	}
	backupDir := c.ConfigBackupDir
	if backupDir == "" {
		backupDir = DefaultConfigBackupDir
	}
	singBoxPath := c.SingBoxPath
	if singBoxPath == "" {
		singBoxPath = DefaultSingBoxPath
	}
	serviceName := c.SingBoxServiceName
	if serviceName == "" {
		serviceName = DefaultSingBoxServiceName
	}
	serviceControlEnabled := c.ServiceControlEnabled
	trafficInterval := c.TrafficCollectionIntervalSeconds
	if trafficInterval <= 0 {
		trafficInterval = DefaultTrafficCollectionIntervalSeconds
	}
	trafficUsageFilePath := c.TrafficUsageFilePath
	if trafficUsageFilePath == "" {
		trafficUsageFilePath = DefaultTrafficUsageFilePath
	}
	presenceInterval := c.ClientPresenceIntervalSeconds
	if presenceInterval <= 0 { presenceInterval = DefaultClientPresenceIntervalSeconds }
	presenceFilePath := defaultString(c.ClientPresenceFilePath, DefaultClientPresenceFilePath)
	wireGuardStagingDir := defaultString(c.WireGuardStagingDir, DefaultWireGuardStagingDir)
	wireGuardActiveConfigPath := defaultString(c.WireGuardActiveConfigPath, DefaultWireGuardActiveConfigPath)
	wireGuardBackupDir := defaultString(c.WireGuardBackupDir, DefaultWireGuardBackupDir)
	wgQuickPath := defaultString(c.WGQuickPath, DefaultWGQuickPath)
	wgPath := defaultString(c.WGPath, DefaultWGPath)
	wireGuardServiceName := defaultString(c.WireGuardServiceName, DefaultWireGuardServiceName)
	wireGuardInterface := defaultString(c.WireGuardInterface, DefaultWireGuardInterface)
	hysteria2StagingDir := defaultString(c.Hysteria2StagingDir, DefaultHysteria2StagingDir)
	hysteria2ActiveConfigPath := defaultString(c.Hysteria2ActiveConfigPath, DefaultHysteria2ActiveConfigPath)
	hysteria2BackupDir := defaultString(c.Hysteria2BackupDir, DefaultHysteria2BackupDir)
	hysteria2Path := defaultString(c.Hysteria2Path, DefaultHysteria2Path)
	hysteria2ServiceName := defaultString(c.Hysteria2ServiceName, DefaultHysteria2ServiceName)
	ssPath := defaultString(c.SSPath, DefaultSSPath)
	mtprotoStagingDir := defaultString(c.MTProtoStagingDir, DefaultMTProtoStagingDir)
	mtprotoActiveConfigPath := defaultString(c.MTProtoActiveConfigPath, DefaultMTProtoActiveConfigPath)
	mtprotoBackupDir := defaultString(c.MTProtoBackupDir, DefaultMTProtoBackupDir)
	mtgPath := defaultString(c.MTGPath, DefaultMTGPath)
	mtprotoServiceName := defaultString(c.MTProtoServiceName, DefaultMTProtoServiceName)

	var output strings.Builder
	// Preserve explicit test opt-in across registration; omit ordinary defaults.
	if c.ExperimentalRuntimeMutationFencing {
		fmt.Fprintln(&output, "experimental_runtime_mutation_fencing: true")
	}
	fmt.Fprintf(&output, "manager_url: %q\n", c.ManagerURL)
	if c.RegistrationToken != "" {
		fmt.Fprintf(&output, "registration_token: %q\n", c.RegistrationToken)
	}
	fmt.Fprintf(&output, "agent_id: %q\nserver_id: %q\nagent_token: %q\n", c.AgentID, c.ServerID, c.AgentToken)
	fmt.Fprintf(&output, "heartbeat_interval_seconds: %d\n", c.HeartbeatIntervalSeconds)
	fmt.Fprintf(&output, "config_staging_dir: %q\nactive_config_path: %q\nconfig_backup_dir: %q\n", stagingDir, activeConfigPath, backupDir)
	fmt.Fprintf(&output, "sing_box_path: %q\nsing_box_service_name: %q\n", singBoxPath, serviceName)
	fmt.Fprintf(&output, "wireguard_staging_dir: %q\nwireguard_active_config_path: %q\nwireguard_backup_dir: %q\n", wireGuardStagingDir, wireGuardActiveConfigPath, wireGuardBackupDir)
	fmt.Fprintf(&output, "wg_quick_path: %q\nwg_path: %q\nwireguard_service_name: %q\nwireguard_interface: %q\n", wgQuickPath, wgPath, wireGuardServiceName, wireGuardInterface)
	fmt.Fprintf(&output, "hysteria2_staging_dir: %q\nhysteria2_active_config_path: %q\nhysteria2_backup_dir: %q\n", hysteria2StagingDir, hysteria2ActiveConfigPath, hysteria2BackupDir)
	fmt.Fprintf(&output, "hysteria2_path: %q\nhysteria2_service_name: %q\nss_path: %q\n", hysteria2Path, hysteria2ServiceName, ssPath)
	fmt.Fprintf(&output, "mtproto_staging_dir: %q\nmtproto_active_config_path: %q\nmtproto_backup_dir: %q\n", mtprotoStagingDir, mtprotoActiveConfigPath, mtprotoBackupDir)
	fmt.Fprintf(&output, "mtg_path: %q\nmtproto_service_name: %q\n", mtgPath, mtprotoServiceName)
	fmt.Fprintf(&output, "service_control_enabled: %t\ntraffic_collection_enabled: %t\ntraffic_collection_interval_seconds: %d\ntraffic_usage_file_path: %q\n", serviceControlEnabled, c.TrafficCollectionEnabled, trafficInterval, trafficUsageFilePath)
	fmt.Fprintf(&output, "client_presence_enabled: %t\nclient_presence_interval_seconds: %d\nclient_presence_file_path: %q\n", c.ClientPresenceEnabled, presenceInterval, presenceFilePath)
	data := output.String()

	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		return fmt.Errorf("write config: %w", err)
	}
	return nil
}

func defaultString(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}

func (c Config) HasAgentCredentials() bool  { return c.AgentToken != "" }
func (c Config) HasRegistrationToken() bool { return c.RegistrationToken != "" }

func stripComment(line string) string {
	inSingle, inDouble := false, false
	for i, r := range line {
		switch r {
		case '\'':
			if !inDouble {
				inSingle = !inSingle
			}
		case '"':
			if !inSingle {
				inDouble = !inDouble
			}
		case '#':
			if !inSingle && !inDouble {
				return line[:i]
			}
		}
	}
	return line
}

func trimYAMLScalar(value string) string {
	value = strings.TrimSpace(value)
	if len(value) >= 2 {
		if unquoted, err := strconv.Unquote(value); err == nil {
			return unquoted
		}
		if value[0] == '\'' && value[len(value)-1] == '\'' {
			return strings.ReplaceAll(value[1:len(value)-1], "''", "'")
		}
	}
	return value
}
