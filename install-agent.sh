#!/usr/bin/env bash

set -Eeuo pipefail
IFS=$'\n\t'
umask 077

ROUTEGATE_REPOSITORY="${ROUTEGATE_REPOSITORY:-ikaevus/RouteGate}"
ROUTEGATE_VERSION="${ROUTEGATE_VERSION:-latest}"
ROUTEGATE_MANAGER_URL="${ROUTEGATE_MANAGER_URL:-}"
ROUTEGATE_REGISTRATION_TOKEN="${ROUTEGATE_REGISTRATION_TOKEN:-}"
ROUTEGATE_BUNDLE_FILE="${ROUTEGATE_BUNDLE_FILE:-}"
ROUTEGATE_CHECKSUM_FILE="${ROUTEGATE_CHECKSUM_FILE:-}"
ROUTEGATE_BUNDLE_URL="${ROUTEGATE_BUNDLE_URL:-}"
ROUTEGATE_CHECKSUM_URL="${ROUTEGATE_CHECKSUM_URL:-}"
ROUTEGATE_BUNDLE_BASE_URL="${ROUTEGATE_BUNDLE_BASE_URL:-}"

ROUTEGATE_AGENT_CONFIG="${ROUTEGATE_AGENT_CONFIG:-/etc/routegate/agent.yaml}"
ROUTEGATE_AGENT_BINARY="${ROUTEGATE_AGENT_BINARY:-/usr/local/bin/routegate-agent}"
ROUTEGATE_AGENT_SERVICE="${ROUTEGATE_AGENT_SERVICE:-/etc/systemd/system/routegate-agent.service}"
ROUTEGATE_INSTALLER_STATE_DIR="${ROUTEGATE_INSTALLER_STATE_DIR:-/var/lib/routegate-agent-installer}"
ROUTEGATE_INSTALLER_STATE_FILE="${ROUTEGATE_INSTALLER_STATE_FILE:-${ROUTEGATE_INSTALLER_STATE_DIR}/state.env}"
ROUTEGATE_INSTALLER_LOG_FILE="${ROUTEGATE_INSTALLER_LOG_FILE:-/var/log/routegate-agent-installer.log}"
ROUTEGATE_INSTALLER_STATE_READY=0
ROUTEGATE_INSTALLER_STAGE="preflight"
ROUTEGATE_INSTALLER_STARTED_AT=""
ROUTEGATE_WORK_DIR=""
ROUTEGATE_ARCH=""
ROUTEGATE_RESOLVED_VERSION=""
ROUTEGATE_BUNDLE_NAME=""

usage() {
  cat <<'USAGE'
RouteGate VPN Node Agent Installer

Usage:
  sudo env ROUTEGATE_MANAGER_URL='https://manager.example' \
    ROUTEGATE_REGISTRATION_TOKEN='rg_reg_...' bash install-agent.sh

Options:
  --manager-url URL          Public HTTPS URL of RouteGate Manager.
  --registration-token TOKEN
                             Short-lived one-time token created by Manager.
  --version VERSION          Release tag to install. Defaults to latest.
  --bundle-file PATH         Use a local release bundle.
  --checksum-file PATH       SHA256SUMS file for --bundle-file.
  --bundle-url URL           Use an explicit release bundle URL.
  --checksum-url URL         SHA256SUMS URL for --bundle-url.
  --bundle-base-url URL      Base URL containing versioned bundles and SHA256SUMS.
  --help                     Show this help.

Supported target: Ubuntu 24.04 LTS, amd64 or arm64, systemd.
USAGE
}

log() {
  local message="$*"
  printf '[RouteGate Agent] %s\n' "$message"
  if [[ "${ROUTEGATE_INSTALLER_STATE_READY:-0}" == "1" && -f "${ROUTEGATE_INSTALLER_LOG_FILE:-}" ]]; then
    printf '%s [INFO] stage=%s %s\n' "$(date -u +%Y-%m-%dT%H:%M:%SZ)" "${ROUTEGATE_INSTALLER_STAGE:-unknown}" "$message" >>"$ROUTEGATE_INSTALLER_LOG_FILE"
  fi
}

