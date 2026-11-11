#!/usr/bin/env bash
# GitHub-hosted, disposable Ubuntu 24.04 ONLY. Executes the real install.sh.
# A local trusted test CA replaces external ACME via a process-local certbot
# shim. This never tests public Let's Encrypt or a real external VPN client.
set -Eeuo pipefail
IFS=$'\n\t'
umask 077

ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
[[ "${GITHUB_ACTIONS:-}" == true ]] || { echo "GitHub Actions required" >&2; exit 2; }
[[ "${RUNNER_TEMP:-}" == /home/runner/work/_temp* ]] || {
  echo "Refusing to touch a non-disposable host" >&2; exit 2;
}
[[ $(id -u) != 0 && $(lsb_release -rs) == 24.04 ]] || {
  echo "Run as unprivileged user on Ubuntu 24.04" >&2; exit 2;
}
DOMAIN=routegate-hybrid-ci.invalid
WORK="$RUNNER_TEMP/routegate-hybrid-clean-host-e2e"
VERSION=ci-hybrid-e2e
[[ "${ROUTEGATE_E2E_GRAFANA:-0}" == "0" || "${ROUTEGATE_E2E_GRAFANA:-0}" == "1" ]] ||
  { echo "Invalid Grafana E2E mode" >&2; exit 2; }
MONITORING_FLAGS=(--without-prometheus)
if [[ "${ROUTEGATE_E2E_GRAFANA:-0}" == "1" ]]; then
  MONITORING_FLAGS=(--with-grafana)
fi
COMMIT=$(git -C "$ROOT" rev-parse HEAD)
export ROUTEGATE_E2E_DOMAIN="$DOMAIN" ROUTEGATE_E2E_WORK_DIR="$WORK"
mkdir -m 0700 "$WORK" "$WORK/bin"
: >"$WORK/install.stdout"
chmod 0600 "$WORK/install.stdout"
log() { printf '[hybrid-e2e] %s\n' "$*"; }
die() { printf '[hybrid-e2e] ERROR: %s\n' "$*" >&2; exit 1; }

# A fresh runner is mandatory; never take over an existing system.
[[ ! -e /etc/routegate/install-state.env &&
   ! -e /usr/local/bin/routegate-manager &&
   ! -e /etc/nginx/sites-available/routegate ]] ||
  die "Runner already has RouteGate state"
for svc in postgresql nginx apache2; do
  sudo systemctl is-active --quiet "$svc" && die "Existing $svc service" || true
done
if sudo ss -ltnH | awk '{split($4,a,":"); p=a[length(a)]; if (p=="80" || p=="443") found=1} END {exit !found}'; then
  die "TCP 80/443 is already owned"
fi

log "Preparing official Ubuntu repositories on this one-use runner."
sudo rm -f /etc/apt/sources.list
sudo find /etc/apt/sources.list.d -maxdepth 1 -type f -delete
sudo tee /etc/apt/sources.list.d/ubuntu.sources >/dev/null <<'APT'
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
APT

HOST_IP=$(ip -4 route get 1.1.1.1 | awk '{for(i=1;i<=NF;i++) if($i=="src") {print $(i+1); exit}}')
[[ "$HOST_IP" =~ ^[0-9]+(\.[0-9]+){3}$ ]] || die "Missing test host IP"
printf '%s %s\n' "$HOST_IP" "$DOMAIN" | sudo tee -a /etc/hosts >/dev/null
export NO_PROXY="$DOMAIN,localhost,127.0.0.1" no_proxy="$DOMAIN,localhost,127.0.0.1"

# The shim is NOT installed globally or shipped with RouteGate. It only runs
# inside the isolated GitHub installer process with an exact invalid DNS name.
cat >"$WORK/bin/certbot" <<'CERTBOT'
#!/usr/bin/env bash
set -Eeuo pipefail
umask 077
[[ "${GITHUB_ACTIONS:-}" == true &&
   "${ROUTEGATE_E2E_DOMAIN:-}" == routegate-hybrid-ci.invalid &&
   "${ROUTEGATE_E2E_WORK_DIR:-}" == /home/runner/work/_temp/routegate-hybrid-clean-host-e2e ]] || exit 70
