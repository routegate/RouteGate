#!/usr/bin/env bash

set -Eeuo pipefail
IFS=$'\n\t'
umask 077

ROUTEGATE_STATE_FILE="${ROUTEGATE_STATE_FILE:-/etc/routegate/install-state.env}"
ROUTEGATE_MANAGER_ENV="${ROUTEGATE_MANAGER_ENV:-/etc/routegate/manager.env}"
ROUTEGATE_NGINX_SITE="${ROUTEGATE_NGINX_SITE:-/etc/nginx/sites-available/routegate}"
ROUTEGATE_GRAFANA_CREDENTIALS_FILE="${ROUTEGATE_GRAFANA_CREDENTIALS_FILE:-/root/routegate-grafana-access.txt}"
ROUTEGATE_GRAFANA_CONFIG="${ROUTEGATE_GRAFANA_CONFIG:-/etc/grafana/grafana.ini}"
ROUTEGATE_GRAFANA_DATASOURCE="${ROUTEGATE_GRAFANA_DATASOURCE:-/etc/grafana/provisioning/datasources/routegate-prometheus.yaml}"
ROUTEGATE_GRAFANA_DASHBOARD_PROVIDER="${ROUTEGATE_GRAFANA_DASHBOARD_PROVIDER:-/etc/grafana/provisioning/dashboards/routegate.yaml}"
ROUTEGATE_GRAFANA_DASHBOARD_DIR="${ROUTEGATE_GRAFANA_DASHBOARD_DIR:-/var/lib/grafana/routegate-dashboards}"
ROUTEGATE_GRAFANA_DASHBOARD="${ROUTEGATE_GRAFANA_DASHBOARD:-${ROUTEGATE_GRAFANA_DASHBOARD_DIR}/routegate-fleet-overview.json}"
ROUTEGATE_GRAFANA_APT_KEY="${ROUTEGATE_GRAFANA_APT_KEY:-/etc/apt/keyrings/grafana.asc}"
ROUTEGATE_GRAFANA_APT_LIST="${ROUTEGATE_GRAFANA_APT_LIST:-/etc/apt/sources.list.d/grafana.list}"
ROUTEGATE_GRAFANA_BOOTSTRAP_OVERRIDE="${ROUTEGATE_GRAFANA_BOOTSTRAP_OVERRIDE:-/etc/systemd/system/grafana-server.service.d/routegate-bootstrap.conf}"
ROUTEGATE_GRAFANA_INSTALL_MASK="${ROUTEGATE_GRAFANA_INSTALL_MASK:-/etc/systemd/system/grafana-server.service}"
ROUTEGATE_GRAFANA_SERVICE="grafana-server.service"
ROUTEGATE_PROMETHEUS_URL="${ROUTEGATE_PROMETHEUS_URL:-http://127.0.0.1:9090}"
ROUTEGATE_GRAFANA_KEY_FINGERPRINT="B53AE77BADB630A683046005963FA27710458545"
ROUTEGATE_DOMAIN=""
ROUTEGATE_GRAFANA_URL=""
ROUTEGATE_GRAFANA_ADMIN_PASSWORD=""
ROUTEGATE_BACKUP_DIR=""
ROUTEGATE_BACKUPS_READY=0
ROUTEGATE_MUTATED=0
ROUTEGATE_SUCCESS=0

log() { printf '[RouteGate Grafana] %s\n' "$*"; }
warn() { printf '[RouteGate Grafana] WARNING: %s\n' "$*" >&2; }
die() { printf '[RouteGate Grafana] ERROR: %s\n' "$*" >&2; exit 1; }

package_installed() {
  dpkg-query -W -f='${Status}' "$1" 2>/dev/null | grep -qx 'install ok installed'
}

state_value() {
  local key=$1
  [[ -r "$ROUTEGATE_STATE_FILE" ]] || return 1
  sed -n "s/^${key}=//p" "$ROUTEGATE_STATE_FILE" | head -n1
}

wait_for_http_200() {
  local url=$1
  local attempts=${2:-30}
  local delay=${3:-1}
  local host_header=${4:-}
  local code i
  for ((i = 1; i <= attempts; i++)); do
    if [[ -n "$host_header" ]]; then
      code=$(curl -sS --max-time 5 -o /dev/null -w '%{http_code}' -H "Host: ${host_header}" "$url" 2>/dev/null || true)
    else
      code=$(curl -sS --max-time 5 -o /dev/null -w '%{http_code}' "$url" 2>/dev/null || true)
    fi
    [[ "$code" == "200" ]] && return 0
    sleep "$delay"
  done
  return 1
}

