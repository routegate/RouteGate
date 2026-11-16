#!/usr/bin/env bash

set -Eeuo pipefail
IFS=$'\n\t'
umask 077

ROUTEGATE_REPOSITORY="${ROUTEGATE_REPOSITORY:-ikaevus/RouteGate}"
ROUTEGATE_VERSION="${ROUTEGATE_VERSION:-latest}"
ROUTEGATE_DOMAIN="${ROUTEGATE_DOMAIN:-}"
ROUTEGATE_EMAIL="${ROUTEGATE_EMAIL:-}"
ROUTEGATE_ADMIN_EMAIL="${ROUTEGATE_ADMIN_EMAIL:-}"
ROUTEGATE_SERVER_NAME="${ROUTEGATE_SERVER_NAME:-}"
ROUTEGATE_BUNDLE_FILE="${ROUTEGATE_BUNDLE_FILE:-}"
ROUTEGATE_CHECKSUM_FILE="${ROUTEGATE_CHECKSUM_FILE:-}"
ROUTEGATE_BUNDLE_URL="${ROUTEGATE_BUNDLE_URL:-}"
ROUTEGATE_CHECKSUM_URL="${ROUTEGATE_CHECKSUM_URL:-}"
ROUTEGATE_ASSUME_YES="${ROUTEGATE_ASSUME_YES:-0}"
ROUTEGATE_INSTALL_PROMETHEUS="${ROUTEGATE_INSTALL_PROMETHEUS:-}"

ROUTEGATE_STATE_DIR="/var/lib/routegate-installer"
ROUTEGATE_STATE_FILE="/etc/routegate/install-state.env"
ROUTEGATE_LOG_FILE="/var/log/routegate-installer.log"
ROUTEGATE_CREDENTIALS_FILE="/root/routegate-first-login.txt"
ROUTEGATE_MANAGER_ENV="/etc/routegate/manager.env"
ROUTEGATE_AGENT_CONFIG="/etc/routegate/agent.yaml"
ROUTEGATE_LOCAL_API="http://127.0.0.1:8080"
ROUTEGATE_WORK_DIR=""
ROUTEGATE_ARCH=""
ROUTEGATE_RESOLVED_VERSION=""
ROUTEGATE_BUNDLE_NAME=""
ROUTEGATE_PUBLIC_IPV4=""
ROUTEGATE_DB_PASSWORD=""
ROUTEGATE_ADMIN_PASSWORD=""
ROUTEGATE_MONITORING_TOKEN=""
ROUTEGATE_SECRETS_FILE="/var/lib/routegate-installer/secrets.env"
ROUTEGATE_RESUME_INSTALL=0
ROUTEGATE_SETUP_URL=""
ROUTEGATE_SETUP_EXPIRES_AT=""
ROUTEGATE_LOCK_FILE="/run/lock/routegate-installer.lock"
ROUTEGATE_PROMETHEUS_CONFIG="/etc/prometheus/routegate.yml"
ROUTEGATE_PROMETHEUS_TOKEN_FILE="/etc/prometheus/routegate.token"
ROUTEGATE_PROMETHEUS_STORAGE="/var/lib/prometheus/routegate"
ROUTEGATE_PROMETHEUS_OVERRIDE="/etc/systemd/system/prometheus.service.d/routegate.conf"
ROUTEGATE_PROMETHEUS_INSTALL_MASK="/etc/systemd/system/prometheus.service"
ROUTEGATE_INSTALL_STAGE="arguments"

set_install_stage() {
  ROUTEGATE_INSTALL_STAGE=$1
}

usage() {
  cat <<'USAGE'
RouteGate Clean VPS Installer

Usage:
  sudo bash install.sh [options]

Canonical interactive installation:
  Download install.sh from an immutable RouteGate release, review it, then run:
  sudo bash install.sh

Do not pipe a mutable branch directly into a privileged shell.

The installer requires a public DNS name you control (a subdomain is fine).
IP-only Manager installations are not supported. It also asks for email
addresses and whether optional Prometheus should be installed.

Options:
  --domain FQDN             Public RouteGate hostname. DNS must already point here.
  --email EMAIL             Email used for Let's Encrypt notifications.
  --admin-email EMAIL       First administrator email. Defaults to --email.
  --server-name NAME        Local All-in-One server name. Defaults to the FQDN.
  --version VERSION         Release tag to install. Defaults to latest.
  --bundle-file PATH        Use a local release bundle instead of GitHub Releases.
  --checksum-file PATH      SHA256SUMS file for --bundle-file.
  --bundle-url URL          Use an explicit bundle URL.
  --checksum-url URL        SHA256SUMS URL for --bundle-url.
  --with-prometheus         Install RouteGate-managed Prometheus.
  --without-prometheus      Do not install Prometheus (default).
  --yes                     Skip the final confirmation prompt.
  --help                    Show this help.

Supported installation target:
  Ubuntu 24.04 LTS, amd64, systemd, clean or RouteGate-owned host.
USAGE
}

log() {
  local message="$*"
  printf '[RouteGate] %s\n' "$message"
  if [[ -n "${ROUTEGATE_LOG_FILE:-}" && -e "${ROUTEGATE_LOG_FILE:-}" ]]; then
    printf '%s [INFO] %s\n' "$(date -u +%Y-%m-%dT%H:%M:%SZ)" "$message" >>"$ROUTEGATE_LOG_FILE"
  fi
}

warn() {
  local message="$*"
  printf '[RouteGate] WARNING: %s\n' "$message" >&2
  if [[ -n "${ROUTEGATE_LOG_FILE:-}" && -e "${ROUTEGATE_LOG_FILE:-}" ]]; then
    printf '%s [WARN] %s\n' "$(date -u +%Y-%m-%dT%H:%M:%SZ)" "$message" >>"$ROUTEGATE_LOG_FILE"
  fi
}

die() {
  local message="$*"
  printf '[RouteGate] ERROR at stage %s: %s\n' "$ROUTEGATE_INSTALL_STAGE" "$message" >&2
  if [[ -n "${ROUTEGATE_LOG_FILE:-}" && -e "${ROUTEGATE_LOG_FILE:-}" ]]; then
    printf '%s [ERROR] stage=%s %s\n' "$(date -u +%Y-%m-%dT%H:%M:%SZ)" "$ROUTEGATE_INSTALL_STAGE" "$message" >>"$ROUTEGATE_LOG_FILE"
  fi
  printf '[RouteGate] Next action: resolve the error; review the installer log and routegate-recovery status if available before retrying this installer.\n' >&2
  exit 1
}

on_error() {
  local exit_code=$?
  local line_number=${1:-unknown}
  warn "Installation stopped at stage ${ROUTEGATE_INSTALL_STAGE}, line ${line_number} (exit ${exit_code})."
  warn "Next action: review ${ROUTEGATE_LOG_FILE} and routegate-recovery status if available; resolve the error before retrying."
  exit "$exit_code"
}

cleanup() {
  if [[ -n "${ROUTEGATE_WORK_DIR:-}" && -d "$ROUTEGATE_WORK_DIR" ]]; then
    rm -rf "$ROUTEGATE_WORK_DIR"
  fi
}

acquire_install_lock() {
  install -d -m 0755 "$(dirname "$ROUTEGATE_LOCK_FILE")"
  exec 9>"$ROUTEGATE_LOCK_FILE"
  flock -n 9 || die "Another RouteGate installer process is already running."
}

parse_args() {
  while (($# > 0)); do
    case "$1" in
      --domain)
        (($# >= 2)) || die "--domain requires a value."
        ROUTEGATE_DOMAIN="$2"
        shift 2
        ;;
      --email)
        (($# >= 2)) || die "--email requires a value."
        ROUTEGATE_EMAIL="$2"
        shift 2
        ;;
      --admin-email)
        (($# >= 2)) || die "--admin-email requires a value."
        ROUTEGATE_ADMIN_EMAIL="$2"
        shift 2
        ;;
      --server-name)
        (($# >= 2)) || die "--server-name requires a value."
        ROUTEGATE_SERVER_NAME="$2"
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
      --with-prometheus)
        ROUTEGATE_INSTALL_PROMETHEUS=1
        shift
        ;;
      --without-prometheus)
        ROUTEGATE_INSTALL_PROMETHEUS=0
        shift
        ;;
      --yes|-y)
        ROUTEGATE_ASSUME_YES=1
        shift
        ;;
      --help|-h)
        usage
        exit 0
        ;;
      *)
        die "Unknown argument: $1"
        ;;
    esac
  done

  ROUTEGATE_DOMAIN="${ROUTEGATE_DOMAIN,,}"
}

interactive_tty_available() {
  [[ -r /dev/tty && -w /dev/tty ]]
}

prompt_valid_value() {
  local prompt=$1
  local kind=$2
  local value=""

  while true; do
    read -r -p "$prompt" value </dev/tty
    case "$kind" in
      domain)
        value="${value,,}"
        if validate_domain "$value"; then
          printf '%s' "$value"
          return 0
        fi
        ;;
      email)
        if validate_email "$value"; then
          printf '%s' "$value"
          return 0
        fi
        ;;
      *)
        die "Unknown interactive input kind: $kind"
        ;;
    esac
    printf '[RouteGate] Invalid value. Please try again.\n' >/dev/tty
  done
}