[[ " $* " == *" --nginx "* && " $* " == *" --redirect "* &&
   " $* " == *" -d routegate-hybrid-ci.invalid "* ]] || exit 71
# Inject one safe, deterministic ACME failure after PostgreSQL, Manager,
# nginx and RouteGate-owned files have already been installed. This shim exists
# ONLY inside this one-use GitHub runner's process-local PATH.
if [[ -f "$ROUTEGATE_E2E_WORK_DIR/fail-certbot-once" ]]; then
  rm -f "$ROUTEGATE_E2E_WORK_DIR/fail-certbot-once"
  exit 75
fi
TLS="$ROUTEGATE_E2E_WORK_DIR/tls"
mkdir -p "$TLS"
openssl req -x509 -newkey rsa:2048 -nodes -sha256 -days 1 \
  -subj '/CN=RouteGate disposable CI CA' \
  -keyout "$TLS/ca.key" -out "$TLS/ca.crt" >/dev/null 2>&1
openssl req -newkey rsa:2048 -nodes -sha256 \
  -subj '/CN=routegate-hybrid-ci.invalid' \
  -keyout "$TLS/key.pem" -out "$TLS/request.csr" >/dev/null 2>&1
printf 'basicConstraints=CA:FALSE\nkeyUsage=digitalSignature,keyEncipherment\nextendedKeyUsage=serverAuth\nsubjectAltName=DNS:routegate-hybrid-ci.invalid\n' >"$TLS/ext"
openssl x509 -req -sha256 -days 1 \
  -in "$TLS/request.csr" -CA "$TLS/ca.crt" -CAkey "$TLS/ca.key" \
  -CAcreateserial -extfile "$TLS/ext" -out "$TLS/cert.pem" >/dev/null 2>&1
install -m 0644 "$TLS/ca.crt" /usr/local/share/ca-certificates/routegate-disposable-ci.crt
update-ca-certificates >/dev/null
install -d -m 0700 /etc/letsencrypt/live/routegate-hybrid-ci.invalid
install -m 0644 "$TLS/cert.pem" /etc/letsencrypt/live/routegate-hybrid-ci.invalid/fullchain.pem
install -m 0600 "$TLS/key.pem" /etc/letsencrypt/live/routegate-hybrid-ci.invalid/privkey.pem
python3 - <<'PY'
from pathlib import Path
p = Path('/etc/nginx/sites-available/routegate')
src = p.read_text()
mark = '\n# Hybrid Hysteria2'
assert src.count(mark) == 1
http, rest = src.split(mark, 1)
assert http.count('server {') == 1
domain = 'routegate-hybrid-ci.invalid'
assert f'server_name {domain};' in http
tls = http.replace('listen 80;', 'listen 443 ssl;', 1).replace('listen [::]:80;', 'listen [::]:443 ssl;', 1)
tls = tls.replace(f'server_name {domain};', f'server_name {domain};\n    ssl_certificate /etc/letsencrypt/live/{domain}/fullchain.pem;\n    ssl_certificate_key /etc/letsencrypt/live/{domain}/privkey.pem;', 1)
p.write_text(http + '\n' + tls + mark + rest)
PY
nginx -t >/dev/null 2>&1
systemctl reload nginx
CERTBOT
chmod 0700 "$WORK/bin/certbot"

if [[ "${ROUTEGATE_E2E_GRAFANA:-0}" == "1" ]]; then
  # Process-local fault injection, strictly limited to the disposable runner.
  cat >"$WORK/bin/apt-get" <<'APT_SHIM'
#!/usr/bin/env bash
set -Eeuo pipefail
if [[ "${GITHUB_ACTIONS:-}" == true &&
      "${ROUTEGATE_E2E_DOMAIN:-}" == routegate-hybrid-ci.invalid &&
      "${ROUTEGATE_E2E_WORK_DIR:-}" == /home/runner/work/_temp/routegate-hybrid-clean-host-e2e &&
      -f "$ROUTEGATE_E2E_WORK_DIR/fail-grafana-once" ]]; then
  for arg in "$@"; do
    if [[ "$arg" == grafana ]]; then
      rm -f "$ROUTEGATE_E2E_WORK_DIR/fail-grafana-once"
      printf '[hybrid-e2e] synthetic Grafana APT failure (once)\n' >&2
      exit 79
    fi
  done
