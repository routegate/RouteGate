#!/usr/bin/env bash

set -Eeuo pipefail
IFS=$'\n\t'

ROOT_DIR=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
# shellcheck source=install-grafana.sh
source "$ROOT_DIR/install-grafana.sh"

TESTS_RUN=0
TESTS_FAILED=0
TEST_TMP=$(mktemp -d /tmp/routegate-grafana-tests.XXXXXX)

cleanup_tests() {
  rm -rf "$TEST_TMP"
}
trap cleanup_tests EXIT

pass() {
  TESTS_RUN=$((TESTS_RUN + 1))
  printf 'ok %d - %s\n' "$TESTS_RUN" "$1"
}

fail() {
  TESTS_RUN=$((TESTS_RUN + 1))
  TESTS_FAILED=$((TESTS_FAILED + 1))
  printf 'not ok %d - %s\n' "$TESTS_RUN" "$1" >&2
}

assert_true() {
  local name=$1
  shift
  if "$@"; then
    pass "$name"
  else
    fail "$name"
  fi
}

assert_false() {
  local name=$1
  shift
  if "$@"; then
    fail "$name"
  else
    pass "$name"
  fi
}

assert_equal() {
  local name=$1 expected=$2 actual=$3
  if [[ "$expected" == "$actual" ]]; then
    pass "$name"
  else
    fail "$name (expected: $expected, actual: $actual)"
  fi
}

json_valid() {
  python3 -m json.tool "$1" >/dev/null
}

test_routegate_identity() {
  local state="$TEST_TMP/install-state.env"
  local env_file="$TEST_TMP/manager.env"
  local nginx_site="$TEST_TMP/routegate.conf"

  cat >"$state" <<'EOF_STATE'
STATUS=complete
DOMAIN=us.routegate.org
VERSION=v0.1.0
ARCH=amd64
PROMETHEUS_MANAGED=1
UPDATED_AT=2026-08-18T00:00:00Z
EOF_STATE
  cat >"$env_file" <<'EOF_ENV'
ROUTEGATE_PUBLIC_URL="https://us.routegate.org"
EOF_ENV
  printf 'server {\n    location / {\n    }\n}\n' >"$nginx_site"

  ROUTEGATE_STATE_FILE="$state"
  ROUTEGATE_MANAGER_ENV="$env_file"
  ROUTEGATE_NGINX_SITE="$nginx_site"
  ROUTEGATE_DOMAIN=""
  ROUTEGATE_GRAFANA_URL=""
  load_routegate_identity

  assert_equal "derives RouteGate domain from Manager public URL" "us.routegate.org" "$ROUTEGATE_DOMAIN"
  assert_equal "derives canonical Grafana subpath URL" "https://us.routegate.org/grafana/" "$ROUTEGATE_GRAFANA_URL"
}

test_state_update() {
  local state="$TEST_TMP/state-update.env"
  cat >"$state" <<'EOF_STATE'
STATUS=complete
DOMAIN=us.routegate.org
PROMETHEUS_MANAGED=1
UPDATED_AT=old
EOF_STATE
  ROUTEGATE_STATE_FILE="$state"
  mark_managed_state

  assert_true "marks Grafana as RouteGate-managed" grep -Fxq 'GRAFANA_MANAGED=1' "$state"
  assert_true "preserves Prometheus ownership state" grep -Fxq 'PROMETHEUS_MANAGED=1' "$state"
  assert_equal "stores one Grafana ownership field" "1" "$(grep -c '^GRAFANA_MANAGED=' "$state")"
  assert_equal "stores one state timestamp" "1" "$(grep -c '^UPDATED_AT=' "$state")"
}