normalize_prometheus_choice() {
  case "${1,,}" in
    1|true|yes|y)
      printf '1'
      ;;
    0|false|no|n|"")
      printf '0'
      ;;
    *)
      return 1
      ;;
  esac
}

prompt_for_prometheus() {
  if [[ -n "$ROUTEGATE_INSTALL_PROMETHEUS" ]]; then
    ROUTEGATE_INSTALL_PROMETHEUS=$(normalize_prometheus_choice "$ROUTEGATE_INSTALL_PROMETHEUS") \
      || die "ROUTEGATE_INSTALL_PROMETHEUS must be true/false or 1/0."
    return 0
  fi

  if [[ -f "$ROUTEGATE_STATE_FILE" ]]; then
    local stored_choice=""
    stored_choice=$(state_value PROMETHEUS_MANAGED || true)
    if [[ -n "$stored_choice" ]]; then
      ROUTEGATE_INSTALL_PROMETHEUS=$(normalize_prometheus_choice "$stored_choice") \
        || die "Stored Prometheus installation state is invalid."
      return 0
    fi
  fi

  if [[ "$ROUTEGATE_ASSUME_YES" == "1" ]] || ! interactive_tty_available; then
    ROUTEGATE_INSTALL_PROMETHEUS=0
    return 0
  fi

  local answer=""
  read -r -p "Install Prometheus for historical infrastructure metrics? [y/N] " answer </dev/tty
  if [[ "$answer" =~ ^[Yy]$ ]]; then
    ROUTEGATE_INSTALL_PROMETHEUS=1
  else
    ROUTEGATE_INSTALL_PROMETHEUS=0
  fi
}

prompt_for_inputs() {
  if [[ -z "$ROUTEGATE_DOMAIN" || -z "$ROUTEGATE_EMAIL" ]]; then
    [[ "$ROUTEGATE_ASSUME_YES" != "1" ]] \
      || die "--domain and --email are required when --yes is used."
    interactive_tty_available \
      || die "Missing --domain or --email and no interactive terminal is available."
  fi

  if [[ -z "$ROUTEGATE_DOMAIN" ]]; then
    ROUTEGATE_DOMAIN=$(prompt_valid_value "RouteGate domain: " domain)
  fi
  ROUTEGATE_DOMAIN="${ROUTEGATE_DOMAIN,,}"

  if [[ -z "$ROUTEGATE_EMAIL" ]]; then
    ROUTEGATE_EMAIL=$(prompt_valid_value "Email for Let's Encrypt notifications: " email)
  fi

  if [[ -z "$ROUTEGATE_ADMIN_EMAIL" ]]; then
    if [[ "$ROUTEGATE_ASSUME_YES" == "1" ]] || ! interactive_tty_available; then
      ROUTEGATE_ADMIN_EMAIL="$ROUTEGATE_EMAIL"
    else
      local answer=""
      read -r -p "Use the same email for the RouteGate administrator? [Y/n] " answer </dev/tty
      if [[ "$answer" =~ ^[Nn]$ ]]; then
        ROUTEGATE_ADMIN_EMAIL=$(prompt_valid_value "RouteGate administrator email: " email)
      else
        ROUTEGATE_ADMIN_EMAIL="$ROUTEGATE_EMAIL"
      fi
    fi
  fi

  prompt_for_prometheus
  ROUTEGATE_SERVER_NAME="${ROUTEGATE_SERVER_NAME:-$ROUTEGATE_DOMAIN}"
}