fi
exec /usr/bin/apt-get "$@"
APT_SHIM
  chmod 0700 "$WORK/bin/apt-get"
fi

log "Building native exact-commit Manager + Agent + frontend bundle."
if ! OUTPUT_DIR="$WORK/dist" VERSION="$VERSION" COMMIT="$COMMIT" ARCHITECTURES=amd64 \
  "$ROOT/scripts/build-release-bundle.sh" >"$WORK/build.log" 2>&1; then
  die "Native bundle build failed (private runner log)"
fi
BUNDLE="$WORK/dist/routegate-$VERSION-linux-amd64.tar.gz"
CHECKSUM="$WORK/dist/SHA256SUMS"
[[ -s "$BUNDLE" && -s "$CHECKSUM" ]] || die "Release bundle/checksum missing"

log "Proving bad checksum rejects BEFORE package, service, or RouteGate state changes."
# Negative installer test on the very same clean runner: intentionally tamper
# ONLY the checksum file, not the actual candidate release bundle.
printf '%064d  %s\n' 0 "$(basename "$BUNDLE")" >"$WORK/bad-checksums"
if sudo -n env \
  "PATH=$WORK/bin:$PATH" "GITHUB_ACTIONS=true" "RUNNER_TEMP=$RUNNER_TEMP" \
  "ROUTEGATE_E2E_DOMAIN=$DOMAIN" "ROUTEGATE_E2E_WORK_DIR=$WORK" \
  "NO_PROXY=$NO_PROXY" "no_proxy=$no_proxy" \
  bash "$ROOT/install.sh" \
    --domain "$DOMAIN" --email routegate-ci@example.invalid \
    --admin-email routegate-ci@example.invalid --server-name "$DOMAIN" \
    --version "$VERSION" --bundle-file "$BUNDLE" --checksum-file "$WORK/bad-checksums" \
    --without-prometheus --yes >"$WORK/rejected-checksum.stdout" 2>&1; then
  die "The real installer accepted a release with a tampered checksum"
fi
grep -Fq 'ERROR at stage bundle: Release bundle checksum verification failed.' \
  "$WORK/rejected-checksum.stdout" ||
  die "Corrupt bundle was not rejected at the pre-mutation bundle stage"
[[ ! -e /etc/routegate/install-state.env &&
   ! -e /usr/local/bin/routegate-manager &&
   ! -e /usr/local/bin/routegate-agent ]] ||
  die "Failed checksum attempt partially installed RouteGate or created install state"
for svc in postgresql nginx routegate-manager routegate-agent; do
  if sudo systemctl is-active --quiet "$svc"; then
    die "Failed checksum attempt unexpectedly activated $svc"
  fi
done
log "Checksum failure was safe; retrying with original verified bundle and checksum."

log "Injecting one post-mutation TLS failure to exercise actual interrupted-install recovery."
touch "$WORK/fail-certbot-once"
if sudo -n env \
  "PATH=$WORK/bin:$PATH" "GITHUB_ACTIONS=true" "RUNNER_TEMP=$RUNNER_TEMP" \
  "ROUTEGATE_E2E_DOMAIN=$DOMAIN" "ROUTEGATE_E2E_WORK_DIR=$WORK" \
  "NO_PROXY=$NO_PROXY" "no_proxy=$no_proxy" \
  bash "$ROOT/install.sh" \
    --domain "$DOMAIN" --email routegate-ci@example.invalid \
    --admin-email routegate-ci@example.invalid --server-name "$DOMAIN" \
    --version "$VERSION" --bundle-file "$BUNDLE" --checksum-file "$CHECKSUM" \
    "${MONITORING_FLAGS[@]}" --yes >"$WORK/rejected-acme.stdout" 2>&1; then
  die "TLS failure injection was ignored by the native installer"