local_grafana_api() {
  local path=$1
  shift
  curl -fsS --max-time 10 -H "Host: ${ROUTEGATE_DOMAIN}" "$@" "http://127.0.0.1:3000/grafana${path}"
}

require_root() {
  [[ ${EUID:-$(id -u)} -eq 0 ]] || die "Run this installer as root or through sudo."
}

validate_platform() {
  local os_id version_id arch
  os_id=$(sed -n 's/^ID=//p' /etc/os-release 2>/dev/null | head -n1 | tr -d '"')
  version_id=$(sed -n 's/^VERSION_ID=//p' /etc/os-release 2>/dev/null | head -n1 | tr -d '"')
  arch=$(dpkg --print-architecture 2>/dev/null || true)
  [[ "$os_id" == "ubuntu" && "$version_id" == "24.04" && "$arch" == "amd64" ]] \
    || die "Managed Grafana currently requires Ubuntu 24.04 LTS on amd64."
  local required_command
  for required_command in systemctl curl nginx openssl python3 ss apt-get; do
    command -v "$required_command" >/dev/null 2>&1 || die "${required_command} is required."
  done
}

load_routegate_identity() {
  [[ -r "$ROUTEGATE_STATE_FILE" ]] || die "RouteGate installer ownership state is missing: ${ROUTEGATE_STATE_FILE}."
  [[ -r "$ROUTEGATE_MANAGER_ENV" ]] || die "RouteGate Manager environment is missing: ${ROUTEGATE_MANAGER_ENV}."
  [[ -r "$ROUTEGATE_NGINX_SITE" ]] || die "RouteGate nginx site is missing: ${ROUTEGATE_NGINX_SITE}."

  local status public_url state_domain
  status=$(state_value STATUS || true)
  state_domain=$(state_value DOMAIN || true)
  public_url=$(sed -n 's/^ROUTEGATE_PUBLIC_URL="\{0,1\}\([^"[:space:]]*\)"\{0,1\}$/\1/p' "$ROUTEGATE_MANAGER_ENV" | head -n1)

  [[ "$status" == "complete" ]] || die "RouteGate installation state is not complete."
  [[ "$public_url" == https://* ]] || die "ROUTEGATE_PUBLIC_URL must use HTTPS before Grafana can be exposed safely."

  ROUTEGATE_DOMAIN=${public_url#https://}
  ROUTEGATE_DOMAIN=${ROUTEGATE_DOMAIN%/}
  [[ "$ROUTEGATE_DOMAIN" != */* && "$ROUTEGATE_DOMAIN" != *:* && "$ROUTEGATE_DOMAIN" == *.* ]] \
    || die "Could not derive a valid RouteGate domain from ROUTEGATE_PUBLIC_URL."
  [[ -z "$state_domain" || "$state_domain" == "$ROUTEGATE_DOMAIN" ]] \
    || die "RouteGate state and Manager public URL refer to different domains."
  ROUTEGATE_GRAFANA_URL="https://${ROUTEGATE_DOMAIN}/grafana/"
}

verify_prometheus_dependency() {
  [[ "$(state_value PROMETHEUS_MANAGED || true)" == "1" ]] \
    || die "RouteGate-managed Prometheus is required before installing Grafana. Enable historical metrics first."
  systemctl is-active --quiet prometheus.service || die "RouteGate-managed Prometheus is not active."
  wait_for_http_200 "${ROUTEGATE_PROMETHEUS_URL}/-/ready" 10 1 \
    || die "RouteGate-managed Prometheus is not ready on 127.0.0.1:9090."
}

validate_loopback_listener() {
  local port=$1 service_name=$2
  if ss -ltnH | awk '{print $4}' | grep -Eq "(^0\\.0\\.0\\.0:${port}$|^\\[::\\]:${port}$)"; then
    die "${service_name} is unexpectedly listening on a public wildcard address."
  fi
  ss -ltnH | awk '{print $4}' | grep -Eq "(^127\\.0\\.0\\.1:${port}$|^\\[::1\\]:${port}$)" \
    || die "${service_name} is not listening on the expected loopback address."
}

verify_existing_managed_install() {
  local managed
  managed=$(state_value GRAFANA_MANAGED || true)
  managed=${managed:-0}
  [[ "$managed" == "0" || "$managed" == "1" ]] || die "Invalid GRAFANA_MANAGED state: ${managed}."
  [[ "$managed" == "1" ]] || return 1

  package_installed grafana || die "RouteGate state claims Grafana ownership, but the grafana package is missing."
  systemctl is-enabled --quiet "$ROUTEGATE_GRAFANA_SERVICE" || die "RouteGate-managed Grafana is not enabled."
  systemctl is-active --quiet "$ROUTEGATE_GRAFANA_SERVICE" || die "RouteGate-managed Grafana is not active."
  validate_loopback_listener 3000 Grafana
  wait_for_http_200 "http://127.0.0.1:3000/grafana/api/health" 10 1 "$ROUTEGATE_DOMAIN" \
    || die "RouteGate-managed Grafana health endpoint is unavailable."
  grep -Fq '# BEGIN ROUTEGATE MANAGED GRAFANA' "$ROUTEGATE_NGINX_SITE" \
    || die "RouteGate-managed Grafana nginx gateway is missing."
  wait_for_http_200 "${ROUTEGATE_GRAFANA_URL}api/health" 10 1 \
    || die "RouteGate-managed Grafana is not reachable through RouteGate HTTPS."

  log "Grafana is already installed and healthy."
  log "Open ${ROUTEGATE_GRAFANA_URL}"
  return 0
}

refuse_unowned_grafana() {
  package_installed grafana && die "An existing Grafana package was found. RouteGate will not take ownership of it."
  [[ "$(systemctl is-active "$ROUTEGATE_GRAFANA_SERVICE" 2>/dev/null || true)" != "active" ]] \
    || die "An existing Grafana service is active. RouteGate will not take ownership of it."
  [[ ! -e "$ROUTEGATE_GRAFANA_APT_LIST" && ! -e "$ROUTEGATE_GRAFANA_APT_KEY" ]] \
    || die "Existing Grafana APT repository configuration was found. RouteGate will not overwrite it."
  [[ ! -e /etc/grafana/grafana.ini && ! -e /var/lib/grafana/grafana.db ]] \
    || die "Existing Grafana configuration or data was found. RouteGate will not overwrite it."
  [[ ! -e "$ROUTEGATE_GRAFANA_INSTALL_MASK" && ! -L "$ROUTEGATE_GRAFANA_INSTALL_MASK" ]] \
    || die "An existing local grafana-server.service override was found."
}

prepare_backups() {
  ROUTEGATE_BACKUP_DIR=$(mktemp -d /root/routegate-grafana-install.XXXXXX)
  cp -a "$ROUTEGATE_STATE_FILE" "$ROUTEGATE_BACKUP_DIR/install-state.env"
  cp -a "$ROUTEGATE_NGINX_SITE" "$ROUTEGATE_BACKUP_DIR/nginx-routegate"
  ROUTEGATE_BACKUPS_READY=1
}

rollback() {
  local rc=$?
  trap - EXIT ERR
  set +e
  if [[ "$ROUTEGATE_SUCCESS" != "1" && "$ROUTEGATE_MUTATED" == "1" ]]; then
    warn "Grafana installation failed; restoring the pre-Grafana RouteGate state."
    systemctl disable --now "$ROUTEGATE_GRAFANA_SERVICE" >/dev/null 2>&1 || true
    rm -f "$ROUTEGATE_GRAFANA_BOOTSTRAP_OVERRIDE" "$ROUTEGATE_GRAFANA_INSTALL_MASK"
    rmdir /etc/systemd/system/grafana-server.service.d >/dev/null 2>&1 || true
    systemctl daemon-reload >/dev/null 2>&1 || true

    if [[ "$ROUTEGATE_BACKUPS_READY" == "1" ]]; then
      install -m 0600 "$ROUTEGATE_BACKUP_DIR/install-state.env" "$ROUTEGATE_STATE_FILE"
      install -m 0644 "$ROUTEGATE_BACKUP_DIR/nginx-routegate" "$ROUTEGATE_NGINX_SITE"
    fi

    rm -f "$ROUTEGATE_GRAFANA_CREDENTIALS_FILE"
    if package_installed grafana; then
      DEBIAN_FRONTEND=noninteractive apt-get purge -y grafana >/dev/null 2>&1 || true
    fi
    rm -f "$ROUTEGATE_GRAFANA_APT_LIST" "$ROUTEGATE_GRAFANA_APT_KEY"
    apt-get update >/dev/null 2>&1 || true
    if nginx -t >/dev/null 2>&1; then
      systemctl reload nginx >/dev/null 2>&1 || true
    fi
  fi
  [[ -z "$ROUTEGATE_BACKUP_DIR" ]] || rm -rf "$ROUTEGATE_BACKUP_DIR"
  exit "$rc"
}

install_grafana_package() {
  log "Installing Grafana OSS from the official Grafana stable APT repository."
  export DEBIAN_FRONTEND=noninteractive
  apt-get update >/dev/null
  apt-get install -y apt-transport-https gnupg >/dev/null
  command -v gpg >/dev/null 2>&1 || die "gnupg installation did not provide gpg."

  install -d -m 0755 /etc/apt/keyrings
  curl -fsSL --connect-timeout 15 --max-time 60 https://apt.grafana.com/gpg-full.key -o "$ROUTEGATE_GRAFANA_APT_KEY"
  chmod 0644 "$ROUTEGATE_GRAFANA_APT_KEY"
  gpg --batch --show-keys --with-colons "$ROUTEGATE_GRAFANA_APT_KEY" 2>/dev/null \
    | awk -F: '$1 == "fpr" {print $10}' \
    | grep -Fxq "$ROUTEGATE_GRAFANA_KEY_FINGERPRINT" \
    || die "The Grafana APT signing key fingerprint is unexpected."
  printf 'deb [signed-by=%s] https://apt.grafana.com stable main\n' "$ROUTEGATE_GRAFANA_APT_KEY" >"$ROUTEGATE_GRAFANA_APT_LIST"
  chmod 0644 "$ROUTEGATE_GRAFANA_APT_LIST"

  ln -s /dev/null "$ROUTEGATE_GRAFANA_INSTALL_MASK"
  systemctl daemon-reload
  apt-get update >/dev/null
  if ! apt-get install -y grafana >/dev/null; then
    rm -f "$ROUTEGATE_GRAFANA_INSTALL_MASK"
    systemctl daemon-reload >/dev/null 2>&1 || true
    die "Grafana package installation failed."
  fi
  rm -f "$ROUTEGATE_GRAFANA_INSTALL_MASK"
  systemctl daemon-reload
  package_installed grafana || die "Grafana package installation did not complete successfully."
}

write_grafana_config() {
  install -d -m 0755 /etc/grafana
  install -o root -g grafana -m 0640 /dev/null "$ROUTEGATE_GRAFANA_CONFIG"
  cat >"$ROUTEGATE_GRAFANA_CONFIG" <<EOF_GRAFANA
[server]
protocol = http
http_addr = 127.0.0.1
http_port = 3000
domain = ${ROUTEGATE_DOMAIN}
enforce_domain = true
root_url = ${ROUTEGATE_GRAFANA_URL}
serve_from_sub_path = true

[users]
allow_sign_up = false
allow_org_create = false

[auth.anonymous]
enabled = false
hide_version = true

[security]
admin_user = admin
cookie_secure = true
cookie_samesite = strict
allow_embedding = false

[analytics]
reporting_enabled = false
check_for_updates = false
check_for_plugin_updates = false

[dashboards]
min_refresh_interval = 30s
default_home_dashboard_path = ${ROUTEGATE_GRAFANA_DASHBOARD}
EOF_GRAFANA
  chown root:grafana "$ROUTEGATE_GRAFANA_CONFIG"
  chmod 0640 "$ROUTEGATE_GRAFANA_CONFIG"
}

write_grafana_provisioning() {
  install -d -o root -g grafana -m 0750 /etc/grafana/provisioning/datasources
  install -d -o root -g grafana -m 0750 /etc/grafana/provisioning/dashboards
  install -d -o root -g grafana -m 0750 "$ROUTEGATE_GRAFANA_DASHBOARD_DIR"

  install -o root -g grafana -m 0640 /dev/null "$ROUTEGATE_GRAFANA_DATASOURCE"
  cat >"$ROUTEGATE_GRAFANA_DATASOURCE" <<'EOF_DATASOURCE'
apiVersion: 1
prune: true

datasources:
  - name: RouteGate Prometheus
    uid: routegate-prometheus
    type: prometheus
    access: proxy
    url: http://127.0.0.1:9090
    isDefault: true
    editable: false
    jsonData:
      httpMethod: POST
      timeInterval: 30s
      prometheusType: Prometheus
EOF_DATASOURCE

  install -o root -g grafana -m 0640 /dev/null "$ROUTEGATE_GRAFANA_DASHBOARD_PROVIDER"
  cat >"$ROUTEGATE_GRAFANA_DASHBOARD_PROVIDER" <<EOF_PROVIDER
apiVersion: 1

providers:
  - name: RouteGate
    orgId: 1
    folder: RouteGate
    folderUid: routegate
    type: file
    disableDeletion: true
    updateIntervalSeconds: 30
    allowUiUpdates: false
    options:
      path: ${ROUTEGATE_GRAFANA_DASHBOARD_DIR}
EOF_PROVIDER

  write_routegate_dashboard "$ROUTEGATE_GRAFANA_DASHBOARD"
  chown root:grafana "$ROUTEGATE_GRAFANA_DATASOURCE" "$ROUTEGATE_GRAFANA_DASHBOARD_PROVIDER" "$ROUTEGATE_GRAFANA_DASHBOARD"
  chmod 0640 "$ROUTEGATE_GRAFANA_DATASOURCE" "$ROUTEGATE_GRAFANA_DASHBOARD_PROVIDER" "$ROUTEGATE_GRAFANA_DASHBOARD"
}

write_routegate_dashboard() {
  local path=$1
  cat >"$path" <<'EOF_DASHBOARD'
{
  "annotations": {"list": []},
  "editable": false,
  "graphTooltip": 1,
  "links": [],
  "panels": [
    {
      "datasource": {"type": "prometheus", "uid": "routegate-prometheus"},
      "fieldConfig": {"defaults": {"mappings": [], "thresholds": {"mode": "absolute", "steps": [{"color": "red", "value": null}, {"color": "green", "value": 1}]}}, "overrides": []},
      "gridPos": {"h": 4, "w": 6, "x": 0, "y": 0},
      "id": 1,
      "options": {"colorMode": "value", "graphMode": "none", "justifyMode": "auto", "orientation": "auto", "reduceOptions": {"calcs": ["lastNotNull"], "fields": "", "values": false}, "textMode": "auto", "wideLayout": true},
      "targets": [{"editorMode": "code", "expr": "routegate_manager_up", "instant": true, "legendFormat": "Manager", "range": false, "refId": "A"}],
      "title": "Manager",
      "type": "stat"
    },
    {
      "datasource": {"type": "prometheus", "uid": "routegate-prometheus"},
      "fieldConfig": {"defaults": {"mappings": [], "thresholds": {"mode": "absolute", "steps": [{"color": "red", "value": null}, {"color": "green", "value": 1}]}}, "overrides": []},
      "gridPos": {"h": 4, "w": 6, "x": 6, "y": 0},
      "id": 2,
      "options": {"colorMode": "value", "graphMode": "none", "justifyMode": "auto", "orientation": "auto", "reduceOptions": {"calcs": ["lastNotNull"], "fields": "", "values": false}, "textMode": "auto", "wideLayout": true},
      "targets": [{"editorMode": "code", "expr": "routegate_postgresql_up", "instant": true, "legendFormat": "PostgreSQL", "range": false, "refId": "A"}],
      "title": "PostgreSQL",
      "type": "stat"
    },
    {
      "datasource": {"type": "prometheus", "uid": "routegate-prometheus"},
      "fieldConfig": {"defaults": {"mappings": [], "thresholds": {"mode": "absolute", "steps": [{"color": "red", "value": null}, {"color": "green", "value": 1}]}}, "overrides": []},
      "gridPos": {"h": 4, "w": 6, "x": 12, "y": 0},
      "id": 3,
      "options": {"colorMode": "value", "graphMode": "none", "justifyMode": "auto", "orientation": "auto", "reduceOptions": {"calcs": ["lastNotNull"], "fields": "", "values": false}, "textMode": "auto", "wideLayout": true},
      "targets": [{"editorMode": "code", "expr": "sum(routegate_agent_up)", "instant": true, "legendFormat": "Agents", "range": false, "refId": "A"}],
      "title": "Agents online",
      "type": "stat"
    },
    {
      "datasource": {"type": "prometheus", "uid": "routegate-prometheus"},
      "fieldConfig": {"defaults": {"mappings": [], "thresholds": {"mode": "absolute", "steps": [{"color": "green", "value": null}, {"color": "red", "value": 1}]}}, "overrides": []},
      "gridPos": {"h": 4, "w": 6, "x": 18, "y": 0},
      "id": 4,
      "options": {"colorMode": "value", "graphMode": "none", "justifyMode": "auto", "orientation": "auto", "reduceOptions": {"calcs": ["lastNotNull"], "fields": "", "values": false}, "textMode": "auto", "wideLayout": true},
      "targets": [{"editorMode": "code", "expr": "sum(routegate_alerts_active)", "instant": true, "legendFormat": "Alerts", "range": false, "refId": "A"}],
      "title": "Active alerts",
      "type": "stat"
    },
    {
      "datasource": {"type": "prometheus", "uid": "routegate-prometheus"},
      "fieldConfig": {"defaults": {"unit": "percent"}, "overrides": []},
      "gridPos": {"h": 8, "w": 12, "x": 0, "y": 4},
      "id": 10,
      "options": {"legend": {"calcs": ["lastNotNull"], "displayMode": "table", "placement": "bottom", "showLegend": true}, "tooltip": {"mode": "multi", "sort": "desc"}},
      "targets": [{"editorMode": "code", "expr": "routegate_host_memory_usage_ratio{server_id=~\"$server_id\"} * 100", "legendFormat": "{{server_id}}", "range": true, "refId": "A"}],
      "title": "Memory usage",
      "type": "timeseries"
    },
    {
      "datasource": {"type": "prometheus", "uid": "routegate-prometheus"},
      "fieldConfig": {"defaults": {"unit": "percent"}, "overrides": []},
      "gridPos": {"h": 8, "w": 12, "x": 12, "y": 4},
      "id": 11,
      "options": {"legend": {"calcs": ["lastNotNull"], "displayMode": "table", "placement": "bottom", "showLegend": true}, "tooltip": {"mode": "multi", "sort": "desc"}},
      "targets": [{"editorMode": "code", "expr": "routegate_host_root_fs_usage_ratio{server_id=~\"$server_id\"} * 100", "legendFormat": "{{server_id}}", "range": true, "refId": "A"}],
      "title": "Root filesystem usage",
      "type": "timeseries"
    },
    {
      "datasource": {"type": "prometheus", "uid": "routegate-prometheus"},
      "fieldConfig": {"defaults": {}, "overrides": []},
      "gridPos": {"h": 8, "w": 12, "x": 0, "y": 12},
      "id": 12,
      "options": {"legend": {"calcs": ["lastNotNull"], "displayMode": "table", "placement": "bottom", "showLegend": true}, "tooltip": {"mode": "multi", "sort": "desc"}},
      "targets": [
        {"editorMode": "code", "expr": "routegate_host_load1{server_id=~\"$server_id\"}", "legendFormat": "{{server_id}} · 1m", "range": true, "refId": "A"},
        {"editorMode": "code", "expr": "routegate_host_load5{server_id=~\"$server_id\"}", "legendFormat": "{{server_id}} · 5m", "range": true, "refId": "B"},
        {"editorMode": "code", "expr": "routegate_host_load15{server_id=~\"$server_id\"}", "legendFormat": "{{server_id}} · 15m", "range": true, "refId": "C"}
      ],
      "title": "Host load",
      "type": "timeseries"
    },
    {
      "datasource": {"type": "prometheus", "uid": "routegate-prometheus"},
      "fieldConfig": {"defaults": {"unit": "s"}, "overrides": []},
      "gridPos": {"h": 8, "w": 12, "x": 12, "y": 12},
      "id": 13,
      "options": {"legend": {"calcs": ["lastNotNull"], "displayMode": "table", "placement": "bottom", "showLegend": true}, "tooltip": {"mode": "multi", "sort": "desc"}},
      "targets": [{"editorMode": "code", "expr": "routegate_agent_observation_age_seconds{server_id=~\"$server_id\"}", "legendFormat": "{{server_id}}", "range": true, "refId": "A"}],
      "title": "Agent observation age",
      "type": "timeseries"
    },
    {
      "datasource": {"type": "prometheus", "uid": "routegate-prometheus"},
      "fieldConfig": {"defaults": {"min": 0, "max": 1}, "overrides": []},
      "gridPos": {"h": 7, "w": 12, "x": 0, "y": 20},
      "id": 14,
      "options": {"legend": {"calcs": ["lastNotNull"], "displayMode": "table", "placement": "bottom", "showLegend": true}, "tooltip": {"mode": "multi", "sort": "desc"}},
      "targets": [{"editorMode": "code", "expr": "routegate_agent_up{server_id=~\"$server_id\"}", "legendFormat": "{{server_id}}", "range": true, "refId": "A"}],
      "title": "Agent availability",
      "type": "timeseries"
    },
    {
      "datasource": {"type": "prometheus", "uid": "routegate-prometheus"},
      "fieldConfig": {"defaults": {"min": 0, "max": 1}, "overrides": []},
      "gridPos": {"h": 7, "w": 12, "x": 12, "y": 20},
      "id": 15,
      "options": {"legend": {"calcs": ["lastNotNull"], "displayMode": "table", "placement": "bottom", "showLegend": true}, "tooltip": {"mode": "multi", "sort": "desc"}},
      "targets": [{"editorMode": "code", "expr": "routegate_vpn_core_up{server_id=~\"$server_id\"}", "legendFormat": "{{server_id}} · {{core}}", "range": true, "refId": "A"}],
      "title": "VPN Core availability",
      "type": "timeseries"
    }
  ],
  "refresh": "30s",
  "schemaVersion": 41,
  "tags": ["routegate", "managed"],
  "templating": {
    "list": [
      {
        "allValue": ".*",
        "current": {"selected": true, "text": "All", "value": "$__all"},
        "datasource": {"type": "prometheus", "uid": "routegate-prometheus"},
        "definition": "label_values(routegate_agent_up, server_id)",
        "includeAll": true,
        "label": "Server",
        "multi": true,
        "name": "server_id",
        "options": [],
        "query": {"query": "label_values(routegate_agent_up, server_id)", "refId": "PrometheusVariableQueryEditor-VariableQuery"},
        "refresh": 1,
        "regex": "",
        "sort": 1,
        "type": "query"
      }
    ]
  },
  "time": {"from": "now-6h", "to": "now"},
  "timepicker": {},
  "timezone": "browser",
  "title": "RouteGate Fleet Overview",
  "uid": "routegate-fleet-overview",
  "version": 1
}
EOF_DASHBOARD
  python3 -m json.tool "$path" >/dev/null || die "Generated RouteGate Grafana dashboard is invalid JSON."
}

write_bootstrap_override() {
  install -d -m 0755 "$(dirname "$ROUTEGATE_GRAFANA_BOOTSTRAP_OVERRIDE")"
  install -m 0600 /dev/null "$ROUTEGATE_GRAFANA_BOOTSTRAP_OVERRIDE"
  cat >"$ROUTEGATE_GRAFANA_BOOTSTRAP_OVERRIDE" <<EOF_OVERRIDE
[Service]
Environment="GF_SECURITY_ADMIN_USER=admin"
Environment="GF_SECURITY_ADMIN_PASSWORD=${ROUTEGATE_GRAFANA_ADMIN_PASSWORD}"
EOF_OVERRIDE
  chmod 0600 "$ROUTEGATE_GRAFANA_BOOTSTRAP_OVERRIDE"
}

start_grafana() {
  ROUTEGATE_GRAFANA_ADMIN_PASSWORD=$(openssl rand -base64 30 | tr -d '\n' | tr '/+' '_-')
  [[ "$ROUTEGATE_GRAFANA_ADMIN_PASSWORD" =~ ^[A-Za-z0-9_-]{30,}$ ]] \
    || die "Failed to generate the Grafana administrator password."

  write_bootstrap_override
  systemctl daemon-reload
  systemctl enable "$ROUTEGATE_GRAFANA_SERVICE" >/dev/null
  systemctl restart "$ROUTEGATE_GRAFANA_SERVICE"
  wait_for_http_200 "http://127.0.0.1:3000/grafana/api/health" 45 1 "$ROUTEGATE_DOMAIN" \
    || die "Grafana did not become healthy after first start."
  validate_loopback_listener 3000 Grafana

  rm -f "$ROUTEGATE_GRAFANA_BOOTSTRAP_OVERRIDE"
  rmdir /etc/systemd/system/grafana-server.service.d >/dev/null 2>&1 || true
  systemctl daemon-reload
  systemctl restart "$ROUTEGATE_GRAFANA_SERVICE"
  wait_for_http_200 "http://127.0.0.1:3000/grafana/api/health" 30 1 "$ROUTEGATE_DOMAIN" \
    || die "Grafana did not recover after removing the bootstrap secret from systemd."
}

write_nginx_grafana_proxy() {
  local path=$1
  python3 - "$path" <<'PY_NGINX'
from pathlib import Path
import sys

path = Path(sys.argv[1])
text = path.read_text()
if '# BEGIN ROUTEGATE MANAGED GRAFANA' in text:
    raise SystemExit(0)
api_marker = '    location /api/ {\n'
api_index = text.find(api_marker)
if api_index < 0:
    raise SystemExit('RouteGate nginx API proxy was not found; refusing to guess the server block.')
needle = '    location / {\n'
insert_index = text.find(needle, api_index)
if insert_index < 0:
    raise SystemExit('RouteGate nginx SPA location was not found after the API proxy.')
block = '''    # BEGIN ROUTEGATE MANAGED GRAFANA
    location = /grafana {
        return 301 /grafana/;
    }

    location /grafana/api/live/ {
        proxy_pass http://127.0.0.1:3000;
        proxy_http_version 1.1;
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;
        proxy_set_header Upgrade $http_upgrade;
        proxy_set_header Connection "upgrade";
        proxy_connect_timeout 5s;
        proxy_read_timeout 60s;
    }

    location /grafana/ {
        proxy_pass http://127.0.0.1:3000;
        proxy_http_version 1.1;
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;
        proxy_connect_timeout 5s;
        proxy_read_timeout 60s;
    }
    # END ROUTEGATE MANAGED GRAFANA

'''
path.write_text(text[:insert_index] + block + text[insert_index:])
PY_NGINX
}

verify_provisioning() {
  local auth="admin:${ROUTEGATE_GRAFANA_ADMIN_PASSWORD}"
  local_grafana_api "/api/datasources/uid/routegate-prometheus" -u "$auth" >/dev/null \
    || die "Grafana did not provision the RouteGate Prometheus datasource."
  local_grafana_api "/api/dashboards/uid/routegate-fleet-overview" -u "$auth" >/dev/null \
    || die "Grafana did not provision the RouteGate Fleet Overview dashboard."
}

configure_https_gateway() {
  write_nginx_grafana_proxy "$ROUTEGATE_NGINX_SITE"
  nginx -t >/dev/null
  systemctl reload nginx
  wait_for_http_200 "${ROUTEGATE_GRAFANA_URL}api/health" 30 1 \
    || die "Grafana is healthy locally but not reachable through RouteGate HTTPS."
}

write_access_credentials() {
  install -m 0600 /dev/null "$ROUTEGATE_GRAFANA_CREDENTIALS_FILE"
  cat >"$ROUTEGATE_GRAFANA_CREDENTIALS_FILE" <<EOF_ACCESS
RouteGate managed Grafana

URL: ${ROUTEGATE_GRAFANA_URL}
Username: admin
Initial password: ${ROUTEGATE_GRAFANA_ADMIN_PASSWORD}

Grafana is reachable only through the RouteGate HTTPS gateway. Its local service
listens on 127.0.0.1:3000 and Prometheus listens on 127.0.0.1:9090.

NEXT ACTION:
1. Open the URL above.
2. Sign in and change the Grafana administrator password.
3. Remove this root-only file after verifying the new password:
   sudo rm -f ${ROUTEGATE_GRAFANA_CREDENTIALS_FILE}
EOF_ACCESS
  chmod 0600 "$ROUTEGATE_GRAFANA_CREDENTIALS_FILE"
}

mark_managed_state() {
  python3 - "$ROUTEGATE_STATE_FILE" <<'PY_STATE'
from datetime import datetime, timezone
from pathlib import Path
import sys

path = Path(sys.argv[1])
lines = path.read_text().splitlines()
out = []
seen = False
for line in lines:
    if line.startswith('GRAFANA_MANAGED='):
        if not seen:
            out.append('GRAFANA_MANAGED=1')
            seen = True
    elif line.startswith('UPDATED_AT='):
        continue
    else:
        out.append(line)
if not seen:
    out.append('GRAFANA_MANAGED=1')
out.append('UPDATED_AT=' + datetime.now(timezone.utc).strftime('%Y-%m-%dT%H:%M:%SZ'))
path.write_text('\n'.join(out) + '\n')
PY_STATE
  chmod 0600 "$ROUTEGATE_STATE_FILE"
}

verify_final_state() {
  package_installed grafana || die "Final Grafana package verification failed."
  systemctl is-enabled --quiet "$ROUTEGATE_GRAFANA_SERVICE" || die "Grafana is not enabled."
  systemctl is-active --quiet "$ROUTEGATE_GRAFANA_SERVICE" || die "Grafana is not active."
  validate_loopback_listener 3000 Grafana
  validate_loopback_listener 9090 Prometheus
  wait_for_http_200 "http://127.0.0.1:3000/grafana/api/health" 10 1 "$ROUTEGATE_DOMAIN" \
    || die "Final Grafana local health check failed."
  wait_for_http_200 "${ROUTEGATE_GRAFANA_URL}api/health" 10 1 \
    || die "Final Grafana HTTPS gateway check failed."
  [[ "$(state_value GRAFANA_MANAGED || true)" == "1" ]] || die "Final Grafana ownership state was not persisted."
}

print_success() {
  cat <<EOF_SUCCESS

RouteGate managed Grafana installation completed successfully.

Grafana:
  ${ROUTEGATE_GRAFANA_URL}

Security boundary:
  Grafana:    127.0.0.1:3000
  Prometheus: 127.0.0.1:9090
  Remote access: RouteGate HTTPS /grafana/ only

Provisioned automatically:
  - RouteGate Prometheus datasource
  - RouteGate Fleet Overview dashboard
  - anonymous access disabled

NEXT ACTION — Sign in to Grafana and change the initial administrator password.
Root-only access details:
  ${ROUTEGATE_GRAFANA_CREDENTIALS_FILE}

Read them with:
  sudo cat ${ROUTEGATE_GRAFANA_CREDENTIALS_FILE}
EOF_SUCCESS
}

main() {
  require_root
  validate_platform
  load_routegate_identity
  verify_prometheus_dependency
  if verify_existing_managed_install; then
    return 0
  fi

  refuse_unowned_grafana
  prepare_backups
  ROUTEGATE_MUTATED=1
  trap rollback EXIT ERR

  install_grafana_package
  write_grafana_config
  write_grafana_provisioning
  start_grafana
  verify_provisioning
  configure_https_gateway
  write_access_credentials
  mark_managed_state
  verify_final_state

  ROUTEGATE_SUCCESS=1
  rm -rf "$ROUTEGATE_BACKUP_DIR"
  ROUTEGATE_BACKUP_DIR=""
  trap - EXIT ERR
  print_success
}

if [[ "${BASH_SOURCE[0]:-$0}" == "$0" ]]; then
  main "$@"
fi