validate_domain() {
  local domain=$1
  [[ ${#domain} -le 253 ]] || return 1
  [[ "$domain" == *.* ]] || return 1
  [[ "$domain" != *://* && "$domain" != */* && "$domain" != *:* ]] || return 1
  [[ "$domain" =~ ^([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$ ]]
}

validate_email() {
  local email=$1
  [[ "$email" =~ ^[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\.[A-Za-z]{2,}$ ]]
}

validate_release_version() {
  local version=$1
  [[ "$version" == "latest" || "$version" =~ ^[A-Za-z0-9][A-Za-z0-9._+-]*$ ]]
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

require_root() {
  [[ ${EUID:-$(id -u)} -eq 0 ]] || die "Run the installer as root or through sudo."
}

initialize_logging() {
  install -d -m 0755 "$(dirname "$ROUTEGATE_LOG_FILE")"
  [[ ! -L "$ROUTEGATE_LOG_FILE" ]] || die "Refusing to use a symbolic link as the installer log."
  touch "$ROUTEGATE_LOG_FILE"
  chown root:root "$ROUTEGATE_LOG_FILE"
  chmod 0600 "$ROUTEGATE_LOG_FILE"
}

validate_inputs() {
  [[ -n "$ROUTEGATE_DOMAIN" ]] || die "--domain is required."
  [[ -n "$ROUTEGATE_EMAIL" ]] || die "--email is required for HTTPS."
  validate_domain "$ROUTEGATE_DOMAIN" || die "Invalid FQDN: $ROUTEGATE_DOMAIN"
  validate_email "$ROUTEGATE_EMAIL" || die "Invalid Let's Encrypt email address."
  validate_email "$ROUTEGATE_ADMIN_EMAIL" || die "Invalid administrator email address."
  validate_release_version "$ROUTEGATE_VERSION" || die "Invalid release version: $ROUTEGATE_VERSION"
  [[ "$ROUTEGATE_INSTALL_PROMETHEUS" == "0" || "$ROUTEGATE_INSTALL_PROMETHEUS" == "1" ]] \
    || die "Prometheus installation choice is invalid."
  [[ -n "$ROUTEGATE_SERVER_NAME" ]] || die "Server name cannot be empty."
  [[ ${#ROUTEGATE_SERVER_NAME} -le 128 ]] || die "Server name must be 128 characters or fewer."
  [[ "$ROUTEGATE_SERVER_NAME" != *$'\n'* && "$ROUTEGATE_SERVER_NAME" != *$'\r'* ]] \
    || die "Server name cannot contain a line break."

  if [[ -n "$ROUTEGATE_BUNDLE_FILE" || -n "$ROUTEGATE_CHECKSUM_FILE" ]]; then
    [[ -n "$ROUTEGATE_BUNDLE_FILE" && -n "$ROUTEGATE_CHECKSUM_FILE" ]] \
      || die "--bundle-file and --checksum-file must be supplied together."
  fi
  if [[ -n "$ROUTEGATE_BUNDLE_URL" || -n "$ROUTEGATE_CHECKSUM_URL" ]]; then
    [[ -n "$ROUTEGATE_BUNDLE_URL" && -n "$ROUTEGATE_CHECKSUM_URL" ]] \
      || die "--bundle-url and --checksum-url must be supplied together."
    validate_artifact_url "$ROUTEGATE_BUNDLE_URL" \
      || die "--bundle-url must be an HTTPS URL without credentials, fragments, or whitespace."
    validate_artifact_url "$ROUTEGATE_CHECKSUM_URL" \
      || die "--checksum-url must be an HTTPS URL without credentials, fragments, or whitespace."
  fi
  [[ -z "$ROUTEGATE_BUNDLE_FILE" || -z "$ROUTEGATE_BUNDLE_URL" ]] \
    || die "Choose either a local bundle or an explicit bundle URL, not both."
}

read_os_release_value() {
  local key=$1
  local value=""
  [[ -r /etc/os-release ]] || return 1
  value=$(sed -n "s/^${key}=//p" /etc/os-release | head -n1)
  value=${value%\"}
  value=${value#\"}
  printf '%s' "$value"
}

platform_tuple_supported() {
  local os_id=$1
  local version_id=$2
  local arch=$3
  local systemd_running=$4
  [[ "$os_id" == "ubuntu" && "$version_id" == "24.04" && "$arch" == "amd64" && "$systemd_running" == "1" ]]
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

validate_platform() {
  local os_id version_id arch
  os_id=$(read_os_release_value ID)
  version_id=$(read_os_release_value VERSION_ID)
  arch=$(dpkg --print-architecture 2>/dev/null || true)

  [[ "$os_id" == "ubuntu" ]] || die "Unsupported operating system: ${os_id:-unknown}. Ubuntu 24.04 LTS is required."
  [[ "$version_id" == "24.04" ]] || die "Unsupported Ubuntu version: ${version_id:-unknown}. Ubuntu 24.04 LTS is required."
  [[ "$arch" == "amd64" ]] || die "Unsupported architecture: ${arch:-unknown}. The installer MVP currently accepts amd64 only."
  [[ -d /run/systemd/system ]] || die "systemd is required and does not appear to be running."
  command -v systemctl >/dev/null 2>&1 || die "systemctl is required."
  command -v apt-get >/dev/null 2>&1 || die "apt-get is required to run the installer."
  command -v curl >/dev/null 2>&1 || die "curl is required to run the installer."
  command -v getent >/dev/null 2>&1 || die "getent is required."
  command -v ip >/dev/null 2>&1 || die "the ip command is required."
  command -v runuser >/dev/null 2>&1 || die "runuser is required."
  command -v ss >/dev/null 2>&1 || die "the ss command is required."
  command -v flock >/dev/null 2>&1 || die "flock is required."

  ROUTEGATE_ARCH="$arch"
}

state_value() {
  local key=$1
  [[ -r "$ROUTEGATE_STATE_FILE" ]] || return 1
  sed -n "s/^${key}=//p" "$ROUTEGATE_STATE_FILE" | head -n1
}

write_install_state_status() {
  local status=$1
  local version=${2:-${ROUTEGATE_RESOLVED_VERSION:-${ROUTEGATE_VERSION}}}
  install -d -m 0755 /etc/routegate
  install -m 0600 /dev/null "$ROUTEGATE_STATE_FILE"
  cat >"$ROUTEGATE_STATE_FILE" <<EOF_STATE
STATUS=${status}
DOMAIN=${ROUTEGATE_DOMAIN}
VERSION=${version}
ARCH=${ROUTEGATE_ARCH}
PROMETHEUS_MANAGED=${ROUTEGATE_INSTALL_PROMETHEUS}
UPDATED_AT=$(date -u +%Y-%m-%dT%H:%M:%SZ)
EOF_STATE
  chmod 0600 "$ROUTEGATE_STATE_FILE"
}

verify_existing_install() {
  local installed_domain installed_status installed_prometheus
  installed_domain=$(state_value DOMAIN || true)
  installed_status=$(state_value STATUS || true)
  installed_prometheus=$(state_value PROMETHEUS_MANAGED || true)
  installed_prometheus=${installed_prometheus:-0}
  installed_prometheus=$(normalize_prometheus_choice "$installed_prometheus") \
    || die "Existing RouteGate Prometheus ownership state is invalid."

  [[ "$installed_domain" == "$ROUTEGATE_DOMAIN" ]] \
    || die "This host is already owned by RouteGate for ${installed_domain:-another domain}."
  [[ "$ROUTEGATE_INSTALL_PROMETHEUS" == "$installed_prometheus" ]] \
    || die "Existing RouteGate installation uses a different Prometheus ownership mode. Manage Prometheus through RouteGate instead of re-running the platform installer with a different choice."

  if [[ "$installed_status" == "installing" ]]; then
    ROUTEGATE_RESUME_INSTALL=1
    log "An interrupted RouteGate installation was detected; resuming with preserved secrets."
    return 0
  fi
  [[ "$installed_status" == "complete" ]] \
    || die "RouteGate state is unknown (${installed_status:-missing}); review ${ROUTEGATE_STATE_FILE}."

  log "Existing RouteGate-owned installation detected; running idempotency checks."
  local services=(postgresql nginx routegate-manager routegate-agent certbot.timer)
  if [[ "$installed_prometheus" == "1" ]]; then
    services+=(prometheus)
  fi
  local service
  for service in "${services[@]}"; do
    systemctl is-enabled "$service" >/dev/null 2>&1 \
      || die "Existing installation is incomplete: ${service} is not enabled."
    systemctl is-active "$service" >/dev/null 2>&1 \
      || die "Existing installation is unhealthy: ${service} is not active."
  done
  curl -fsS --max-time 15 "https://${ROUTEGATE_DOMAIN}/api/admin/health" >/dev/null \
    || die "Existing installation is unhealthy: HTTPS health check failed."
  if [[ "$installed_prometheus" == "1" ]]; then
    wait_for_url "http://127.0.0.1:9090/-/ready" 5 1 \
      || die "Existing installation is unhealthy: Prometheus readiness check failed."
  fi

  log "RouteGate is already installed and healthy. No changes were made."
  log "Open https://${ROUTEGATE_DOMAIN}/"
  exit 0
}

verify_completed_install_before_repository_preflight() {
  [[ -f "$ROUTEGATE_STATE_FILE" ]] || return 0
  [[ "$(state_value STATUS || true)" == "complete" ]] || return 0
  verify_existing_install
}

collect_routegate_conflicts() {
  local root=${1:-}
  local path
  for path in \
    /usr/local/bin/routegate-manager \
    /usr/local/bin/routegate-agent \
	/usr/local/bin/hysteria \
    /usr/local/bin/mtg \
    /usr/local/lib/routegate/update \
    /usr/local/lib/routegate/verifier \
    /usr/local/sbin/routegate-recovery \
    /usr/local/sbin/routegate-update \
    /etc/routegate/manager.env \
    /etc/routegate/agent.yaml \
    /etc/systemd/system/routegate-manager.service \
    /etc/systemd/system/routegate-agent.service \
	/etc/systemd/system/hysteria-server.service \
	/etc/hysteria \
    /etc/systemd/system/routegate-mtproto.service \
    /etc/routegate-mtproto \
    /etc/nginx/sites-available/routegate \
    /etc/letsencrypt/renewal-hooks/deploy/routegate-nginx-reload \
    /etc/prometheus/routegate.yml \
    /etc/prometheus/routegate.token \
    /etc/systemd/system/prometheus.service \
    /etc/systemd/system/prometheus.service.d/routegate.conf \
    /var/www/routegate/index.html; do
    [[ ! -e "${root}${path}" && ! -L "${root}${path}" ]] || printf '%s\n' "$path"
  done
}

package_installed() {
  dpkg-query -W -f='${Status}' "$1" 2>/dev/null | grep -qx 'install ok installed'
}

platform_packages() {
  printf '%s\n' \
    ca-certificates \
    certbot \
    curl \
		iproute2 \
    jq \
    nginx \
    openssl \
    postgresql \
    postgresql-client \
    python3 \
    python3-certbot-nginx \
    tar
  if [[ "$ROUTEGATE_INSTALL_PROMETHEUS" == "1" ]]; then
    printf '%s\n' prometheus
  fi
}

print_dependency_plan() {
  local packages=()
  mapfile -t packages < <(platform_packages)
  local package

  printf '\n[RouteGate] Dependency preflight\n'
  for package in "${packages[@]}"; do
    if package_installed "$package"; then
      printf '  [reuse]   %s is already installed\n' "$package"
    else
      printf '  [install] %s will be installed\n' "$package"
    fi
  done
  printf '\n'
}

conflict_recommendations() {
  case "$1" in
    postgresql)
      cat <<'EOF_RECOMMENDATIONS'
Safe options:
  - Keep the existing PostgreSQL deployment unchanged and install RouteGate on a clean VPS (recommended).
  - Back up the existing databases, stop or relocate the service manually, then choose Recheck.
  - Wait for the future advanced external-PostgreSQL deployment profile. Standard All-in-One mode will not rewrite an existing database service.
EOF_RECOMMENDATIONS
      ;;
    nginx)
      cat <<'EOF_RECOMMENDATIONS'
Safe options:
  - Keep the existing nginx sites unchanged and install RouteGate on a clean VPS (recommended).
  - Move the existing site or reverse proxy to another host, then choose Recheck.
  - Use a future advanced existing-reverse-proxy profile. Standard All-in-One mode will not edit unrelated nginx configuration.
EOF_RECOMMENDATIONS
      ;;
    prometheus)
      cat <<'EOF_RECOMMENDATIONS'
Safe options:
  - Keep the existing Prometheus deployment unchanged and select No for RouteGate-managed Prometheus.
  - Install RouteGate on a clean VPS and let RouteGate own its local Prometheus instance.
  - Use the existing Prometheus as an external integration; RouteGate will not rewrite an existing Prometheus installation.
EOF_RECOMMENDATIONS
      ;;
    apache|ports)
      cat <<'EOF_RECOMMENDATIONS'
Safe options:
  - Keep the current web service unchanged and install RouteGate on a clean VPS (recommended).
  - Move or stop the service that owns TCP 80/443 manually, then choose Recheck.
  - Place RouteGate behind an existing proxy only through a reviewed advanced deployment; the standard installer will not guess that configuration.
EOF_RECOMMENDATIONS
      ;;
    routegate-files)
      cat <<'EOF_RECOMMENDATIONS'
Safe options:
  - Preserve the files and review whether they belong to an earlier RouteGate deployment.
  - Restore the matching RouteGate install-state file if this is a valid interrupted installation.
  - Back up and remove only confirmed stale RouteGate files, then choose Recheck.
EOF_RECOMMENDATIONS
      ;;
    *)
      printf 'Use a clean VPS or resolve the detected conflict manually before retrying.\n'
      ;;
  esac
}

show_conflict_diagnostics() {
  local kind=$1
  printf '\n[RouteGate] Conflict diagnostics\n'
  case "$kind" in
    postgresql)
      systemctl --no-pager --full status postgresql.service 2>/dev/null || true
      ss -ltnp 2>/dev/null | awk '$4 ~ /:5432$/' || true
      ;;
    nginx)
      systemctl --no-pager --full status nginx.service 2>/dev/null || true
      find /etc/nginx/sites-enabled -maxdepth 1 -type l -printf 'enabled site: %f -> %l\n' 2>/dev/null || true
      ss -ltnp 2>/dev/null | awk '$4 ~ /:(80|443)$/' || true
      ;;
    prometheus)
      systemctl --no-pager --full status prometheus.service 2>/dev/null || true
      ss -ltnp 2>/dev/null | awk '$4 ~ /:9090$/' || true
      ;;
    apache)
      systemctl --no-pager --full status apache2.service 2>/dev/null || true
      ss -ltnp 2>/dev/null | awk '$4 ~ /:(80|443)$/' || true
      ;;
    ports)
      ss -ltnp 2>/dev/null | awk '$4 ~ /:(80|443)$/' || true
      ;;
    routegate-files)
      collect_routegate_conflicts "" | sed 's/^/conflicting path: /'
      ;;
  esac
  printf '\n'
}

guided_conflict_resolution() {
  local kind=$1
  local summary=$2

  warn "$summary"
  conflict_recommendations "$kind" >&2

  if [[ "$ROUTEGATE_ASSUME_YES" == "1" ]] || ! interactive_tty_available; then
    die "The conflict requires an explicit operator decision. No existing service or configuration was changed."
  fi

  while true; do
    cat >/dev/tty <<'EOF_CHOICES'

Choose the next action:
  1. Stop installation and keep the existing system unchanged [default]
  2. Recheck after resolving the conflict in another SSH session
  3. Show diagnostics and recommendations
EOF_CHOICES
    local answer=""
    read -r -p "Selection [1-3]: " answer </dev/tty
    case "$answer" in
      ""|1)
        die "Installation stopped safely. No conflicting service or configuration was changed."
        ;;
      2)
        log "Rechecking the conflict."
        return 0
        ;;
      3)
        show_conflict_diagnostics "$kind" >/dev/tty
        conflict_recommendations "$kind" >/dev/tty
        ;;
      *)
        printf '[RouteGate] Choose 1, 2, or 3.\n' >/dev/tty
        ;;
    esac
  done
}