fi
grep -Fq 'Installation stopped at stage nginx_tls' "$WORK/rejected-acme.stdout" ||
  die "Interrupted install did not stop at the expected post-mutation TLS stage"
sudo grep -Fxq 'STATUS=installing' /etc/routegate/install-state.env ||
  die "Failed post-mutation install lost its resumable ownership state"
[[ ! -e "$WORK/fail-certbot-once" ]] ||
  die "One-time synthetic ACME failure was never exercised"
sudo test -s /var/lib/routegate-installer/secrets.env ||
  die "Post-mutation failure did not preserve necessary recovery credentials"
sudo systemctl is-active --quiet postgresql || die "PostgreSQL not yet running after TLS failure"
sudo systemctl is-active --quiet routegate-manager || die "Manager not yet running after TLS failure"
sudo systemctl is-active --quiet nginx || die "nginx not yet running after TLS failure"
PRESERVED_ADMIN_SHA=$(sudo sed -n 's/^ADMIN_PASSWORD=//p' /var/lib/routegate-installer/secrets.env |
  sha256sum | awk '{print $1}')
[[ "$PRESERVED_ADMIN_SHA" =~ ^[a-f0-9]{64}$ ]] ||
  die "Failed to fingerprint preserved bootstrap credential"
log "Interrupted state and secrets preserved; retrying the exact same verified release."

log "Installing all-in-one Hybrid on disposable Ubuntu with test TLS."

if [[ "${ROUTEGATE_E2E_GRAFANA:-0}" == "1" ]]; then
  log "Injecting Grafana install failure AFTER core completion."
  touch "$WORK/fail-grafana-once"
  if sudo -n env \
    "PATH=$WORK/bin:$PATH" "GITHUB_ACTIONS=true" "RUNNER_TEMP=$RUNNER_TEMP" \
    "ROUTEGATE_E2E_DOMAIN=$DOMAIN" "ROUTEGATE_E2E_WORK_DIR=$WORK" \
    "NO_PROXY=$NO_PROXY" "no_proxy=$no_proxy" \
    bash "$ROOT/install.sh" \
      --domain "$DOMAIN" --email routegate-ci@example.invalid \
      --admin-email routegate-ci@example.invalid --server-name "$DOMAIN" \
      --version "$VERSION" --bundle-file "$BUNDLE" --checksum-file "$CHECKSUM" \
      --with-grafana --yes >"$WORK/rejected-grafana.stdout" 2>&1; then
    die "Synthetic Grafana failure was ignored"
  fi
  [[ ! -e "$WORK/fail-grafana-once" ]] ||
    die "Synthetic Grafana package failure was not exercised"
  grep -Fq 'Core RouteGate remains complete, but Grafana failed.' "$WORK/rejected-grafana.stdout" ||
    die "Grafana failure did not report core survived"
  if grep -Fq 'RouteGate installation completed successfully.' "$WORK/rejected-grafana.stdout"; then
    die "Install falsely reported total success before Grafana"
  fi
  sudo grep -Fxq 'STATUS=complete' /etc/routegate/install-state.env ||
    die "Grafana failure damaged core ownership"
  sudo grep -Fxq 'GRAFANA_REQUESTED=1' /etc/routegate/install-state.env ||
    die "Grafana failure lost original intent"
  if sudo grep -Fxq 'GRAFANA_MANAGED=1' /etc/routegate/install-state.env; then
    die "Grafana failure falsely recorded success"
  fi
  for svc in postgresql nginx routegate-manager routegate-agent prometheus; do
    sudo systemctl is-active --quiet "$svc" || die "Core service $svc died after Grafana failure"
  done
  curl -fsS --max-time 10 "https://$DOMAIN/api/admin/health" >/dev/null ||
    die "Core HTTPS health failed after Grafana rollback"
  log "Core preserved after failed Grafana; retrying with the same verified release."
