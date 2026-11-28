#!/usr/bin/env bash

set -Eeuo pipefail
IFS=$'\n\t'
umask 077

ROOT_DIR=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
WORK_DIR="${ROUTEGATE_E2E_WORK_DIR:-${RUNNER_TEMP:-/tmp}/routegate-remote-bootstrap-e2e}"
DATABASE_URL="${ROUTEGATE_E2E_DATABASE_URL:-}"
COMMIT="${ROUTEGATE_E2E_COMMIT:-$(git -C "$ROOT_DIR" rev-parse HEAD)}"
PUBLIC_URL="${ROUTEGATE_E2E_PUBLIC_URL:-https://localhost:18443}"
MANAGER_ADDR="${ROUTEGATE_E2E_MANAGER_ADDR:-127.0.0.1:18080}"
BOOTSTRAP_EMAIL="remote-bootstrap-e2e@example.invalid"
BOOTSTRAP_PASSWORD="RouteGate-Remote-Bootstrap-E2E-2026!"
SERVER_NAME="remote-bootstrap-e2e"
SERVER_IP="127.0.0.1"

MANAGER_PID=""
PROXY_PID=""

log() {
  printf '[remote-bootstrap-e2e] %s\n' "$*"
}

fail() {
  printf '[remote-bootstrap-e2e] ERROR: %s\n' "$*" >&2
  exit 1
}

cleanup() {
  set +e
  if [[ -n "$PROXY_PID" ]]; then
    kill "$PROXY_PID" >/dev/null 2>&1 || true
  fi
  if [[ -n "$MANAGER_PID" ]]; then
    kill "$MANAGER_PID" >/dev/null 2>&1 || true
  fi
  sudo systemctl stop routegate-agent.service >/dev/null 2>&1 || true
}

require_command() {
  command -v "$1" >/dev/null 2>&1 || fail "$1 is required"
}

wait_for_url() {
  local url=$1
  local attempts=${2:-60}
  local delay=${3:-1}
  local i
  for ((i=1; i<=attempts; i++)); do
    if curl -fsS --max-time 3 "$url" >/dev/null 2>&1; then
      return 0
    fi
    sleep "$delay"
  done
  return 1
}

api_json() {
  local method=$1
  local url=$2
  local body=${3:-}
  local token=${4:-}
  local args=(-fsS -X "$method" -H 'Content-Type: application/json')
  if [[ -n "$token" ]]; then
    args+=(-H "Authorization: Bearer $token")
  fi
  if [[ -n "$body" ]]; then
    args+=(--data "$body")
  fi
  curl "${args[@]}" "$url"
}

prepare_official_apt_sources() {
  log "Replacing runner-specific APT sources with the supported Ubuntu sources for the disposable E2E host."
  sudo rm -f /etc/apt/sources.list
  sudo find /etc/apt/sources.list.d -maxdepth 1 -type f -delete
  sudo tee /etc/apt/sources.list.d/ubuntu.sources >/dev/null <<'EOF_APT'
Types: deb
URIs: http://archive.ubuntu.com/ubuntu
Suites: noble noble-updates noble-backports
Components: main restricted universe multiverse
Signed-By: /usr/share/keyrings/ubuntu-archive-keyring.gpg

Types: deb
URIs: http://security.ubuntu.com/ubuntu
Suites: noble-security
Components: main restricted universe multiverse
Signed-By: /usr/share/keyrings/ubuntu-archive-keyring.gpg
EOF_APT
}