detect_conflicts() {
  if [[ -f "$ROUTEGATE_STATE_FILE" ]]; then
    verify_existing_install
    [[ "$ROUTEGATE_RESUME_INSTALL" == "1" ]] && return 0
  fi

  local conflicts=()
  while true; do
    mapfile -t conflicts < <(collect_routegate_conflicts "")
    ((${#conflicts[@]} == 0)) && break
    printf '[RouteGate] Unowned or partial RouteGate files were found:\n' >&2
    printf '  - %s\n' "${conflicts[@]}" >&2
    guided_conflict_resolution routegate-files \
      "RouteGate will not overwrite files without a valid installation ownership state."
  done

  while systemctl is-active postgresql.service >/dev/null 2>&1; do
    guided_conflict_resolution postgresql \
      "An active PostgreSQL deployment was detected. Installed packages are reusable, but an active database service may contain unrelated data."
  done
  while systemctl is-active nginx.service >/dev/null 2>&1; do
    guided_conflict_resolution nginx \
      "An active nginx deployment was detected. RouteGate will not overwrite or guess the purpose of existing sites."
  done
  if [[ "$ROUTEGATE_INSTALL_PROMETHEUS" == "1" ]]; then
    while package_installed prometheus || systemctl is-active prometheus.service >/dev/null 2>&1; do
      guided_conflict_resolution prometheus \
        "An existing Prometheus package or service was detected. RouteGate will not take ownership of an existing monitoring installation."
    done
  fi
  while systemctl is-active apache2.service >/dev/null 2>&1; do
    guided_conflict_resolution apache \
      "Apache is active and may already own TCP 80/443."
  done
  while ss -ltnH | awk '{address=$4; sub(/^.*:/,"",address); if (address == "80" || address == "443") found=1} END {exit !found}'; do
    guided_conflict_resolution ports \
      "Another process is listening on TCP 80 or 443."
  done
}

initialize_install_state() {
  if [[ "$ROUTEGATE_RESUME_INSTALL" == "0" ]]; then
    write_install_state_status installing "$ROUTEGATE_VERSION"
  fi
  install -d -m 0700 "$ROUTEGATE_STATE_DIR"
}

load_or_create_secrets() {
  if [[ -r "$ROUTEGATE_SECRETS_FILE" ]]; then
    ROUTEGATE_DB_PASSWORD=$(sed -n 's/^DB_PASSWORD=//p' "$ROUTEGATE_SECRETS_FILE" | head -n1)
    ROUTEGATE_ADMIN_PASSWORD=$(sed -n 's/^ADMIN_PASSWORD=//p' "$ROUTEGATE_SECRETS_FILE" | head -n1)
    ROUTEGATE_MONITORING_TOKEN=$(sed -n 's/^MONITORING_TOKEN=//p' "$ROUTEGATE_SECRETS_FILE" | head -n1)
    [[ "$ROUTEGATE_DB_PASSWORD" =~ ^[a-f0-9]{64}$ ]] \
      || die "Preserved database secret is invalid."
    [[ "$ROUTEGATE_ADMIN_PASSWORD" =~ ^[A-Za-z0-9_-]{30,}$ ]] \
      || die "Preserved administrator secret is invalid."
    if [[ "$ROUTEGATE_INSTALL_PROMETHEUS" == "1" ]]; then
      if [[ -z "$ROUTEGATE_MONITORING_TOKEN" ]]; then
        ROUTEGATE_MONITORING_TOKEN=$(openssl rand -hex 32)
        printf 'MONITORING_TOKEN=%s\n' "$ROUTEGATE_MONITORING_TOKEN" >>"$ROUTEGATE_SECRETS_FILE"
      fi
      [[ "$ROUTEGATE_MONITORING_TOKEN" =~ ^[a-f0-9]{64}$ ]] \
        || die "Preserved monitoring secret is invalid."
    else
      ROUTEGATE_MONITORING_TOKEN=""
    fi
    return 0
  fi

  ROUTEGATE_DB_PASSWORD=$(openssl rand -hex 32)
  ROUTEGATE_ADMIN_PASSWORD=$(openssl rand -base64 30 | tr -d '\n' | tr '/+' '_-')
  if [[ "$ROUTEGATE_INSTALL_PROMETHEUS" == "1" ]]; then
    ROUTEGATE_MONITORING_TOKEN=$(openssl rand -hex 32)
  fi
  install -m 0600 /dev/null "$ROUTEGATE_SECRETS_FILE"
  cat >"$ROUTEGATE_SECRETS_FILE" <<EOF_SECRETS
DB_PASSWORD=${ROUTEGATE_DB_PASSWORD}
ADMIN_PASSWORD=${ROUTEGATE_ADMIN_PASSWORD}
EOF_SECRETS
  if [[ "$ROUTEGATE_INSTALL_PROMETHEUS" == "1" ]]; then
    printf 'MONITORING_TOKEN=%s\n' "$ROUTEGATE_MONITORING_TOKEN" >>"$ROUTEGATE_SECRETS_FILE"
  fi
  chmod 0600 "$ROUTEGATE_SECRETS_FILE"
}

public_ipv4_candidates() {
  local external=""
  external=$(curl -4fsS --max-time 10 https://api.ipify.org 2>/dev/null || true)
  if [[ "$external" =~ ^([0-9]{1,3}\.){3}[0-9]{1,3}$ ]]; then
    printf '%s\n' "$external"
  fi

  ip -o -4 addr show scope global 2>/dev/null \
    | awk '{split($4,a,"/"); print a[1]}'
}

validate_dns() {
  local resolved=()
  local candidates=()
  mapfile -t resolved < <(getent ahostsv4 "$ROUTEGATE_DOMAIN" | awk '{print $1}' | sort -u)
  ((${#resolved[@]} > 0)) || die "DNS preflight failed: ${ROUTEGATE_DOMAIN} has no IPv4 address."

  mapfile -t candidates < <(public_ipv4_candidates | awk 'NF' | sort -u)
  ((${#candidates[@]} > 0)) || die "Could not determine this VPS public IPv4 address."

  local candidate address
  for candidate in "${candidates[@]}"; do
    for address in "${resolved[@]}"; do
      if [[ "$address" == "$candidate" ]]; then
        ROUTEGATE_PUBLIC_IPV4="$candidate"
        return 0
      fi
    done
  done

  die "DNS mismatch: ${ROUTEGATE_DOMAIN} does not resolve to any IPv4 address detected for this VPS."
}

confirm_installation() {
  log "Installation target:"
  log "  Domain: ${ROUTEGATE_DOMAIN}"
  log "  Administrator: ${ROUTEGATE_ADMIN_EMAIL}"
  log "  Platform: Ubuntu 24.04 LTS / ${ROUTEGATE_ARCH}"
  log "  TLS: Let's Encrypt"
  if [[ "$ROUTEGATE_INSTALL_PROMETHEUS" == "1" ]]; then
    log "  Prometheus: install and manage locally (loopback only)"
  else
    log "  Prometheus: not installed (optional)"
  fi

  [[ "$ROUTEGATE_ASSUME_YES" == "1" ]] && return 0
  interactive_tty_available || die "Non-interactive installation requires --yes."

  local answer=""
  while true; do
    read -r -p "Proceed with RouteGate installation? [Y/n] " answer </dev/tty
    case "$answer" in
      ""|Y|y)
        return 0
        ;;
      N|n)
        die "Installation cancelled."
        ;;
      *)
        printf '[RouteGate] Enter Y to continue or N to cancel.\n' >/dev/tty
        ;;
    esac
  done
}

cleanup_prometheus_install_mask() {
  if [[ -L "$ROUTEGATE_PROMETHEUS_INSTALL_MASK" ]] \
    && [[ "$(readlink "$ROUTEGATE_PROMETHEUS_INSTALL_MASK")" == "/dev/null" ]]; then
    systemctl stop prometheus.service >/dev/null 2>&1 || true
    rm -f "$ROUTEGATE_PROMETHEUS_INSTALL_MASK"
    systemctl daemon-reload >/dev/null 2>&1 || true
  fi
}

prepare_prometheus_package_install() {
  [[ "$ROUTEGATE_INSTALL_PROMETHEUS" == "1" ]] || return 0

  if [[ -e "$ROUTEGATE_PROMETHEUS_INSTALL_MASK" || -L "$ROUTEGATE_PROMETHEUS_INSTALL_MASK" ]]; then
    if [[ "$ROUTEGATE_RESUME_INSTALL" == "1" ]] \
      && [[ -L "$ROUTEGATE_PROMETHEUS_INSTALL_MASK" ]] \
      && [[ "$(readlink "$ROUTEGATE_PROMETHEUS_INSTALL_MASK")" == "/dev/null" ]]; then
      rm -f "$ROUTEGATE_PROMETHEUS_INSTALL_MASK"
    else
      die "Refusing to replace an existing local prometheus.service override."
    fi
  fi

  install -d -m 0755 "$(dirname "$ROUTEGATE_PROMETHEUS_INSTALL_MASK")"
  ln -s /dev/null "$ROUTEGATE_PROMETHEUS_INSTALL_MASK"
  systemctl daemon-reload
}

install_dependencies() {
  log "Installing platform dependencies."
  export DEBIAN_FRONTEND=noninteractive
  local packages=()
  mapfile -t packages < <(platform_packages)

  if [[ "$ROUTEGATE_INSTALL_PROMETHEUS" == "1" ]]; then
    prepare_prometheus_package_install
  fi

  apt-get update >>"$ROUTEGATE_LOG_FILE" 2>&1
  if ! apt-get install -y "${packages[@]}" >>"$ROUTEGATE_LOG_FILE" 2>&1; then
    cleanup_prometheus_install_mask
    die "Failed to install required platform dependencies."
  fi

  if [[ "$ROUTEGATE_INSTALL_PROMETHEUS" == "1" ]]; then
    cleanup_prometheus_install_mask
    package_installed prometheus || die "Prometheus package installation did not complete successfully."
  fi
}

resolve_release_version() {
  if [[ "$ROUTEGATE_VERSION" != "latest" ]]; then
    ROUTEGATE_RESOLVED_VERSION="$ROUTEGATE_VERSION"
    return 0
  fi

  ROUTEGATE_RESOLVED_VERSION=$(curl -fsSL --max-time 30 \
    "https://api.github.com/repos/${ROUTEGATE_REPOSITORY}/releases/latest" \
    | jq -er '.tag_name') \
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

prepare_bundle() {
  ROUTEGATE_WORK_DIR=$(mktemp -d /tmp/routegate-installer.XXXXXX)
  local bundle_path checksum_path
  bundle_path="$ROUTEGATE_WORK_DIR/routegate-bundle.tar.gz"
  checksum_path="$ROUTEGATE_WORK_DIR/SHA256SUMS"

  if [[ -n "$ROUTEGATE_BUNDLE_FILE" ]]; then
    [[ -r "$ROUTEGATE_BUNDLE_FILE" ]] || die "Bundle file is not readable: $ROUTEGATE_BUNDLE_FILE"
    [[ -r "$ROUTEGATE_CHECKSUM_FILE" ]] || die "Checksum file is not readable: $ROUTEGATE_CHECKSUM_FILE"
    cp "$ROUTEGATE_BUNDLE_FILE" "$bundle_path"
    cp "$ROUTEGATE_CHECKSUM_FILE" "$checksum_path"
    ROUTEGATE_BUNDLE_NAME=$(basename "$ROUTEGATE_BUNDLE_FILE")
    ROUTEGATE_RESOLVED_VERSION="${ROUTEGATE_VERSION}"
  elif [[ -n "$ROUTEGATE_BUNDLE_URL" ]]; then
    ROUTEGATE_BUNDLE_NAME=$(basename "${ROUTEGATE_BUNDLE_URL%%\?*}")
    log "Downloading explicit RouteGate bundle."
    curl -fL --retry 3 --connect-timeout 15 --max-time 300 \
      -o "$bundle_path" "$ROUTEGATE_BUNDLE_URL"
    curl -fL --retry 3 --connect-timeout 15 --max-time 60 \
      -o "$checksum_path" "$ROUTEGATE_CHECKSUM_URL"
    ROUTEGATE_RESOLVED_VERSION="${ROUTEGATE_VERSION}"
  else
    resolve_release_version
    ROUTEGATE_BUNDLE_NAME="routegate-${ROUTEGATE_RESOLVED_VERSION}-linux-${ROUTEGATE_ARCH}.tar.gz"
    local urls=()
    mapfile -t urls < <(artifact_urls "$ROUTEGATE_RESOLVED_VERSION" "$ROUTEGATE_ARCH")
    log "Downloading RouteGate ${ROUTEGATE_RESOLVED_VERSION}."
    curl -fL --retry 3 --connect-timeout 15 --max-time 300 \
      -o "$bundle_path" "${urls[0]}"
    curl -fL --retry 3 --connect-timeout 15 --max-time 60 \
      -o "$checksum_path" "${urls[1]}"
  fi

  verify_bundle_checksum "$bundle_path" "$checksum_path" "$ROUTEGATE_BUNDLE_NAME"
  extract_bundle "$bundle_path"
}

verify_bundle_checksum() {
  local bundle_path=$1
  local checksum_path=$2
  local bundle_name=$3
  local expected actual

  expected=$(awk -v name="$bundle_name" '$2 == name || $2 == "*" name {print $1; exit}' "$checksum_path")
  [[ "$expected" =~ ^[a-fA-F0-9]{64}$ ]] \
    || die "No valid SHA-256 entry for ${bundle_name} was found."
  actual=$(sha256sum "$bundle_path" | awk '{print $1}')
  [[ "$actual" == "$expected" ]] || die "Release bundle checksum verification failed."
  log "Release bundle checksum verified."
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

  local required
  for required in \
    bin/routegate-manager \
    bin/routegate-agent \
    manager/migrations \
    frontend/index.html \
    systemd/routegate-manager.service \
    systemd/routegate-agent.service \
    nginx/routegate.conf.example \
    tools/routegate-recovery \
    metadata/manifest.env; do
    [[ -e "$extract_dir/$required" ]] || die "Release bundle is missing ${required}."
  done

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

  local manifest_format manifest_version manifest_os manifest_arch
  manifest_format=$(sed -n 's/^FORMAT_VERSION=//p' "$extract_dir/metadata/manifest.env" | head -n1)
  manifest_version=$(sed -n 's/^VERSION=//p' "$extract_dir/metadata/manifest.env" | head -n1)
  manifest_os=$(sed -n 's/^OS=//p' "$extract_dir/metadata/manifest.env" | head -n1)
  manifest_arch=$(sed -n 's/^ARCH=//p' "$extract_dir/metadata/manifest.env" | head -n1)

  [[ "$manifest_format" == "1" ]] || die "Unsupported release bundle manifest format."
  [[ -n "$manifest_version" ]] || die "Release bundle manifest does not contain VERSION."
  validate_release_version "$manifest_version" || die "Release bundle manifest contains an invalid VERSION."
  [[ "$manifest_version" != "latest" ]] || die "Release bundle manifest cannot use VERSION=latest."
  [[ "$manifest_os" == "linux" ]] || die "Release bundle is not built for Linux."
  [[ "$manifest_arch" == "$ROUTEGATE_ARCH" ]] \
    || die "Release bundle architecture (${manifest_arch:-missing}) does not match this host (${ROUTEGATE_ARCH})."
  [[ -s "$extract_dir/bin/routegate-manager" && -s "$extract_dir/bin/routegate-agent" ]] \
    || die "Release bundle contains an empty RouteGate binary."

  if [[ -z "$ROUTEGATE_RESOLVED_VERSION" || "$ROUTEGATE_RESOLVED_VERSION" == "latest" ]]; then
    ROUTEGATE_RESOLVED_VERSION="$manifest_version"
  elif [[ "$ROUTEGATE_RESOLVED_VERSION" != "$manifest_version" ]]; then
    die "Release bundle version (${manifest_version}) does not match the requested version (${ROUTEGATE_RESOLVED_VERSION})."
  fi
}

install_files() {
  local source_dir="$ROUTEGATE_WORK_DIR/extracted"
  log "Installing RouteGate binaries, migrations, frontend, and service definitions."

  systemctl stop routegate-agent.service routegate-manager.service >/dev/null 2>&1 || true

  if ! id routegate >/dev/null 2>&1; then
    useradd --system --home-dir /var/lib/routegate-manager --create-home --shell /usr/sbin/nologin routegate
  fi

  install -d -m 0755 /etc/routegate
  install -d -m 0755 -o routegate -g routegate /opt/routegate-manager
  install -d -m 0755 /var/www/routegate
  install -d -m 0700 /var/lib/routegate-agent
	install -d -m 0700 /var/lib/routegate-agent/configs /var/lib/routegate-agent/backups /var/lib/routegate-agent/wireguard-configs /var/lib/routegate-agent/wireguard-backups /var/lib/routegate-agent/hysteria2-configs /var/lib/routegate-agent/hysteria2-backups /var/lib/routegate-agent/mtproto-configs /var/lib/routegate-agent/mtproto-backups
  install -d -m 0700 "$ROUTEGATE_STATE_DIR"

  install -m 0755 "$source_dir/bin/routegate-manager" /usr/local/bin/routegate-manager
  install -m 0755 "$source_dir/bin/routegate-agent" /usr/local/bin/routegate-agent
  install -m 0755 "$source_dir/tools/routegate-recovery" /usr/local/sbin/routegate-recovery

  rm -rf /opt/routegate-manager/migrations
  cp -a "$source_dir/manager/migrations" /opt/routegate-manager/migrations
  chown -R routegate:routegate /opt/routegate-manager

  find /var/www/routegate -mindepth 1 -maxdepth 1 -exec rm -rf -- {} +
  cp -a "$source_dir/frontend/." /var/www/routegate/
  chown -R root:root /var/www/routegate
  find /var/www/routegate -type d -exec chmod 0755 {} +
  find /var/www/routegate -type f -exec chmod 0644 {} +

  install -m 0644 "$source_dir/systemd/routegate-manager.service" /etc/systemd/system/routegate-manager.service
  install -m 0644 "$source_dir/systemd/routegate-agent.service" /etc/systemd/system/routegate-agent.service
}

configure_postgresql() {
  log "Configuring the local PostgreSQL database."
  systemctl enable --now postgresql >>"$ROUTEGATE_LOG_FILE" 2>&1

  local db_password=$1
  runuser -u postgres -- psql --set=ON_ERROR_STOP=1 >>"$ROUTEGATE_LOG_FILE" 2>&1 <<SQL
DO \$routegate\$
BEGIN
  IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'routegate') THEN
    CREATE ROLE routegate LOGIN;
  END IF;
END
\$routegate\$;
ALTER ROLE routegate WITH LOGIN PASSWORD '${db_password}';
SELECT 'CREATE DATABASE routegate OWNER routegate'
WHERE NOT EXISTS (SELECT FROM pg_database WHERE datname = 'routegate')\gexec
ALTER DATABASE routegate OWNER TO routegate;
REVOKE ALL ON DATABASE routegate FROM PUBLIC;
GRANT CONNECT ON DATABASE routegate TO routegate;
ALTER SYSTEM SET listen_addresses = 'localhost';
SQL

  systemctl restart postgresql
  runuser -u postgres -- psql -tAc "SELECT 1 FROM pg_roles WHERE rolname='routegate'" | grep -qx 1 \
    || die "PostgreSQL role verification failed."
  runuser -u postgres -- psql -tAc "SELECT 1 FROM pg_database WHERE datname='routegate'" | grep -qx 1 \
    || die "PostgreSQL database verification failed."
}

write_manager_environment() {
  local db_password=$1
  local admin_password=$2

  install -m 0600 /dev/null "$ROUTEGATE_MANAGER_ENV"
  cat >"$ROUTEGATE_MANAGER_ENV" <<EOF_MANAGER
ROUTEGATE_ENV="production"
ROUTEGATE_HTTP_ADDR="127.0.0.1:8080"
ROUTEGATE_PUBLIC_URL="https://${ROUTEGATE_DOMAIN}"
ROUTEGATE_DATABASE_URL="postgres://routegate:${db_password}@127.0.0.1:5432/routegate?sslmode=disable"
ROUTEGATE_LOG_LEVEL="info"
ROUTEGATE_AUTH_SESSION_TTL_HOURS="24"
ROUTEGATE_BOOTSTRAP_ADMIN_EMAIL="${ROUTEGATE_ADMIN_EMAIL}"
ROUTEGATE_BOOTSTRAP_ADMIN_USERNAME="admin"
ROUTEGATE_BOOTSTRAP_ADMIN_PASSWORD="${admin_password}"
ROUTEGATE_BOOTSTRAP_ADMIN_DISPLAY_NAME="RouteGate Administrator"
EOF_MANAGER
  if [[ "$ROUTEGATE_INSTALL_PROMETHEUS" == "1" ]]; then
    [[ "$ROUTEGATE_MONITORING_TOKEN" =~ ^[a-f0-9]{64}$ ]] \
      || die "Monitoring token is unavailable."
    cat >>"$ROUTEGATE_MANAGER_ENV" <<EOF_MONITORING
ROUTEGATE_MONITORING_ENABLED="true"
ROUTEGATE_MONITORING_TOKEN="${ROUTEGATE_MONITORING_TOKEN}"
EOF_MONITORING
  fi
  chmod 0600 "$ROUTEGATE_MANAGER_ENV"
}

wait_for_url() {
  local url=$1
  local attempts=${2:-60}
  local delay=${3:-2}
  local i
  for ((i = 1; i <= attempts; i++)); do
    if curl -fsS --max-time 5 "$url" >/dev/null 2>&1; then
      return 0
    fi
    sleep "$delay"
  done
  return 1
}

start_manager() {
  log "Starting RouteGate Manager and applying database migrations."
  systemctl daemon-reload
  systemctl enable --now routegate-manager >>"$ROUTEGATE_LOG_FILE" 2>&1
  if ! wait_for_url "${ROUTEGATE_LOCAL_API}/api/admin/health" 60 2; then
    journalctl -u routegate-manager -n 100 --no-pager >>"$ROUTEGATE_LOG_FILE" 2>&1 || true
    die "RouteGate Manager did not become healthy."
  fi
}

write_prometheus_config() {
  install -d -m 0755 /etc/prometheus
  install -o root -g prometheus -m 0640 /dev/null "$ROUTEGATE_PROMETHEUS_TOKEN_FILE"
  printf '%s\n' "$ROUTEGATE_MONITORING_TOKEN" >"$ROUTEGATE_PROMETHEUS_TOKEN_FILE"
  chown root:prometheus "$ROUTEGATE_PROMETHEUS_TOKEN_FILE"
  chmod 0640 "$ROUTEGATE_PROMETHEUS_TOKEN_FILE"

  install -o root -g prometheus -m 0640 /dev/null "$ROUTEGATE_PROMETHEUS_CONFIG"
  cat >"$ROUTEGATE_PROMETHEUS_CONFIG" <<EOF_PROMETHEUS
global:
  scrape_interval: 30s
  evaluation_interval: 30s

scrape_configs:
  - job_name: routegate-manager
    metrics_path: /metrics
    authorization:
      type: Bearer
      credentials_file: ${ROUTEGATE_PROMETHEUS_TOKEN_FILE}
    static_configs:
      - targets: ["127.0.0.1:8080"]

  - job_name: routegate-fleet
    metrics_path: /metrics/fleet
    authorization:
      type: Bearer
      credentials_file: ${ROUTEGATE_PROMETHEUS_TOKEN_FILE}
    static_configs:
      - targets: ["127.0.0.1:8080"]
EOF_PROMETHEUS
  chown root:prometheus "$ROUTEGATE_PROMETHEUS_CONFIG"
  chmod 0640 "$ROUTEGATE_PROMETHEUS_CONFIG"
}

write_prometheus_systemd_override() {
  install -d -m 0755 "$(dirname "$ROUTEGATE_PROMETHEUS_OVERRIDE")"
  install -m 0644 /dev/null "$ROUTEGATE_PROMETHEUS_OVERRIDE"
  cat >"$ROUTEGATE_PROMETHEUS_OVERRIDE" <<EOF_OVERRIDE
[Service]
ExecStart=
ExecStart=/usr/bin/prometheus --config.file=${ROUTEGATE_PROMETHEUS_CONFIG} --storage.tsdb.path=${ROUTEGATE_PROMETHEUS_STORAGE} --web.listen-address=127.0.0.1:9090
EOF_OVERRIDE
}

write_monitoring_curl_config() {
  local path=$1
  install -m 0600 /dev/null "$path"
  printf 'header = "Authorization: Bearer %s"\n' "$ROUTEGATE_MONITORING_TOKEN" >"$path"
  chmod 0600 "$path"
}

wait_for_prometheus_targets() {
  local attempts=${1:-30}
  local delay=${2:-2}
  local i response up_count
  for ((i = 1; i <= attempts; i++)); do
    response=$(curl -fsS --max-time 5 "http://127.0.0.1:9090/api/v1/targets" 2>/dev/null || true)
    if [[ -n "$response" ]]; then
      up_count=$(jq -r '[.data.activeTargets[]? | select(.health == "up") | .labels.job] | unique | length' <<<"$response" 2>/dev/null || printf '0')
      if [[ "$up_count" == "2" ]]; then
        return 0
      fi
    fi
    sleep "$delay"
  done
  return 1
}

configure_managed_prometheus() {
  [[ "$ROUTEGATE_INSTALL_PROMETHEUS" == "1" ]] || return 0
  log "Configuring RouteGate-managed Prometheus."

  command -v prometheus >/dev/null 2>&1 || die "The Prometheus package is not installed."
  command -v promtool >/dev/null 2>&1 || die "promtool is not available."
  getent group prometheus >/dev/null 2>&1 || die "The Prometheus system group is unavailable."

  install -d -o prometheus -g prometheus -m 0750 "$ROUTEGATE_PROMETHEUS_STORAGE"
  write_prometheus_config
  write_prometheus_systemd_override

  promtool check config "$ROUTEGATE_PROMETHEUS_CONFIG" >>"$ROUTEGATE_LOG_FILE" 2>&1 \
    || die "Prometheus configuration validation failed."

  local auth_config="$ROUTEGATE_WORK_DIR/monitoring-auth.curl"
  write_monitoring_curl_config "$auth_config"
  curl -fsS --max-time 10 --config "$auth_config" "${ROUTEGATE_LOCAL_API}/metrics" >/dev/null \
    || die "RouteGate Manager metrics endpoint verification failed."
  curl -fsS --max-time 10 --config "$auth_config" "${ROUTEGATE_LOCAL_API}/metrics/fleet" >/dev/null \
    || die "RouteGate fleet metrics endpoint verification failed."

  systemctl daemon-reload
  systemctl enable prometheus >>"$ROUTEGATE_LOG_FILE" 2>&1
  systemctl restart prometheus >>"$ROUTEGATE_LOG_FILE" 2>&1
  wait_for_url "http://127.0.0.1:9090/-/ready" 30 1 \
    || die "Prometheus did not become ready."

  if ss -ltnH | awk '{print $4}' | grep -Eq '(^0\.0\.0\.0:9090$|^\[::\]:9090$)'; then
    die "Prometheus is unexpectedly listening on a public wildcard address."
  fi
  if ! ss -ltnH | awk '{print $4}' | grep -Eq '(^127\.0\.0\.1:9090$|^\[::1\]:9090$)'; then
    die "Prometheus is not listening on the expected loopback address."
  fi

  wait_for_prometheus_targets 30 2 \
    || die "Prometheus did not successfully scrape both RouteGate metrics targets."
}

configure_nginx_and_tls() {
  local source_dir="$ROUTEGATE_WORK_DIR/extracted"
  log "Configuring nginx for ${ROUTEGATE_DOMAIN}."

  install -m 0644 "$source_dir/nginx/routegate.conf.example" /etc/nginx/sites-available/routegate
  sed -i "s/routegate\\.example\\.com/${ROUTEGATE_DOMAIN}/g" /etc/nginx/sites-available/routegate
  ln -sfn /etc/nginx/sites-available/routegate /etc/nginx/sites-enabled/routegate
  rm -f /etc/nginx/sites-enabled/default

  nginx -t >>"$ROUTEGATE_LOG_FILE" 2>&1
  systemctl enable --now nginx >>"$ROUTEGATE_LOG_FILE" 2>&1
  systemctl reload nginx

  if command -v ufw >/dev/null 2>&1 && ufw status | grep -q '^Status: active'; then
    log "UFW is active; allowing nginx HTTP/HTTPS without changing SSH policy."
    ufw allow 'Nginx Full' >>"$ROUTEGATE_LOG_FILE" 2>&1
  fi

  wait_for_url "http://${ROUTEGATE_DOMAIN}/api/admin/health" 30 2 \
    || die "HTTP preflight failed before requesting the TLS certificate."

  log "Requesting and configuring the Let's Encrypt certificate."
  certbot --nginx \
    --non-interactive \
    --agree-tos \
    --redirect \
    --no-eff-email \
    --email "$ROUTEGATE_EMAIL" \
    -d "$ROUTEGATE_DOMAIN" >>"$ROUTEGATE_LOG_FILE" 2>&1

  install -d -m 0755 /etc/letsencrypt/renewal-hooks/deploy
  cat >/etc/letsencrypt/renewal-hooks/deploy/routegate-nginx-reload <<'EOF_CERTIFICATE_HOOK'
#!/bin/sh
set -eu
/usr/sbin/nginx -t
/usr/bin/systemctl reload nginx
EOF_CERTIFICATE_HOOK
  chmod 0755 /etc/letsencrypt/renewal-hooks/deploy/routegate-nginx-reload
  systemctl enable --now certbot.timer >>"$ROUTEGATE_LOG_FILE" 2>&1
  systemctl is-enabled --quiet certbot.timer \
    || die "Certbot renewal timer is not enabled."
  systemctl is-active --quiet certbot.timer \
    || die "Certbot renewal timer is not active."

  nginx -t >>"$ROUTEGATE_LOG_FILE" 2>&1
  systemctl reload nginx
  wait_for_url "https://${ROUTEGATE_DOMAIN}/api/admin/health" 30 2 \
    || die "HTTPS health check failed after certificate installation."
}

manager_login_token() {
  local admin_password=$1
  local response
  response=$(printf '%s' "$admin_password" \
    | jq -Rs --arg login "$ROUTEGATE_ADMIN_EMAIL" '{login:$login,password:.}' \
    | curl -fsS --max-time 15 \
        -H 'Content-Type: application/json' \
        --data-binary @- \
        "${ROUTEGATE_LOCAL_API}/api/v1/auth/login") \
    || die "First administrator login verification failed."

  jq -er '.token' <<<"$response" || die "Manager login response did not contain a session token."
}

write_auth_curl_config() {
  local token=$1
  local path=$2
  install -m 0600 /dev/null "$path"
  printf 'header = "Authorization: Bearer %s"\n' "$token" >"$path"
  chmod 0600 "$path"
}

find_or_create_local_server() {
  local session_token=$1
  local auth_config="$ROUTEGATE_WORK_DIR/admin-auth.curl"
  local list_response server_id server_response body_file
  write_auth_curl_config "$session_token" "$auth_config"

  list_response=$(curl -fsS --max-time 15 \
    --config "$auth_config" \
    "${ROUTEGATE_LOCAL_API}/api/v1/servers?search=$(printf '%s' "$ROUTEGATE_SERVER_NAME" | jq -sRr @uri)") \
    || die "Failed to query existing RouteGate servers."
  server_id=$(jq -er --arg name "$ROUTEGATE_SERVER_NAME" \
    '.items[]? | select(.name == $name) | .id' <<<"$list_response" | head -n1 || true)
  if [[ -n "$server_id" ]]; then
    printf '%s' "$server_id"
    return 0
  fi

  body_file="$ROUTEGATE_WORK_DIR/create-server.json"
  jq -n \
    --arg name "$ROUTEGATE_SERVER_NAME" \
    --arg description "RouteGate All-in-One host installed by the Clean VPS Installer" \
    --arg public_ip "$ROUTEGATE_PUBLIC_IPV4" \
    '{name:$name,deploymentRole:"hybrid",description:$description,publicIp:$public_ip}' >"$body_file"
  chmod 0600 "$body_file"

  server_response=$(curl -fsS --max-time 15 \
    --config "$auth_config" \
    -H 'Content-Type: application/json' \
    --data-binary "@${body_file}" \
    "${ROUTEGATE_LOCAL_API}/api/v1/servers") \
    || die "Failed to create the local RouteGate server record."
  jq -er '.id' <<<"$server_response" \
    || die "Server creation response did not contain an ID."
}

bootstrap_local_agent() {
  local admin_password=$1

  if agent_has_credentials; then
    log "The local Agent already has persistent credentials; preserving them."
    systemctl daemon-reload
    systemctl enable --now routegate-agent >>"$ROUTEGATE_LOG_FILE" 2>&1
    return 0
  fi

  if [[ -r "$ROUTEGATE_AGENT_CONFIG" ]] && grep -q '^registration_token:' "$ROUTEGATE_AGENT_CONFIG"; then
    log "Resuming local Agent registration with the preserved one-time token."
    systemctl daemon-reload
    systemctl reset-failed routegate-agent.service >/dev/null 2>&1 || true
    systemctl enable --now routegate-agent >>"$ROUTEGATE_LOG_FILE" 2>&1
    if wait_for_agent_registration; then
      return 0
    fi

    warn "The preserved Agent registration token was not accepted; rotating it safely."
    systemctl stop routegate-agent.service >/dev/null 2>&1 || true
    sed -i '/^registration_token:/d' "$ROUTEGATE_AGENT_CONFIG"
  fi

  local session_token server_id token_response registration_token auth_config
  log "Creating the local All-in-One server and Agent registration token."
  session_token=$(manager_login_token "$admin_password")
  server_id=$(find_or_create_local_server "$session_token")
  auth_config="$ROUTEGATE_WORK_DIR/admin-auth.curl"
  write_auth_curl_config "$session_token" "$auth_config"

  token_response=$(curl -fsS --max-time 15 \
    --config "$auth_config" \
    -X POST \
    "${ROUTEGATE_LOCAL_API}/api/v1/servers/${server_id}/registration-token") \
    || die "Failed to create the local Agent registration token."
  registration_token=$(jq -er '.registrationToken' <<<"$token_response") \
    || die "Registration-token response did not contain a token."

  install -m 0600 /dev/null "$ROUTEGATE_AGENT_CONFIG"
  cat >"$ROUTEGATE_AGENT_CONFIG" <<EOF_AGENT
manager_url: "https://${ROUTEGATE_DOMAIN}"
registration_token: "${registration_token}"
heartbeat_interval_seconds: 10
config_staging_dir: "/var/lib/routegate-agent/configs"
active_config_path: "/etc/sing-box/config.json"
config_backup_dir: "/var/lib/routegate-agent/backups"
sing_box_path: "/usr/bin/sing-box"
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
EOF_AGENT
  chmod 0600 "$ROUTEGATE_AGENT_CONFIG"

  systemctl daemon-reload
  systemctl enable --now routegate-agent >>"$ROUTEGATE_LOG_FILE" 2>&1
  wait_for_agent_registration || die "The local RouteGate Agent did not register successfully."
}

agent_has_credentials() {
  [[ -r "$ROUTEGATE_AGENT_CONFIG" ]] || return 1
  local agent_id server_id agent_token
  agent_id=$(sed -n 's/^agent_id:[[:space:]]*"\(.*\)"[[:space:]]*$/\1/p' "$ROUTEGATE_AGENT_CONFIG" | head -n1)
  server_id=$(sed -n 's/^server_id:[[:space:]]*"\(.*\)"[[:space:]]*$/\1/p' "$ROUTEGATE_AGENT_CONFIG" | head -n1)
  agent_token=$(sed -n 's/^agent_token:[[:space:]]*"\(.*\)"[[:space:]]*$/\1/p' "$ROUTEGATE_AGENT_CONFIG" | head -n1)
  [[ -n "$agent_id" && -n "$server_id" && -n "$agent_token" ]]
}

wait_for_agent_registration() {
  local i
  for ((i = 1; i <= 60; i++)); do
    if agent_has_credentials; then
      return 0
    fi
    sleep 2
  done
  journalctl -u routegate-agent -n 100 --no-pager >>"$ROUTEGATE_LOG_FILE" 2>&1 || true
  return 1
}

create_initial_setup_link() {
  local admin_password=$1
  local session_token auth_config response token
  log "Creating the one-time administrator activation link."

  session_token=$(manager_login_token "$admin_password")
  auth_config="$ROUTEGATE_WORK_DIR/setup-auth.curl"
  write_auth_curl_config "$session_token" "$auth_config"

  response=$(curl -fsS --max-time 15 \
    --config "$auth_config" \
    -H 'Content-Type: application/json' \
    -X POST \
    "${ROUTEGATE_LOCAL_API}/api/v1/auth/initial-setup-token") \
    || die "Failed to create the initial administrator activation link."

  token=$(jq -er '.token' <<<"$response") \
    || die "Initial setup response did not contain a token."
  ROUTEGATE_SETUP_EXPIRES_AT=$(jq -er '.expires_at' <<<"$response") \
    || die "Initial setup response did not contain an expiration time."
  ROUTEGATE_SETUP_URL="https://${ROUTEGATE_DOMAIN}/setup#token=${token}"
}

remove_bootstrap_environment() {
  log "Removing first-user bootstrap secrets from the Manager environment."
  sed -i '/^ROUTEGATE_BOOTSTRAP_ADMIN_/d' "$ROUTEGATE_MANAGER_ENV"
  chmod 0600 "$ROUTEGATE_MANAGER_ENV"
  systemctl restart routegate-manager
  wait_for_url "${ROUTEGATE_LOCAL_API}/api/admin/health" 60 2 \
    || die "Manager failed after removing bootstrap environment values."
}

write_install_state() {
  write_install_state_status complete "$ROUTEGATE_RESOLVED_VERSION"
  rm -f "$ROUTEGATE_SECRETS_FILE"
}

write_initial_credentials() {
  local admin_password=$1
  install -m 0600 /dev/null "$ROUTEGATE_CREDENTIALS_FILE"
  cat >"$ROUTEGATE_CREDENTIALS_FILE" <<EOF_CREDENTIALS
RouteGate first access

Setup URL: ${ROUTEGATE_SETUP_URL}
Administrator email: ${ROUTEGATE_ADMIN_EMAIL}
Setup link expires at: ${ROUTEGATE_SETUP_EXPIRES_AT}

Open the setup URL and choose your own password. The link is single-use.

Recovery only:
A unique bootstrap password is retained below so the server owner can recover
if the setup link expires before activation. Do not send this password by email.
Bootstrap password: ${admin_password}

Remove this file after activation and password verification:
  sudo rm -f ${ROUTEGATE_CREDENTIALS_FILE}
EOF_CREDENTIALS
  chmod 0600 "$ROUTEGATE_CREDENTIALS_FILE"
}

verify_final_state() {
  log "Verifying the installed RouteGate stack."
  local services=(postgresql nginx routegate-manager routegate-agent certbot.timer)
  if [[ "$ROUTEGATE_INSTALL_PROMETHEUS" == "1" ]]; then
    services+=(prometheus)
  fi
  local service
  for service in "${services[@]}"; do
    systemctl is-enabled "$service" >/dev/null 2>&1 || die "${service} is not enabled."
    systemctl is-active "$service" >/dev/null 2>&1 || die "${service} is not active."
  done
  wait_for_url "https://${ROUTEGATE_DOMAIN}/api/admin/health" 10 2 \
    || die "Final public HTTPS health check failed."
  agent_has_credentials || die "Final Agent credential verification failed."
  [[ -x /usr/local/sbin/routegate-recovery ]] || die "RouteGate recovery tool is not installed."
  [[ -x /etc/letsencrypt/renewal-hooks/deploy/routegate-nginx-reload ]] \
    || die "RouteGate certificate deploy hook is not installed."

  if ss -ltnH | awk '{print $4}' | grep -Eq '(^|:)5432$' \
    && ss -ltnH | awk '{print $4}' | grep -Eq '(^0\.0\.0\.0:5432$|^\[::\]:5432$)'; then
    die "PostgreSQL is unexpectedly listening on a public wildcard address."
  fi

  if [[ "$ROUTEGATE_INSTALL_PROMETHEUS" == "1" ]]; then
    wait_for_url "http://127.0.0.1:9090/-/ready" 5 1 \
      || die "Final Prometheus readiness check failed."
    if ss -ltnH | awk '{print $4}' | grep -Eq '(^0\.0\.0\.0:9090$|^\[::\]:9090$)'; then
      die "Prometheus is unexpectedly listening on a public wildcard address."
    fi
  fi
}

bootstrap_trusted_updater() {
  local source_dir="$ROUTEGATE_WORK_DIR/extracted"
  local helper="$source_dir/tools/routegate-update-bootstrap.sh"
  [[ -f "$helper" && ! -L "$helper" ]] || die "Release bundle is missing the trusted updater bootstrap helper."

  log "Bootstrapping the local trusted updater boundary."
  env -u RG_UPDATE_ROOT bash "$helper" \
    || die "Trusted updater bootstrap failed. The platform remains installed, but this node is not update-ready."
}

print_success() {
  printf '\nRouteGate installation completed successfully.\n\n'
  printf 'NEXT ACTION — Complete administrator setup\n\n'
  printf 'Ctrl+click the link below, or copy the complete URL and paste it into your browser:\n\n'
  printf '  \033]8;;%s\033\\Open RouteGate first-time setup\033]8;;\033\\\n\n' "$ROUTEGATE_SETUP_URL"
  printf '  %s\n\n' "$ROUTEGATE_SETUP_URL"

  cat <<EOF_SUCCESS
Administrator:
  ${ROUTEGATE_ADMIN_EMAIL}

The setup link is single-use and expires at ${ROUTEGATE_SETUP_EXPIRES_AT}.
After activation, RouteGate signs the administrator in automatically.

Lost the setup link? Recovery details are stored root-only in:
  ${ROUTEGATE_CREDENTIALS_FILE}

Read them with:
  sudo cat ${ROUTEGATE_CREDENTIALS_FILE}

SMTP delivery is not configured by default. RouteGate does not email passwords.
EOF_SUCCESS

  if [[ "$ROUTEGATE_INSTALL_PROMETHEUS" == "1" ]]; then
    cat <<'EOF_PROMETHEUS_SUCCESS'

Prometheus:
  Installed and managed by RouteGate.
  Web/API listener: 127.0.0.1:9090 (loopback only).
  RouteGate metrics history is enabled.
EOF_PROMETHEUS_SUCCESS
  else
    cat <<'EOF_PROMETHEUS_SUCCESS'

Prometheus:
  Optional component not installed.
  RouteGate health, alerts, diagnostics, and current metrics remain available.
  Prometheus can be added later through RouteGate Analytics.
EOF_PROMETHEUS_SUCCESS
  fi

  cat <<EOF_SUCCESS

Services:
  PostgreSQL, nginx, RouteGate Manager, and RouteGate Agent are active and enabled.

After administrator activation:
  Open the local server and use the guided Install sing-box action.

Installer log:
  ${ROUTEGATE_LOG_FILE}

Recovery toolkit:
  sudo routegate-recovery status
  sudo routegate-recovery renew-certificate
EOF_SUCCESS
}

main() {
  trap 'on_error $LINENO' ERR
  trap cleanup EXIT

  parse_args "$@"
  set_install_stage host_preflight
  require_root
  initialize_logging
  acquire_install_lock
  set_install_stage input_preflight
  prompt_for_inputs
  validate_inputs
  validate_platform
  verify_completed_install_before_repository_preflight
  set_install_stage apt_repository_preflight
  validate_apt_repository_trust
  print_dependency_plan
  set_install_stage conflict_preflight
  detect_conflicts
  set_install_stage dns_preflight
  validate_dns
  confirm_installation
  initialize_install_state
  set_install_stage dependencies
  install_dependencies
  set_install_stage bundle
  prepare_bundle
  set_install_stage platform_files
  install_files
  load_or_create_secrets

  set_install_stage postgresql
  configure_postgresql "$ROUTEGATE_DB_PASSWORD"
  set_install_stage manager
  write_manager_environment "$ROUTEGATE_DB_PASSWORD" "$ROUTEGATE_ADMIN_PASSWORD"
  start_manager
  set_install_stage monitoring
  configure_managed_prometheus
  set_install_stage nginx_tls
  configure_nginx_and_tls
  set_install_stage agent
  bootstrap_local_agent "$ROUTEGATE_ADMIN_PASSWORD"
  set_install_stage admin_setup
  create_initial_setup_link "$ROUTEGATE_ADMIN_PASSWORD"
  remove_bootstrap_environment
  write_initial_credentials "$ROUTEGATE_ADMIN_PASSWORD"
  set_install_stage verification
  verify_final_state
  set_install_stage updater
  bootstrap_trusted_updater
  set_install_stage complete
  write_install_state
  print_success
}

if [[ "${BASH_SOURCE[0]:-$0}" == "$0" ]]; then
  main "$@"
fi