fi
if ! sudo -n env \
  "PATH=$WORK/bin:$PATH" "GITHUB_ACTIONS=true" "RUNNER_TEMP=$RUNNER_TEMP" \
  "ROUTEGATE_E2E_DOMAIN=$DOMAIN" "ROUTEGATE_E2E_WORK_DIR=$WORK" \
  "NO_PROXY=$NO_PROXY" "no_proxy=$no_proxy" \
  bash "$ROOT/install.sh" \
    --domain "$DOMAIN" --email routegate-ci@example.invalid \
    --admin-email routegate-ci@example.invalid --server-name "$DOMAIN" \
    --version "$VERSION" --bundle-file "$BUNDLE" --checksum-file "$CHECKSUM" \
    "${MONITORING_FLAGS[@]}" --yes >"$WORK/install.stdout" 2>&1; then
  # Installer stdout can contain a first-login bearer token: never display it.
  sudo grep -E 'ERROR at stage|stage=.*ERROR' /var/log/routegate-installer.log |
    sed -E 's@https?://[^[:space:]]+@[redacted-url]@g' >&2 || true
  die "Hybrid install failed; private diagnostics remain on disposable runner"
fi

log "Checking real systemd services, persistent Agent and HTTPS frontend."
sudo grep -Fxq 'STATUS=complete' /etc/routegate/install-state.env || die "Install not complete"
sudo grep -Fxq "VERSION=$VERSION" /etc/routegate/install-state.env || die "Wrong version"
[[ "$PRESERVED_ADMIN_SHA" == "$(sudo sed -n 's/^Bootstrap password: //p' /root/routegate-first-login.txt | sha256sum | awk '{print $1}')" ]] ||
  die "Resume unexpectedly rotated the bootstrap password instead of preserving it"
sudo grep -q '^agent_id:' /etc/routegate/agent.yaml || die "Agent not registered"
sudo grep -q '^agent_token:' /etc/routegate/agent.yaml || die "Agent has no persistent token"
if sudo grep -q '^registration_token:' /etc/routegate/agent.yaml; then
  die "One-use registration token still present"
fi
for svc in postgresql nginx routegate-manager routegate-agent certbot.timer; do
  sudo systemctl is-active --quiet "$svc" || die "$svc inactive"
done
sudo grep -Fq 'data-theme="dark"' /var/www/routegate/index.html || die "Stale frontend"
curl -fsS --max-time 10 "https://$DOMAIN/api/admin/health" >/dev/null || die "HTTPS health failed"
curl -fsS --max-time 10 "https://$DOMAIN/" >/dev/null || die "HTTPS frontend failed"