generate_local_tls() {
  local tls_dir="$WORK_DIR/tls"
  mkdir -p "$tls_dir"

  openssl req -x509 -newkey rsa:2048 -nodes -sha256 -days 1 \
    -subj '/CN=RouteGate E2E CA' \
    -keyout "$tls_dir/ca.key" \
    -out "$tls_dir/ca.crt" >/dev/null 2>&1

  openssl req -newkey rsa:2048 -nodes -sha256 \
    -subj '/CN=localhost' \
    -keyout "$tls_dir/server.key" \
    -out "$tls_dir/server.csr" >/dev/null 2>&1

  cat >"$tls_dir/server.ext" <<'EOF_EXT'
basicConstraints=CA:FALSE
keyUsage=digitalSignature,keyEncipherment
extendedKeyUsage=serverAuth
subjectAltName=DNS:localhost,IP:127.0.0.1
EOF_EXT

  openssl x509 -req -sha256 -days 1 \
    -in "$tls_dir/server.csr" \
    -CA "$tls_dir/ca.crt" \
    -CAkey "$tls_dir/ca.key" \
    -CAcreateserial \
    -extfile "$tls_dir/server.ext" \
    -out "$tls_dir/server.crt" >/dev/null 2>&1

  sudo install -m 0644 "$tls_dir/ca.crt" /usr/local/share/ca-certificates/routegate-e2e-ca.crt
  sudo update-ca-certificates >/dev/null
}

build_production_like_bundle() {
  local output_dir="$WORK_DIR/dist"
  local public_root="$WORK_DIR/public/bootstrap/$COMMIT"
  local build_date

  build_date=$(date -u +%Y-%m-%dT%H:%M:%SZ)
  log "Building exact-commit production-like amd64 bundle."
  OUTPUT_DIR="$output_dir" \
  VERSION=production-like \
  COMMIT="$COMMIT" \
  BUILD_DATE="$build_date" \
  AGENT_BUNDLE_BASE_URL="$PUBLIC_URL/bootstrap/$COMMIT" \
  ARCHITECTURES=amd64 \
    "$ROOT_DIR/scripts/build-release-bundle.sh"

  mkdir -p "$public_root" "$WORK_DIR/bundle"
  install -m 0644 "$output_dir/routegate-production-like-linux-amd64.tar.gz" "$public_root/"
  install -m 0644 "$output_dir/SHA256SUMS" "$public_root/"
  tar -xzf "$output_dir/routegate-production-like-linux-amd64.tar.gz" -C "$WORK_DIR/bundle"

  [[ -x "$WORK_DIR/bundle/bin/routegate-manager" ]] || fail "Manager binary is missing from the E2E bundle"
  [[ -x "$WORK_DIR/bundle/bin/routegate-agent" ]] || fail "Agent binary is missing from the E2E bundle"
}

verify_pinned_installer_is_public() {
  local downloaded="$WORK_DIR/pinned-install-agent.sh"
  curl -fsSL --proto '=https' --tlsv1.2 \
    "https://raw.githubusercontent.com/ikaevus/RouteGate/$COMMIT/install-agent.sh" \
    -o "$downloaded"
  cmp -s "$ROOT_DIR/install-agent.sh" "$downloaded" \
    || fail "The exact commit does not expose the same install-agent.sh bytes through raw.githubusercontent.com"
}