write_installer_state() {
  [[ "${ROUTEGATE_INSTALLER_STATE_READY:-0}" == "1" ]] || return 0
  local status=$1
  local stage=$2
  local updated_at tmp
  updated_at=$(date -u +%Y-%m-%dT%H:%M:%SZ)
  tmp=$(mktemp "${ROUTEGATE_INSTALLER_STATE_DIR}/.state.XXXXXX") || return 1
  {
    printf 'STATUS=%s\n' "$status"
    printf 'STAGE=%s\n' "$stage"
    printf 'STARTED_AT=%s\n' "$ROUTEGATE_INSTALLER_STARTED_AT"
    printf 'UPDATED_AT=%s\n' "$updated_at"
  } >"$tmp"
  chmod 0600 "$tmp"
  mv -f -- "$tmp" "$ROUTEGATE_INSTALLER_STATE_FILE"
}

set_installer_stage() {
  ROUTEGATE_INSTALLER_STAGE=$1
  write_installer_state running "$ROUTEGATE_INSTALLER_STAGE"
}

initialize_installer_state() {
  [[ ! -L "$ROUTEGATE_INSTALLER_STATE_DIR" ]] || die "Refusing to use a symbolic link as the Agent installer state directory."
  install -d -m 0700 "$ROUTEGATE_INSTALLER_STATE_DIR"
  [[ ! -L "$ROUTEGATE_INSTALLER_STATE_FILE" ]] || die "Refusing to use a symbolic link as the Agent installer state file."
  [[ ! -L "$ROUTEGATE_INSTALLER_LOG_FILE" ]] || die "Refusing to use a symbolic link as the Agent installer log."
  install -d -m 0755 "$(dirname "$ROUTEGATE_INSTALLER_LOG_FILE")"
  touch "$ROUTEGATE_INSTALLER_LOG_FILE"
  chown root:root "$ROUTEGATE_INSTALLER_LOG_FILE"
  chmod 0600 "$ROUTEGATE_INSTALLER_LOG_FILE"
  ROUTEGATE_INSTALLER_STARTED_AT=$(date -u +%Y-%m-%dT%H:%M:%SZ)
  ROUTEGATE_INSTALLER_STATE_READY=1
  write_installer_state running "$ROUTEGATE_INSTALLER_STAGE"
}

print_retry_guidance() {
  if [[ "${ROUTEGATE_INSTALLER_STATE_READY:-0}" == "1" ]]; then
    printf '[RouteGate Agent] Installer state: %s\n' "$ROUTEGATE_INSTALLER_STATE_FILE" >&2
    printf '[RouteGate Agent] Installer log: %s\n' "$ROUTEGATE_INSTALLER_LOG_FILE" >&2
  fi
  printf '[RouteGate Agent] Safe retry: return to Connect server, generate a fresh command, and run it on a supported host after resolving the error.\n' >&2
}

die() {
  local message="$*"
  printf '[RouteGate Agent] ERROR at stage %s: %s\n' "${ROUTEGATE_INSTALLER_STAGE:-preflight}" "$message" >&2
  if [[ "${ROUTEGATE_INSTALLER_STATE_READY:-0}" == "1" ]]; then
    printf '%s [ERROR] stage=%s %s\n' "$(date -u +%Y-%m-%dT%H:%M:%SZ)" "${ROUTEGATE_INSTALLER_STAGE:-unknown}" "$message" >>"$ROUTEGATE_INSTALLER_LOG_FILE" 2>/dev/null || true
    write_installer_state failed "${ROUTEGATE_INSTALLER_STAGE:-unknown}" 2>/dev/null || true
  fi
  print_retry_guidance
  exit 1
}