if [[ "${ROUTEGATE_E2E_GRAFANA:-0}" == "1" ]]; then
  log "Checking real Grafana package, Prometheus, HTTPS proxy and dashboard provisioning."
  sudo grep -Fxq 'PROMETHEUS_MANAGED=1' /etc/routegate/install-state.env ||
    die "Requested Prometheus ownership not persisted"
  sudo grep -Fxq 'GRAFANA_REQUESTED=1' /etc/routegate/install-state.env ||
    die "Grafana selection not persisted"
  sudo grep -Fxq 'GRAFANA_MANAGED=1' /etc/routegate/install-state.env ||
    die "Grafana installation did not complete"
  for svc in prometheus grafana-server; do
    sudo systemctl is-active --quiet "$svc" || die "$svc not active"
    sudo systemctl is-enabled --quiet "$svc" || die "$svc not enabled"
  done
  sudo test -s /root/routegate-grafana-access.txt ||
    die "Grafana access credentials missing"
  [[ "$(sudo stat -c '%a' /root/routegate-grafana-access.txt)" == 600 ]] ||
    die "Grafana access credentials are not root-only"
  curl -fsS --max-time 15 "https://$DOMAIN/grafana/api/health" >/dev/null ||
    die "Grafana HTTPS gateway is unhealthy"
  GRAFANA_PASSWORD=$(sudo sed -n 's/^Initial password: //p' /root/routegate-grafana-access.txt)
  [[ -n "$GRAFANA_PASSWORD" ]] || die "Grafana initial password unavailable"
  curl -fsS --max-time 15 -u "admin:$GRAFANA_PASSWORD" \
    "https://$DOMAIN/grafana/api/datasources/uid/routegate-prometheus" >/dev/null ||
    die "Grafana Prometheus datasource was not provisioned"
  curl -fsS --max-time 15 -u "admin:$GRAFANA_PASSWORD" \
    "https://$DOMAIN/grafana/api/dashboards/uid/routegate-fleet-overview" >/dev/null ||
    die "Grafana fleet dashboard was not provisioned"
  unset GRAFANA_PASSWORD
  log "Restarting Grafana to verify persistent service and HTTPS availability."
  sudo systemctl restart grafana-server
  for i in $(seq 1 30); do
    curl -fsS --max-time 5 "https://$DOMAIN/grafana/api/health" >/dev/null 2>&1 && break
    sleep 1
  done
  curl -fsS --max-time 5 "https://$DOMAIN/grafana/api/health" >/dev/null ||
    die "Grafana did not recover after restart"
  log "Re-running completed installation with Grafana enabled to prove idempotency."
  # Fingerprints only; never print the first-login or Grafana credentials.
  CORE_STATE_SHA=$(sudo sha256sum /etc/routegate/install-state.env | awk '{print $1}')
  FIRST_LOGIN_SHA=$(sudo sha256sum /root/routegate-first-login.txt | awk '{print $1}')
  GRAFANA_ACCESS_SHA=$(sudo sha256sum /root/routegate-grafana-access.txt | awk '{print $1}')
  if ! sudo -n env \
    "PATH=$WORK/bin:$PATH" "GITHUB_ACTIONS=true" "RUNNER_TEMP=$RUNNER_TEMP" \
    "ROUTEGATE_E2E_DOMAIN=$DOMAIN" "ROUTEGATE_E2E_WORK_DIR=$WORK" \
    "NO_PROXY=$NO_PROXY" "no_proxy=$no_proxy" \
    bash "$ROOT/install.sh" \
      --domain "$DOMAIN" --email routegate-ci@example.invalid \
      --admin-email routegate-ci@example.invalid --server-name "$DOMAIN" \
      --version "$VERSION" --bundle-file "$BUNDLE" --checksum-file "$CHECKSUM" \
      --with-grafana --yes >"$WORK/grafana-rerun.stdout" 2>&1; then
    # Explicitly print only diagnostic stage/error lines. Never expose the
    # private initial admin setup URL, Grafana password, or full stdout.
    grep -E '^\\[RouteGate( Grafana)?\\] (ERROR|WARNING)' "$WORK/grafana-rerun.stdout" >&2 || true
    die "Completed RouteGate+Grafana installation was not idempotent"
  fi
  grep -Fq 'RouteGate is already installed and healthy.' "$WORK/grafana-rerun.stdout" ||
    die "Completed install did not use the idempotent core verification path"
  grep -Fq 'Grafana is already installed and healthy.' "$WORK/grafana-rerun.stdout" ||
    die "Repeated Grafana install did not use the idempotent monitoring verification path"
  [[ "$CORE_STATE_SHA" == "$(sudo sha256sum /etc/routegate/install-state.env | awk '{print $1}')" ]] ||
    die "Idempotent retry unexpectedly rewrote RouteGate ownership"
  [[ "$FIRST_LOGIN_SHA" == "$(sudo sha256sum /root/routegate-first-login.txt | awk '{print $1}')" ]] ||
    die "Idempotent retry unexpectedly rotated first-login credentials"
  [[ "$GRAFANA_ACCESS_SHA" == "$(sudo sha256sum /root/routegate-grafana-access.txt | awk '{print $1}')" ]] ||
    die "Idempotent retry unexpectedly rotated Grafana initial credentials"
  for svc in postgresql nginx routegate-manager routegate-agent prometheus grafana-server; do
    sudo systemctl is-active --quiet "$svc" ||
      die "Idempotent retry left $svc unhealthy"
  done
  curl -fsS --max-time 10 "https://$DOMAIN/grafana/api/health" >/dev/null ||
    die "Idempotent retry broke Grafana HTTPS"
  unset CORE_STATE_SHA FIRST_LOGIN_SHA GRAFANA_ACCESS_SHA
  log "PASS: completed RouteGate/Grafana re-run was idempotent; ownership and credentials unchanged."
  log "PASS: real Hybrid + Prometheus + Grafana installed in same run; HTTPS, datasource, dashboard, restart healthy."
  log "OUT OF SCOPE: public Let's Encrypt, real external VPN client, physical reboot."
  exit 0