start_manager() {
  local manager_host=${MANAGER_ADDR%:*}
  local manager_port=${MANAGER_ADDR##*:}
  (
    cd "$WORK_DIR/bundle/manager"
    exec env \
      ROUTEGATE_ENV=dev \
      ROUTEGATE_HTTP_ADDR="$MANAGER_ADDR" \
      ROUTEGATE_DATABASE_URL="$DATABASE_URL" \
      ROUTEGATE_PUBLIC_URL="$PUBLIC_URL" \
      ROUTEGATE_GEOIP_ENABLED=false \
      ROUTEGATE_BOOTSTRAP_ADMIN_EMAIL="$BOOTSTRAP_EMAIL" \
      ROUTEGATE_BOOTSTRAP_ADMIN_USERNAME=remote-bootstrap-e2e \
      ROUTEGATE_BOOTSTRAP_ADMIN_PASSWORD="$BOOTSTRAP_PASSWORD" \
      "$WORK_DIR/bundle/bin/routegate-manager"
  ) >"$WORK_DIR/manager.log" 2>&1 &
  MANAGER_PID=$!

  wait_for_url "http://$manager_host:$manager_port/api/admin/health" 120 1 || {
    tail -n 100 "$WORK_DIR/manager.log" >&2 || true
    fail "Manager did not become healthy"
  }
}

start_https_proxy() {
  local listen_port=${PUBLIC_URL##*:}
  local manager_host=${MANAGER_ADDR%:*}
  local manager_port=${MANAGER_ADDR##*:}

  python3 "$ROOT_DIR/scripts/remote-node-bootstrap-e2e-proxy.py" \
    --listen-host 127.0.0.1 \
    --listen-port "$listen_port" \
    --manager-host "$manager_host" \
    --manager-port "$manager_port" \
    --static-root "$WORK_DIR/public" \
    --cert "$WORK_DIR/tls/server.crt" \
    --key "$WORK_DIR/tls/server.key" \
    >"$WORK_DIR/proxy.log" 2>&1 &
  PROXY_PID=$!

  wait_for_url "$PUBLIC_URL/api/admin/health" 60 1 || {
    tail -n 100 "$WORK_DIR/proxy.log" >&2 || true
    fail "HTTPS bootstrap proxy did not become healthy"
  }

  local checksums
  checksums=$(curl -fsS "$PUBLIC_URL/bootstrap/$COMMIT/SHA256SUMS")
  grep -Fq 'routegate-production-like-linux-amd64.tar.gz' <<<"$checksums" \
    || fail "Public bootstrap SHA256SUMS does not contain the amd64 production-like bundle"
}

create_bootstrap_command() {
  local manager_http="http://$MANAGER_ADDR"
  local login_response admin_token server_response server_id

  login_response=$(api_json POST "$manager_http/api/admin/auth/login" \
    "$(jq -nc --arg email "$BOOTSTRAP_EMAIL" --arg password "$BOOTSTRAP_PASSWORD" '{email:$email,password:$password}')")
  admin_token=$(jq -er '.token' <<<"$login_response") || fail "Admin login did not return a token"

  server_response=$(api_json POST "$manager_http/api/v1/servers" \
    "$(jq -nc --arg name "$SERVER_NAME" --arg ip "$SERVER_IP" '{name:$name,publicIp:$ip,deploymentRole:"vpn"}')" \
    "$admin_token")
  server_id=$(jq -er '.id' <<<"$server_response") || fail "Server creation did not return an id"

  # Keep credentials out of stdout and the workflow log.
  printf '%s' "$admin_token" >"$WORK_DIR/admin.token"
  printf '%s' "$server_id" >"$WORK_DIR/server.id"
  chmod 0600 "$WORK_DIR/admin.token" "$WORK_DIR/server.id"
  issue_bootstrap_command
}

issue_bootstrap_command() {
  local manager_http="http://$MANAGER_ADDR"
  local admin_token server_id token_response bootstrap_command
  admin_token=$(cat "$WORK_DIR/admin.token")
  server_id=$(cat "$WORK_DIR/server.id")

  token_response=$(api_json POST "$manager_http/api/v1/servers/$server_id/registration-token" '' "$admin_token")
  bootstrap_command=$(jq -er '.bootstrapCommand | select(length > 0)' <<<"$token_response") \
    || fail "Manager did not return a privileged bootstrap command"

  grep -Fq "raw.githubusercontent.com/ikaevus/RouteGate/$COMMIT/install-agent.sh" <<<"$bootstrap_command" \
    || fail "Bootstrap command is not pinned to the exact test commit"
  grep -Fq "ROUTEGATE_BUNDLE_BASE_URL='$PUBLIC_URL/bootstrap/$COMMIT'" <<<"$bootstrap_command" \
    || fail "Bootstrap command is not pinned to the exact Manager-hosted bundle source"

  printf '%s' "$bootstrap_command" >"$WORK_DIR/bootstrap.command"
  chmod 0600 "$WORK_DIR/bootstrap.command"
}

verify_retry_before_registration() {
  local checksums="$WORK_DIR/public/bootstrap/$COMMIT/SHA256SUMS"
  local original="$WORK_DIR/original-SHA256SUMS"
  cp "$checksums" "$original"
  printf '%064d  routegate-production-like-linux-amd64.tar.gz\n' 0 >"$checksums"

  log "Testing checksum failure before Agent installation."
  if bash -lc "$(cat "$WORK_DIR/bootstrap.command")" >"$WORK_DIR/failed-bootstrap.log" 2>&1; then
    fail "Bootstrap accepted a bundle whose checksum did not match"
  fi
  sudo grep -Fxq 'STATUS=failed' /var/lib/routegate-agent-installer/state.env \
    || fail "Installer did not record the failed status"
  sudo grep -Fxq 'STAGE=bundle' /var/lib/routegate-agent-installer/state.env \
    || fail "Installer did not identify the failed bundle stage"
  [[ ! -e /usr/local/bin/routegate-agent && ! -e /etc/routegate/agent.yaml ]] \
    || fail "Failed bundle verification partially installed Agent"
  if grep -Eq 'rg_(reg|agent)_[A-Za-z0-9_-]{43}' "$WORK_DIR/failed-bootstrap.log"; then
    fail "Installer failure output contained a registration or Agent token"
  fi

  cp "$original" "$checksums"
  issue_bootstrap_command
  log "Checksum failure was recorded without installing Agent; retrying with a fresh Manager command."
}

run_generated_bootstrap() {
  local bootstrap_command
  bootstrap_command=$(cat "$WORK_DIR/bootstrap.command")

  log "Executing the exact Manager-generated bootstrap command without printing its registration token."
  bash -lc "$bootstrap_command"

  sudo systemctl is-active --quiet routegate-agent.service \
    || fail "routegate-agent.service is not active after bootstrap"
  sudo grep -Eq '^agent_token: "rg_agent_[A-Za-z0-9_-]{43}"$' /etc/routegate/agent.yaml \
    || fail "Agent config does not contain a persistent Agent credential"
  if sudo grep -Fq 'registration_token:' /etc/routegate/agent.yaml; then
    fail "Consumed registration token remained in Agent config"
  fi

  sudo grep -Fxq 'STATUS=complete' /var/lib/routegate-agent-installer/state.env \
    || fail "Remote installer state is not complete"
  sudo grep -Fxq 'STAGE=complete' /var/lib/routegate-agent-installer/state.env \
    || fail "Remote installer stage is not complete"

  [[ ! -e /usr/local/bin/hysteria ]] \
    || fail "Remote node bootstrap unexpectedly installed Hysteria"
  [[ ! -e /usr/local/bin/mtg ]] \
    || fail "Remote node bootstrap unexpectedly installed MTProto runtime"
  [[ ! -e /usr/local/bin/sing-box && ! -e /etc/systemd/system/sing-box.service ]] \
    || fail "Remote node bootstrap unexpectedly installed sing-box"
  [[ ! -e /etc/wireguard/routegate-wg0.conf ]] \
    || fail "Remote node bootstrap unexpectedly configured WireGuard"
  [[ ! -e /etc/sysctl.d/99-routegate-wireguard.conf ]] \
    || fail "Remote node bootstrap unexpectedly mutated WireGuard forwarding state"
}

verify_manager_observed_agent() {
  local manager_http="http://$MANAGER_ADDR"
  local admin_token server_id response
  admin_token=$(cat "$WORK_DIR/admin.token")
  server_id=$(cat "$WORK_DIR/server.id")

  local i
  for ((i=1; i<=30; i++)); do
    response=$(api_json GET "$manager_http/api/v1/servers/$server_id" '' "$admin_token")
    if jq -e '.agent.id and .agent.lastSeenAt and (.agent.status == "online" or .agent.status == "registered")' <<<"$response" >/dev/null; then
      log "Manager observed registered Agent and heartbeat."
      return 0
    fi
    sleep 1
  done
  fail "Manager never observed a registered Agent heartbeat"
}

main() {
  # This script replaces APT sources and installs system services. Never run it
  # on a workstation, self-hosted runner, or any real RouteGate node.
  [[ "${GITHUB_ACTIONS:-}" == true && "${RUNNER_ENVIRONMENT:-}" == github-hosted && "${ROUTEGATE_E2E_ISOLATED:-}" == 1 ]] \
    || fail "Only an explicitly isolated GitHub-hosted runner is supported"
  [[ -n "${RUNNER_TEMP:-}" && "$RUNNER_TEMP" != / && "$WORK_DIR" == "$RUNNER_TEMP/routegate-remote-bootstrap-e2e" ]] \
    || fail "Work directory must be the dedicated runner temporary directory"
  [[ -n "$DATABASE_URL" ]] || fail "ROUTEGATE_E2E_DATABASE_URL is required"
  [[ "$COMMIT" =~ ^[a-f0-9]{40}$ ]] || fail "ROUTEGATE_E2E_COMMIT must be a full Git SHA"
  [[ "$PUBLIC_URL" =~ ^https://localhost:[0-9]+$ ]] || fail "ROUTEGATE_E2E_PUBLIC_URL must use https://localhost:<port>"
  [[ "$MANAGER_ADDR" =~ ^127\.0\.0\.1:[0-9]+$ ]] || fail "Manager must be local to the isolated runner"

  for command_name in curl git go jq npm openssl psql python3 sha256sum sudo tar; do
    require_command "$command_name"
  done
  # Check both the destination and the actual database before changing the host.
  python3 - "$DATABASE_URL" <<'PY_DATABASE'
import sys
from urllib.parse import urlsplit
try:
    url = urlsplit(sys.argv[1])
    safe = (url.scheme in ('postgres', 'postgresql') and url.hostname == '127.0.0.1'
            and url.path == '/routegate_remote_bootstrap_e2e'
            and url.query == 'sslmode=disable' and not url.fragment)
except ValueError:
    safe = False
if not safe:
    sys.exit('Only the dedicated loopback E2E database is supported')
PY_DATABASE
  local actual_database
  actual_database=$(PGOPTIONS='-c default_transaction_read_only=on -c statement_timeout=5000' \
    psql "$DATABASE_URL" -XAt -v ON_ERROR_STOP=1 -c 'SELECT current_database()' 2>/dev/null) \
    || fail "Cannot verify isolated database"
  [[ "$actual_database" == routegate_remote_bootstrap_e2e ]] || fail "Actual database is not the isolated E2E database"

  [[ ! -e "$WORK_DIR" ]] || fail "Dedicated work directory must not already exist"
  mkdir -p "$WORK_DIR"
  trap cleanup EXIT

  build_production_like_bundle
  verify_pinned_installer_is_public
  generate_local_tls
  start_manager
  start_https_proxy
  create_bootstrap_command
  prepare_official_apt_sources
  verify_retry_before_registration
  run_generated_bootstrap
  verify_manager_observed_agent
  # Independent diagnostic client; never replace the installed server runtime.
  curl --fail --silent --show-error --location --retry 2 --max-time 60 \
    https://github.com/SagerNet/sing-box/releases/download/v1.12.8/sing-box-1.12.8-linux-amd64.tar.gz \
    --output "$WORK_DIR/reference-client.tar.gz"
  printf '%s  %s\n' b41d9ae438a4367e5fd5c4aa36fe040d0c1bfe5f91f96a072eef31f61d46670c "$WORK_DIR/reference-client.tar.gz" | sha256sum --check --status
  tar -xOf "$WORK_DIR/reference-client.tar.gz" sing-box-1.12.8-linux-amd64/sing-box > "$WORK_DIR/reference-client"
  chmod 0700 "$WORK_DIR/reference-client"
  sudo --preserve-env=GITHUB_ACTIONS,RUNNER_ENVIRONMENT,ROUTEGATE_E2E_ISOLATED \
    python3 "$ROOT_DIR/scripts/remote-node-vpn-e2e.py" \
    --work-dir "$WORK_DIR" --manager "http://$MANAGER_ADDR" \
    --reference-client "$WORK_DIR/reference-client"

  log "Remote VPN Node bootstrap E2E passed."
}

main "$@"