on_error() {
  local exit_code=$1
  local line_number=$2
  trap - ERR
  set +e
  printf '[RouteGate Agent] ERROR: Installer stopped at stage %s (line %s, exit %s).\n' "${ROUTEGATE_INSTALLER_STAGE:-unknown}" "$line_number" "$exit_code" >&2
  if [[ "${ROUTEGATE_INSTALLER_STATE_READY:-0}" == "1" ]]; then
    printf '%s [ERROR] stage=%s line=%s exit=%s unexpected command failure\n' "$(date -u +%Y-%m-%dT%H:%M:%SZ)" "${ROUTEGATE_INSTALLER_STAGE:-unknown}" "$line_number" "$exit_code" >>"$ROUTEGATE_INSTALLER_LOG_FILE" 2>/dev/null
    write_installer_state failed "${ROUTEGATE_INSTALLER_STAGE:-unknown}" 2>/dev/null
  fi
  print_retry_guidance
  exit "$exit_code"
}

cleanup() {
  if [[ -n "${ROUTEGATE_WORK_DIR:-}" && -d "$ROUTEGATE_WORK_DIR" ]]; then
    rm -rf "$ROUTEGATE_WORK_DIR"
  fi
}

parse_args() {
  while (($# > 0)); do
    case "$1" in
      --manager-url)
        (($# >= 2)) || die "--manager-url requires a value."
        ROUTEGATE_MANAGER_URL="$2"
        shift 2
        ;;
      --registration-token)
        (($# >= 2)) || die "--registration-token requires a value."
        ROUTEGATE_REGISTRATION_TOKEN="$2"
        shift 2
        ;;
      --version)
        (($# >= 2)) || die "--version requires a value."
        ROUTEGATE_VERSION="$2"
        shift 2
        ;;
      --bundle-file)
        (($# >= 2)) || die "--bundle-file requires a value."
        ROUTEGATE_BUNDLE_FILE="$2"
        shift 2
        ;;
      --checksum-file)
        (($# >= 2)) || die "--checksum-file requires a value."
        ROUTEGATE_CHECKSUM_FILE="$2"
        shift 2
        ;;
      --bundle-url)
        (($# >= 2)) || die "--bundle-url requires a value."
        ROUTEGATE_BUNDLE_URL="$2"
        shift 2
        ;;
      --checksum-url)
        (($# >= 2)) || die "--checksum-url requires a value."
        ROUTEGATE_CHECKSUM_URL="$2"
        shift 2
        ;;
      --bundle-base-url)
        (($# >= 2)) || die "--bundle-base-url requires a value."
        ROUTEGATE_BUNDLE_BASE_URL="$2"
        shift 2
        ;;
      --help|-h)
        usage
        exit 0
        ;;
      *) die "Unknown option: $1" ;;
    esac
  done
}

validate_release_version() {
  [[ "$1" =~ ^[A-Za-z0-9][A-Za-z0-9._+-]*$ ]]
}

validate_manager_url() {
  local value=${1%/}
  [[ "$value" =~ ^https://[A-Za-z0-9]([A-Za-z0-9.-]*[A-Za-z0-9])?(:[0-9]{1,5})?$ ]]
}

validate_bundle_base_url() {
  local value=${1%/}
  [[ "$value" == https://* ]] || return 1
  [[ "$value" != *"?"* && "$value" != *"#"* && "$value" != *"@"* ]] || return 1
  [[ "$value" != *[[:space:]]* ]] || return 1
  [[ "$value" =~ ^https://[A-Za-z0-9]([A-Za-z0-9.-]*[A-Za-z0-9])?(:[0-9]{1,5})?(/[-A-Za-z0-9._~/%]+)*$ ]]
}

validate_artifact_url() {
  local value=$1
  local remainder authority

  [[ "$value" == https://* ]] || return 1
  [[ "$value" != *[[:space:]]* && "$value" != *"#"* ]] || return 1

  remainder=${value#https://}
  authority=${remainder%%/*}
  authority=${authority%%\?*}
  [[ -n "$authority" && "$authority" != *"@"* ]] || return 1
  [[ "$authority" =~ ^[A-Za-z0-9]([A-Za-z0-9.-]*[A-Za-z0-9])?(:[0-9]{1,5})?$ ]]
}

validate_registration_token() {
  [[ "$1" =~ ^rg_reg_[A-Za-z0-9_-]{43}$ ]]
}

platform_architecture() {
  case "$1" in
    x86_64|amd64) printf 'amd64\n' ;;
    aarch64|arm64) printf 'arm64\n' ;;
    *) return 1 ;;
  esac
}

platform_tuple_supported() {
  local os_id=$1
  local version_id=$2
  local arch=$3
  local systemd_running=$4
  [[ "$os_id" == "ubuntu" && "$version_id" == "24.04" && ("$arch" == "amd64" || "$arch" == "arm64") && "$systemd_running" == "1" ]]
}


apt_source_uris() {
  local root=${1:-}
  local file

  for file in \
    "${root}/etc/apt/sources.list" \
    "${root}"/etc/apt/sources.list.d/*.list; do
    [[ -f "$file" ]] || continue
    awk '
      /^[[:space:]]*#/ || /^[[:space:]]*$/ { next }
      $1 == "deb" || $1 == "deb-src" {
        i = 2
        if ($i ~ /^\[/) {
          while (i <= NF && $i !~ /\]$/) i++
          i++
        }
        if (i <= NF) print $i
      }
    ' "$file"
  done

  for file in "${root}"/etc/apt/sources.list.d/*.sources; do
    [[ -f "$file" ]] || continue
    awk '
      function flush() {
        if (tolower(enabled) != "no") {
          for (i = 1; i <= uri_count; i++) print uris[i]
        }
        enabled = ""
        uri_count = 0
        delete uris
        in_uris = 0
      }
      /^[[:space:]]*$/ { flush(); next }
      /^[[:space:]]*#/ { next }
      tolower($1) == "enabled:" {
        enabled = tolower($2)
        in_uris = 0
        next
      }
      tolower($1) == "uris:" {
        for (i = 2; i <= NF; i++) uris[++uri_count] = $i
        in_uris = 1
        next
      }
      /^[^[:space:]][^:]*:/ {
        in_uris = 0
        next
      }
      in_uris && /^[[:space:]]+/ {
        for (i = 1; i <= NF; i++) uris[++uri_count] = $i
      }
      END { flush() }
    ' "$file"
  done
}

apt_repository_host() {
  local uri=$1
  local authority host

  case "$uri" in
    http://*|https://*) ;;
    *) return 1 ;;
  esac

  authority=${uri#*://}
  authority=${authority%%/*}
  authority=${authority##*@}
  host=${authority%%:*}
  [[ -n "$host" ]] || return 1
  printf '%s\n' "${host,,}"
}

apt_repository_host_trusted() {
  case "$1" in
    archive.ubuntu.com|security.ubuntu.com|ports.ubuntu.com|*.archive.ubuntu.com)
      return 0
      ;;
    *)
      return 1
      ;;
  esac
}

apt_repository_uri_trusted() {
  local uri=${1%/}
  case "$uri" in
    http://mirror.yandex.ru/ubuntu|http://mirror.yandex.ru/ubuntu/*|https://mirror.yandex.ru/ubuntu|https://mirror.yandex.ru/ubuntu/*)
      return 0
      ;;
    http://mirror.yandex.ru/mirrors/download.docker.com/linux/ubuntu|http://mirror.yandex.ru/mirrors/download.docker.com/linux/ubuntu/*|https://mirror.yandex.ru/mirrors/download.docker.com/linux/ubuntu|https://mirror.yandex.ru/mirrors/download.docker.com/linux/ubuntu/*)
      return 0
      ;;
    *)
      return 1
      ;;
  esac
}

apt_repository_trust_report() {
  local root=$1
  local uri host
  local found=0
  local blocked=0
  local uris=()

  mapfile -t uris < <(apt_source_uris "$root" | sort -u)

  if ((${#uris[@]} == 0)); then
    printf '  [blocked] no active APT repository URIs detected\n'
    return 1
  fi

  for uri in "${uris[@]}"; do
    found=1
    host=$(apt_repository_host "$uri" 2>/dev/null || true)
    if { [[ -n "$host" ]] && apt_repository_host_trusted "$host"; } || apt_repository_uri_trusted "$uri"; then
      printf '  [trusted] %s\n' "$uri"
    else
      printf '  [blocked] %s\n' "$uri"
      blocked=1
    fi
  done

  ((found == 1 && blocked == 0))
}

validate_apt_repository_trust() {
  local report=""

  printf '\n[RouteGate] APT repository trust preflight\n'
  if report=$(apt_repository_trust_report ""); then
    printf '%s\n\n' "$report"
    return 0
  fi

  printf '%s\n\n' "$report"
  die "Host APT sources are outside the RouteGate-approved boundary. Use Ubuntu archive/security repositories or the approved Yandex mirror paths (mirror.yandex.ru/ubuntu and mirror.yandex.ru/mirrors/download.docker.com/linux/ubuntu), then retry."
}

validate_inputs() {
  ROUTEGATE_MANAGER_URL=${ROUTEGATE_MANAGER_URL%/}
  ROUTEGATE_BUNDLE_BASE_URL=${ROUTEGATE_BUNDLE_BASE_URL%/}
  validate_manager_url "$ROUTEGATE_MANAGER_URL" || die "ROUTEGATE_MANAGER_URL must be a public HTTPS origin without a path, query, or fragment."
  validate_registration_token "$ROUTEGATE_REGISTRATION_TOKEN" || die "ROUTEGATE_REGISTRATION_TOKEN is invalid. Create a fresh token in RouteGate Manager."
  validate_release_version "$ROUTEGATE_VERSION" || die "ROUTEGATE_VERSION contains unsupported characters."
  if [[ -n "$ROUTEGATE_BUNDLE_BASE_URL" ]]; then
    validate_bundle_base_url "$ROUTEGATE_BUNDLE_BASE_URL" || die "ROUTEGATE_BUNDLE_BASE_URL must be an HTTPS URL without credentials, query, fragment, or whitespace."
    [[ "$ROUTEGATE_VERSION" != "latest" ]] || die "ROUTEGATE_BUNDLE_BASE_URL requires an explicit --version."
  fi

  if [[ -n "$ROUTEGATE_BUNDLE_FILE" || -n "$ROUTEGATE_CHECKSUM_FILE" ]]; then
    [[ -n "$ROUTEGATE_BUNDLE_FILE" && -n "$ROUTEGATE_CHECKSUM_FILE" ]] || die "--bundle-file and --checksum-file must be provided together."
  fi
  if [[ -n "$ROUTEGATE_BUNDLE_URL" || -n "$ROUTEGATE_CHECKSUM_URL" ]]; then
    [[ -n "$ROUTEGATE_BUNDLE_URL" && -n "$ROUTEGATE_CHECKSUM_URL" ]] || die "--bundle-url and --checksum-url must be provided together."
    validate_artifact_url "$ROUTEGATE_BUNDLE_URL" || die "--bundle-url must be an HTTPS URL without credentials, fragments, or whitespace."
    validate_artifact_url "$ROUTEGATE_CHECKSUM_URL" || die "--checksum-url must be an HTTPS URL without credentials, fragments, or whitespace."
  fi
  [[ -z "$ROUTEGATE_BUNDLE_FILE" || -z "$ROUTEGATE_BUNDLE_URL" ]] || die "Choose either a local bundle or an explicit bundle URL."
  [[ -z "$ROUTEGATE_BUNDLE_BASE_URL" || ( -z "$ROUTEGATE_BUNDLE_FILE" && -z "$ROUTEGATE_BUNDLE_URL" ) ]] || die "Choose only one bundle source: local files, explicit URLs, or bundle base URL."
}

require_supported_host() {
  [[ ${EUID:-$(id -u)} -eq 0 ]] || die "Run this installer through sudo or as root."
  [[ -r /etc/os-release ]] || die "/etc/os-release is required."
  # shellcheck disable=SC1091
  source /etc/os-release
  ROUTEGATE_ARCH=$(platform_architecture "$(uname -m)") || die "Unsupported CPU architecture: $(uname -m)."
  local systemd_running=0
  [[ -d /run/systemd/system ]] && systemd_running=1
  platform_tuple_supported "${ID:-}" "${VERSION_ID:-}" "$ROUTEGATE_ARCH" "$systemd_running" \
    || die "Supported target: Ubuntu 24.04 LTS on amd64 or arm64 with systemd."
}

install_dependencies() {
  log "Installing Agent bootstrap dependencies."
  export DEBIAN_FRONTEND=noninteractive
  apt-get update >/dev/null
  apt-get install -y ca-certificates curl iproute2 jq python3 tar >/dev/null
}

resolve_release_version() {
  if [[ "$ROUTEGATE_VERSION" != "latest" ]]; then
    ROUTEGATE_RESOLVED_VERSION="$ROUTEGATE_VERSION"
    return
  fi
  ROUTEGATE_RESOLVED_VERSION=$(curl -fsSL --max-time 30 \
    "https://api.github.com/repos/${ROUTEGATE_REPOSITORY}/releases/latest" | jq -er '.tag_name') \
    || die "No published RouteGate release could be resolved."
}

artifact_urls() {
  local version=$1
  local arch=$2
  local bundle_name="routegate-${version}-linux-${arch}.tar.gz"
  printf '%s\n' \
    "https://github.com/${ROUTEGATE_REPOSITORY}/releases/download/${version}/${bundle_name}" \
    "https://github.com/${ROUTEGATE_REPOSITORY}/releases/download/${version}/SHA256SUMS"
}

verify_bundle_checksum() {
  local bundle_path=$1
  local checksum_path=$2
  local bundle_name=$3
  local expected actual
  expected=$(awk -v name="$bundle_name" '$2 == name || $2 == "*" name {print $1; exit}' "$checksum_path")
  [[ "$expected" =~ ^[a-fA-F0-9]{64}$ ]] || die "No valid SHA-256 entry for ${bundle_name} was found."
  actual=$(sha256sum "$bundle_path" | awk '{print $1}')
  [[ "$actual" == "$expected" ]] || die "Release bundle checksum verification failed."
}

extract_bundle() {
  local bundle_path=$1
  local extract_dir="$ROUTEGATE_WORK_DIR/extracted"
  mkdir -p "$extract_dir"
  if tar -tzf "$bundle_path" | awk '$0 ~ /^\// || $0 ~ /(^|\/)\.\.(\/|$)/ {found=1} END {exit !found}'; then
    die "Release bundle contains an unsafe path."
  fi
  if tar -tvzf "$bundle_path" | awk '$1 !~ /^[-d]/ {found=1} END {exit !found}'; then
    die "Release bundle contains a non-regular filesystem entry."
  fi
  tar -xzf "$bundle_path" -C "$extract_dir"

  [[ -s "$extract_dir/bin/routegate-agent" ]] || die "Release bundle is missing RouteGate Agent."
  [[ -f "$extract_dir/systemd/routegate-agent.service" ]] || die "Release bundle is missing the Agent systemd unit."
  [[ -f "$extract_dir/metadata/manifest.env" ]] || die "Release bundle is missing its manifest."

  local updater_file
  for updater_file in \
    release_manifest.py \
    routegate-update-bootstrap.sh \
    routegate-update-core.sh \
    routegate-update-role.sh \
    routegate-update-transaction.sh \
    routegate-update-verified.sh; do
    [[ -f "$extract_dir/tools/$updater_file" && ! -L "$extract_dir/tools/$updater_file" ]] \
      || die "Release bundle is missing updater component tools/${updater_file}."
  done

  local manifest_version manifest_os manifest_arch
  manifest_version=$(sed -n 's/^VERSION=//p' "$extract_dir/metadata/manifest.env" | head -n1)
  manifest_os=$(sed -n 's/^OS=//p' "$extract_dir/metadata/manifest.env" | head -n1)
  manifest_arch=$(sed -n 's/^ARCH=//p' "$extract_dir/metadata/manifest.env" | head -n1)
  [[ -n "$manifest_version" && "$manifest_os" == "linux" && "$manifest_arch" == "$ROUTEGATE_ARCH" ]] \
    || die "Release bundle manifest does not match this host."
}

prepare_bundle() {
  ROUTEGATE_WORK_DIR=$(mktemp -d /tmp/routegate-agent-installer.XXXXXX)
  local bundle_path="$ROUTEGATE_WORK_DIR/routegate-bundle.tar.gz"
  local checksum_path="$ROUTEGATE_WORK_DIR/SHA256SUMS"

  if [[ -n "$ROUTEGATE_BUNDLE_FILE" ]]; then
    cp "$ROUTEGATE_BUNDLE_FILE" "$bundle_path"
    cp "$ROUTEGATE_CHECKSUM_FILE" "$checksum_path"
    ROUTEGATE_BUNDLE_NAME=$(basename "$ROUTEGATE_BUNDLE_FILE")
  elif [[ -n "$ROUTEGATE_BUNDLE_URL" ]]; then
    ROUTEGATE_BUNDLE_NAME=$(basename "${ROUTEGATE_BUNDLE_URL%%\?*}")
    curl -fL --retry 3 --connect-timeout 15 --max-time 300 -o "$bundle_path" "$ROUTEGATE_BUNDLE_URL"
    curl -fL --retry 3 --connect-timeout 15 --max-time 60 -o "$checksum_path" "$ROUTEGATE_CHECKSUM_URL"
  elif [[ -n "$ROUTEGATE_BUNDLE_BASE_URL" ]]; then
    resolve_release_version
    ROUTEGATE_BUNDLE_NAME="routegate-${ROUTEGATE_RESOLVED_VERSION}-linux-${ROUTEGATE_ARCH}.tar.gz"
    curl -fL --retry 3 --connect-timeout 15 --max-time 300 -o "$bundle_path" "${ROUTEGATE_BUNDLE_BASE_URL}/${ROUTEGATE_BUNDLE_NAME}"
    curl -fL --retry 3 --connect-timeout 15 --max-time 60 -o "$checksum_path" "${ROUTEGATE_BUNDLE_BASE_URL}/SHA256SUMS"
  else
    resolve_release_version
    ROUTEGATE_BUNDLE_NAME="routegate-${ROUTEGATE_RESOLVED_VERSION}-linux-${ROUTEGATE_ARCH}.tar.gz"
    local urls=()
    mapfile -t urls < <(artifact_urls "$ROUTEGATE_RESOLVED_VERSION" "$ROUTEGATE_ARCH")
    curl -fL --retry 3 --connect-timeout 15 --max-time 300 -o "$bundle_path" "${urls[0]}"
    curl -fL --retry 3 --connect-timeout 15 --max-time 60 -o "$checksum_path" "${urls[1]}"
  fi

  verify_bundle_checksum "$bundle_path" "$checksum_path" "$ROUTEGATE_BUNDLE_NAME"
  extract_bundle "$bundle_path"
}

config_value() {
  local path=$1
  local key=$2
  sed -n "s/^${key}:[[:space:]]*\"\(.*\)\"[[:space:]]*$/\1/p" "$path" | head -n1
}

write_agent_config() {
  local path=$1
  install -d -m 0755 "$(dirname "$path")"
  cat >"$path" <<EOF_CONFIG
manager_url: "${ROUTEGATE_MANAGER_URL}"
registration_token: "${ROUTEGATE_REGISTRATION_TOKEN}"
heartbeat_interval_seconds: 30
config_staging_dir: "/var/lib/routegate-agent/configs"
active_config_path: "/etc/sing-box/config.json"
config_backup_dir: "/var/lib/routegate-agent/backups"
sing_box_path: "sing-box"
sing_box_service_name: "sing-box"
wireguard_staging_dir: "/var/lib/routegate-agent/wireguard-configs"
wireguard_active_config_path: "/etc/wireguard/routegate-wg0.conf"
wireguard_backup_dir: "/var/lib/routegate-agent/wireguard-backups"
wg_quick_path: "/usr/bin/wg-quick"
wg_path: "/usr/bin/wg"
wireguard_service_name: "wg-quick@routegate-wg0"
wireguard_interface: "routegate-wg0"
hysteria2_staging_dir: "/var/lib/routegate-agent/hysteria2-configs"
hysteria2_active_config_path: "/etc/hysteria/config.json"
hysteria2_backup_dir: "/var/lib/routegate-agent/hysteria2-backups"
hysteria2_path: "/usr/local/bin/hysteria"
hysteria2_service_name: "hysteria-server"
ss_path: "/usr/bin/ss"
mtproto_staging_dir: "/var/lib/routegate-agent/mtproto-configs"
mtproto_active_config_path: "/etc/routegate-mtproto/config.toml"
mtproto_backup_dir: "/var/lib/routegate-agent/mtproto-backups"
mtg_path: "/usr/local/bin/mtg"
mtproto_service_name: "routegate-mtproto"
service_control_enabled: true
traffic_collection_enabled: false
traffic_collection_interval_seconds: 60
traffic_usage_file_path: "/var/lib/routegate-agent/traffic-usage.json"
client_presence_enabled: true
client_presence_interval_seconds: 30
client_presence_file_path: "/var/lib/routegate-agent/client-presence.json"
EOF_CONFIG
  chmod 0600 "$path"
}

install_agent() {
  local source_dir="$ROUTEGATE_WORK_DIR/extracted"
  install -d -m 0700 /var/lib/routegate-agent /var/lib/routegate-agent/configs /var/lib/routegate-agent/backups /var/lib/routegate-agent/wireguard-configs /var/lib/routegate-agent/wireguard-backups /var/lib/routegate-agent/hysteria2-configs /var/lib/routegate-agent/hysteria2-backups /var/lib/routegate-agent/mtproto-configs /var/lib/routegate-agent/mtproto-backups
  install -m 0755 "$source_dir/bin/routegate-agent" "$ROUTEGATE_AGENT_BINARY"
  install -m 0644 "$source_dir/systemd/routegate-agent.service" "$ROUTEGATE_AGENT_SERVICE"

  if [[ -r "$ROUTEGATE_AGENT_CONFIG" ]] && [[ $(config_value "$ROUTEGATE_AGENT_CONFIG" agent_token) == rg_agent_* ]]; then
    local configured_manager
    configured_manager=$(config_value "$ROUTEGATE_AGENT_CONFIG" manager_url)
    [[ "${configured_manager%/}" == "$ROUTEGATE_MANAGER_URL" ]] \
      || die "This host is already registered with a different RouteGate Manager."
    log "Existing Agent identity preserved."
  else
    write_agent_config "$ROUTEGATE_AGENT_CONFIG"
  fi

  systemctl daemon-reload
  systemctl enable --now routegate-agent.service >/dev/null
}

wait_for_registration() {
  local agent_token
  for _ in {1..30}; do
    agent_token=$(config_value "$ROUTEGATE_AGENT_CONFIG" agent_token || true)
    if [[ "$agent_token" == rg_agent_* ]]; then
      log "VPN Node connected to ${ROUTEGATE_MANAGER_URL}."
      log "Protocol runtimes are installed later by RouteGate when you configure a VPN protocol."
      return
    fi
    systemctl is-active --quiet routegate-agent.service || break
    sleep 1
  done
  journalctl -u routegate-agent.service -n 30 --no-pager >&2 || true
  die "Agent did not complete registration. Create a fresh token in Manager before retrying if the current token was consumed."
}

bootstrap_trusted_updater() {
  local source_dir="$ROUTEGATE_WORK_DIR/extracted"
  local helper="$source_dir/tools/routegate-update-bootstrap.sh"
  [[ -f "$helper" && ! -L "$helper" ]] || die "Release bundle is missing the trusted updater bootstrap helper."

  log "Bootstrapping the local trusted updater boundary."
  env -u RG_UPDATE_ROOT bash "$helper" \
    || die "Trusted updater bootstrap failed. The Agent remains installed, but this node is not update-ready."
}

main() {
  trap cleanup EXIT
  parse_args "$@"
  validate_inputs
  require_supported_host

  initialize_installer_state
  trap 'on_error $? $LINENO' ERR

  set_installer_stage apt_repository_preflight
  validate_apt_repository_trust

  set_installer_stage dependencies
  install_dependencies

  set_installer_stage bundle
  prepare_bundle

  set_installer_stage agent_install
  install_agent

  set_installer_stage registration
  wait_for_registration

  set_installer_stage updater
  bootstrap_trusted_updater

  ROUTEGATE_INSTALLER_STAGE=complete
  write_installer_state complete complete
  log "Agent bootstrap completed successfully."
}

if [[ "${BASH_SOURCE[0]:-$0}" == "$0" ]]; then
  main "$@"
fi