fi

# Completed installs deliberately remove transient secrets.env. Retrieve the
# owner-only recovery password instead, then prove the temporary secret is gone.
[[ ! -e /var/lib/routegate-installer/secrets.env ]] || die "Transient bootstrap secret not deleted"
if sudo grep -q '^ROUTEGATE_BOOTSTRAP_ADMIN_' /etc/routegate/manager.env; then
  die "Bootstrap values still present in Manager environment"
fi
ADMIN_PASSWORD=$(sudo sed -n 's/^Bootstrap password: //p' /root/routegate-first-login.txt)
[[ -n "$ADMIN_PASSWORD" ]] || die "Missing bootstrap password"
LOGIN=$(jq -nc --arg login routegate-ci@example.invalid --arg password "$ADMIN_PASSWORD" \
  '{login:$login,password:$password}' |
  curl -fsS --max-time 15 -H 'Content-Type: application/json' \
  --data-binary @- "https://$DOMAIN/api/v1/auth/login") || die "HTTPS admin login failed"
ADMIN_TOKEN=$(jq -er '.token' <<<"$LOGIN") || die "Admin token missing"
SERVER=$(curl -fsS --max-time 15 -H "Authorization: Bearer $ADMIN_TOKEN" \
  "https://$DOMAIN/api/v1/servers?search=$DOMAIN") || die "Cannot list servers"
SERVER_ID=$(jq -er --arg name "$DOMAIN" '.items[] | select(.name == $name) | .id' <<<"$SERVER" | head -n1) ||
  die "Local Hybrid server missing"
[[ -n "$SERVER_ID" ]] || die "Hybrid server ID empty"
DETAIL=$(curl -fsS --max-time 15 -H "Authorization: Bearer $ADMIN_TOKEN" \
  "https://$DOMAIN/api/v1/servers/$SERVER_ID") || die "Cannot inspect Agent"
jq -e '.agent.id != null' <<<"$DETAIL" >/dev/null || die "Hybrid Agent not bound"
# Complete the actual first-administrator /setup lifecycle through nginx HTTPS.
# All bearer tokens and passwords stay in local process memory and the
# owner-only recovery file; never echo them to GitHub Actions.
[[ $(sudo stat -c '%a' /root/routegate-first-login.txt) == 600 ]] ||
  die "First-login recovery file is not root-only"
SETUP_URL=$(sudo sed -n 's/^Setup URL: //p' /root/routegate-first-login.txt)
[[ "$SETUP_URL" == "https://$DOMAIN/setup#token="* ]] ||
  die "Missing or malformed one-use setup URL"