test_nginx_proxy_generation() {
  local site="$TEST_TMP/nginx-routegate.conf"
  cat >"$site" <<'EOF_NGINX'
server {
    listen 443 ssl;
    server_name us.routegate.org;

    location /api/ {
        proxy_pass http://127.0.0.1:8080;
    }

    location / {
        try_files $uri $uri/ /index.html;
    }
}
EOF_NGINX

  write_nginx_grafana_proxy "$site"
  assert_true "adds Grafana HTTPS gateway marker" grep -Fq '# BEGIN ROUTEGATE MANAGED GRAFANA' "$site"
  assert_true "proxies Grafana only to loopback" grep -Fq 'proxy_pass http://127.0.0.1:3000;' "$site"
  assert_true "proxies Grafana Live websocket path" grep -Fq 'location /grafana/api/live/' "$site"
  # shellcheck disable=SC2016
  assert_true "keeps the RouteGate SPA fallback" grep -Fq 'try_files $uri $uri/ /index.html;' "$site"

  local first_hash second_hash
  first_hash=$(sha256sum "$site" | awk '{print $1}')
  write_nginx_grafana_proxy "$site"
  second_hash=$(sha256sum "$site" | awk '{print $1}')
  assert_equal "nginx proxy generation is idempotent" "$first_hash" "$second_hash"
}

test_dashboard_contract() {
  local dashboard="$TEST_TMP/routegate-fleet-overview.json"
  write_routegate_dashboard "$dashboard"
  assert_true "generated Fleet Overview is valid JSON" json_valid "$dashboard"
  assert_true "dashboard uses the managed Prometheus datasource" grep -Fq 'routegate-prometheus' "$dashboard"
  assert_true "dashboard includes memory history" grep -Fq 'routegate_host_memory_usage_ratio' "$dashboard"
  assert_true "dashboard includes disk history" grep -Fq 'routegate_host_root_fs_usage_ratio' "$dashboard"
  assert_true "dashboard includes Agent availability" grep -Fq 'routegate_agent_up' "$dashboard"
  assert_true "dashboard includes VPN Core availability" grep -Fq 'routegate_vpn_core_up' "$dashboard"
}

test_security_contract() {
  assert_true "Grafana service binds to loopback in managed config" grep -Fq 'http_addr = 127.0.0.1' "$ROOT_DIR/install-grafana.sh"
  # shellcheck disable=SC2016
  assert_true "Grafana is published only under the RouteGate subpath" grep -Fq 'root_url = ${ROUTEGATE_GRAFANA_URL}' "$ROOT_DIR/install-grafana.sh"
  assert_true "Grafana anonymous auth section is explicit" grep -Fq '[auth.anonymous]' "$ROOT_DIR/install-grafana.sh"
  assert_true "anonymous Grafana access is disabled" grep -Fq 'enabled = false' "$ROOT_DIR/install-grafana.sh"
  assert_true "Grafana session cookie is HTTPS-only" grep -Fq 'cookie_secure = true' "$ROOT_DIR/install-grafana.sh"
  assert_true "Grafana telemetry reporting is disabled" grep -Fq 'reporting_enabled = false' "$ROOT_DIR/install-grafana.sh"
  assert_true "Grafana datasource stays on local Prometheus" grep -Fq 'url: http://127.0.0.1:9090' "$ROOT_DIR/install-grafana.sh"
  assert_true "Grafana OSS comes from the official stable repository" grep -Fq 'https://apt.grafana.com stable main' "$ROOT_DIR/install-grafana.sh"
  assert_true "Grafana signing key fingerprint is pinned" grep -Fq 'B53AE77BADB630A683046005963FA27710458545' "$ROOT_DIR/install-grafana.sh"
  assert_false "managed Grafana never enables anonymous Viewer access" grep -Fq 'org_role = Viewer' "$ROOT_DIR/install-grafana.sh"
}

test_entrypoint_guard() {
  local guard
  guard=$(tail -n 3 "$ROOT_DIR/install-grafana.sh" | head -n1)
  # shellcheck disable=SC2016
  assert_equal "piped Grafana installer tolerates unset BASH_SOURCE" 'if [[ "${BASH_SOURCE[0]:-$0}" == "$0" ]]; then' "$guard"
}

printf 'TAP version 13\n'
test_routegate_identity
test_state_update
test_nginx_proxy_generation
test_dashboard_contract
test_security_contract
test_entrypoint_guard
printf '1..%d\n' "$TESTS_RUN"

if ((TESTS_FAILED > 0)); then
  printf '%d Grafana installer tests failed.\n' "$TESTS_FAILED" >&2
  exit 1
fi