SETUP_TOKEN=${SETUP_URL#*#token=}
curl -fsS --max-time 10 "https://$DOMAIN/setup" >/dev/null ||
  die "Browser setup route is unavailable"
INSPECT=$(jq -nc --arg token "$SETUP_TOKEN" '{token:$token}' |
  curl -fsS --max-time 15 -H 'Content-Type: application/json' \
    --data-binary @- "https://$DOMAIN/api/v1/auth/initial-setup/inspect") ||
  die "Setup token cannot be inspected"
jq -e '.email == "routegate-ci@example.invalid"' <<<"$INSPECT" >/dev/null ||
  die "Setup token is bound to wrong administrator"
NEW_PASSWORD="RouteGate-CI-$(openssl rand -hex 14)!"
ACTIVATION=$(jq -nc --arg token "$SETUP_TOKEN" --arg password "$NEW_PASSWORD" \
  '{token:$token,new_password:$password}' |
  curl -fsS --max-time 15 -H 'Content-Type: application/json' \
    --data-binary @- "https://$DOMAIN/api/v1/auth/initial-setup/complete") ||
  die "First-administrator setup activation failed"
SESSION_TOKEN=$(jq -er '.token' <<<"$ACTIVATION") ||
  die "First-setup activation did not create a session"
curl -fsS --max-time 15 -H "Authorization: Bearer $SESSION_TOKEN" \
  "https://$DOMAIN/api/v1/auth/me" >/dev/null ||
  die "New administrator session is invalid"
# The one-use bearer must be consumed, not merely flagged in the UI.
OLD_LINK_STATUS=$(jq -nc --arg token "$SETUP_TOKEN" '{token:$token}' |
  curl -sS --max-time 15 -o /dev/null -w '%{http_code}' \
    -H 'Content-Type: application/json' \
    --data-binary @- "https://$DOMAIN/api/v1/auth/initial-setup/inspect")
[[ "$OLD_LINK_STATUS" == 410 ]] || die "Consumed setup token can still be used"
# Old bootstrap password must be useless after activation.
OLD_LOGIN_STATUS=$(jq -nc --arg login routegate-ci@example.invalid \
  --arg password "$ADMIN_PASSWORD" '{login:$login,password:$password}' |
  curl -sS --max-time 15 -o /dev/null -w '%{http_code}' \
    -H 'Content-Type: application/json' --data-binary @- \
    "https://$DOMAIN/api/v1/auth/login")
[[ "$OLD_LOGIN_STATUS" == 401 ]] ||
  die "Old bootstrap password still authenticates after setup"
# Transfer only the post-activation session and local Hybrid server ID through
# private runner files to the isolated data-plane acceptance test.
printf '%s' "$SESSION_TOKEN" >"$WORK/admin.token"
printf '%s' "$SERVER_ID" >"$WORK/server.id"
chmod 0600 "$WORK/admin.token" "$WORK/server.id"
unset SETUP_URL SETUP_TOKEN INSPECT NEW_PASSWORD ACTIVATION SESSION_TOKEN OLD_LINK_STATUS OLD_LOGIN_STATUS
unset ADMIN_PASSWORD LOGIN ADMIN_TOKEN SERVER DETAIL

log "Restarting installed Manager and Agent."
sudo systemctl restart routegate-manager routegate-agent
for i in $(seq 1 30); do
  if curl -fsS --max-time 3 "https://$DOMAIN/api/admin/health" >/dev/null 2>&1 &&
    sudo systemctl is-active --quiet routegate-agent; then break; fi
  sleep 2
done
curl -fsS --max-time 5 "https://$DOMAIN/api/admin/health" >/dev/null ||
  die "Manager unhealthy after restart"
sudo systemctl is-active --quiet routegate-agent || die "Agent inactive after restart"

log "Extending installed Hybrid with Agent-managed VLESS/Reality traffic."
# Same disposable runner, installed Manager/Agent and database. No release
# changes, remote endpoints, or production systems are involved.
sudo -n env \
  "GITHUB_ACTIONS=true" \
  "RUNNER_ENVIRONMENT=github-hosted" \
  "ROUTEGATE_E2E_ISOLATED=1" \
  "RUNNER_TEMP=$RUNNER_TEMP" \
  "NO_PROXY=$NO_PROXY" "no_proxy=$no_proxy" \
  python3 "$ROOT/scripts/hybrid-vpn-e2e.py" ||
  die "Native Hybrid Reality traffic/apply test failed on disposable runner"

# Manager and nginx must survive all Agent runtime operations.
sudo systemctl is-active --quiet routegate-manager || die "Manager stopped after VPN apply"
sudo systemctl is-active --quiet nginx || die "nginx stopped after VPN apply"
sudo systemctl is-active --quiet sing-box || die "VPN runtime stopped after apply"
curl -fsS --max-time 5 "https://$DOMAIN/api/admin/health" >/dev/null ||
  die "Manager HTTPS failed after Reality traffic test"
rm -f "$WORK/admin.token" "$WORK/server.id" "$WORK/hybrid-client.json"
log "PASS: native install -> /setup -> Reality config/apply -> real test client traffic -> fail-safe apply."
log "OUT OF SCOPE: public Let's Encrypt, remote third-party client, full reboot."
