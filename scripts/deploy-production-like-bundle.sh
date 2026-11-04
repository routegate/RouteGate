#!/usr/bin/env bash
set -Eeuo pipefail
umask 077

prepare_atomic_down_migration() {
  local source_file=$1
  local output_file=$2

  python3 - "$source_file" "$output_file" <<'PY'
from pathlib import Path
import re
import sys

source_path = Path(sys.argv[1])
output_path = Path(sys.argv[2])
lines = source_path.read_text().splitlines(keepends=True)


def meaningful_indices(items):
    result = []
    for index, line in enumerate(items):
        stripped = line.strip()
        if not stripped or stripped.startswith("--"):
            continue
        result.append(index)
    return result


indices = meaningful_indices(lines)
if not indices:
    raise SystemExit("down migration is empty")

begin_re = re.compile(r"^BEGIN(?:\s+TRANSACTION)?;$", re.IGNORECASE)
commit_re = re.compile(r"^COMMIT;$", re.IGNORECASE)
first = indices[0]
last = indices[-1]
has_begin = bool(begin_re.fullmatch(lines[first].strip()))
has_commit = bool(commit_re.fullmatch(lines[last].strip()))

if has_begin != has_commit:
    raise SystemExit("down migration has an unmatched outer transaction boundary")
if has_begin:
    del lines[last]
    del lines[first]

for line in lines:
    stripped = line.strip()
    if not stripped or stripped.startswith("--"):
        continue
    if begin_re.fullmatch(stripped) or commit_re.fullmatch(stripped) or stripped.upper() == "ROLLBACK;":
        raise SystemExit("down migration contains transaction control that the atomic rollback runner cannot safely own")

output_path.write_text("".join(lines))
PY
}

run_atomic_down_migration() {
  local db_url=$1
  local down_file=$2
  local version=$3
  local transaction_file
  local rc=0

  [[ "$version" =~ ^[A-Za-z0-9][A-Za-z0-9._-]*$ ]] || {
    printf '[production-like] invalid rollback migration identifier\n' >&2
    return 1
  }
  [[ -f "$down_file" && ! -L "$down_file" ]] || {
    printf '[production-like] unsafe rollback migration file: %s\n' "$down_file" >&2
    return 1
  }

  transaction_file=$(mktemp /tmp/routegate-down-migration.XXXXXX.sql) || return 1
  if ! prepare_atomic_down_migration "$down_file" "$transaction_file"; then
    rm -f -- "$transaction_file"
    return 1
  fi
  cat >>"$transaction_file" <<'SQL'

DELETE FROM schema_migrations
WHERE version = :'rg_migration_version';
SQL

  psql "$db_url" \
    -X \
    -v ON_ERROR_STOP=1 \
    -v rg_migration_version="$version" \
    --single-transaction \
    -f "$transaction_file" >/dev/null || rc=$?
  rm -f -- "$transaction_file"
  return "$rc"
}

if [[ ${1:-} == "--run-down-migration" || ${1:-} == "--run-down-migration-inherited" ]]; then
  [[ $# -eq 4 ]] || {
    printf 'usage: %s --run-down-migration <database-url> <down-file> <version>\n' "$0" >&2
    exit 2
  }
  lock_file=${RG_UPDATE_LOCK_FILE:-/run/lock/routegate-update.lock}
  if [[ $1 == --run-down-migration-inherited ]]; then
    # Re-lock the same open file description; a new open would contend
    # with the waiting Manager. Reject a missing or unrelated descriptor.
    [[ /proc/self/fd/8 -ef "$lock_file" ]] || {
      printf '[production-like] invalid inherited host update lock\n' >&2
      exit 1
    }
  else
    exec 8>>"$lock_file"
  fi
  flock -n 8 || { printf '[production-like] host update lock unavailable\n' >&2; exit 1; }
  run_atomic_down_migration "$2" "$3" "$4"
  exit $?
fi

EXPECTED_COMMIT=${1:?expected commit is required}
BUNDLE_FILE=${2:?bundle path is required}
EXPECTED_BUNDLE_SHA=${3:?bundle sha256 is required}
VALIDATION_SCRIPT=${4:?validation script path is required}
UPDATE_CORE=${5:?update core path is required}
BOOTSTRAP_ARM64_BUNDLE=${6:-}
BOOTSTRAP_CHECKSUMS=${7:-}
PUBLIC_URL=${ROUTEGATE_PUBLIC_URL_OVERRIDE:-https://us.routegate.org}
BOOTSTRAP_ROOT=${ROUTEGATE_BOOTSTRAP_ROOT:-/var/www/routegate/bootstrap}
BOOTSTRAP_DIR="${BOOTSTRAP_ROOT}/${EXPECTED_COMMIT}"
NGINX_SITE=${ROUTEGATE_NGINX_SITE:-/etc/nginx/sites-available/routegate}
NGINX_BIN=${ROUTEGATE_NGINX_BIN:-/usr/sbin/nginx}
BACKUP_DIR=""
BACKUP_SCHEMA=""
NGINX_BACKUP=""
NGINX_MUTATED=0
DB_URL=""
EXPECTED_SCHEMA=""
MUTATED=0
DB_MAY_BE_MUTATED=0
STAGE=initializing
MAINTENANCE_DISPATCH_STATE=""
MAINTENANCE_DISPATCH_ENABLED=0
MAINTENANCE_DISPATCH_ACTIVE=0
MAINTENANCE_DISPATCH_MUTATED=0
MAINTENANCE_DISPATCHER=/usr/local/lib/routegate/update/routegate-maintenance-dispatch.py
MAINTENANCE_SOCKET_UNIT=/etc/systemd/system/routegate-maintenance-dispatch.socket
MAINTENANCE_SERVICE_UNIT=/etc/systemd/system/routegate-maintenance-dispatch@.service

[[ -r "$UPDATE_CORE" ]] || { printf 'Update core is not readable.\n' >&2; exit 1; }
RG_UPDATE_LOG_PREFIX='[production-like]'
# shellcheck source=scripts/routegate-update-core.sh
source "$UPDATE_CORE"
rg_update_acquire_production_like_lock
WORK_DIR=$(mktemp -d /tmp/routegate-production-like.XXXXXX)

log() {
  rg_update_log "$*"
}

backup_maintenance_dispatch() {
  local backup_dir=$1
  local backup_files="$backup_dir/maintenance-dispatch"
  local path present=0

  for path in "$MAINTENANCE_DISPATCHER" "$MAINTENANCE_SOCKET_UNIT" "$MAINTENANCE_SERVICE_UNIT"; do
    if [[ -e "$path" || -L "$path" ]]; then
      present=$((present + 1))
    fi
  done

  if ((present == 0)); then
    MAINTENANCE_DISPATCH_STATE=absent
  elif ((present == 3)) \
    && [[ -f "$MAINTENANCE_DISPATCHER" && ! -L "$MAINTENANCE_DISPATCHER" && -x "$MAINTENANCE_DISPATCHER" ]] \
    && [[ -f "$MAINTENANCE_SOCKET_UNIT" && ! -L "$MAINTENANCE_SOCKET_UNIT" ]] \
    && [[ -f "$MAINTENANCE_SERVICE_UNIT" && ! -L "$MAINTENANCE_SERVICE_UNIT" ]]; then
    MAINTENANCE_DISPATCH_STATE=complete
    systemctl is-enabled --quiet routegate-maintenance-dispatch.socket >/dev/null 2>&1 \
      && MAINTENANCE_DISPATCH_ENABLED=1
    systemctl is-active --quiet routegate-maintenance-dispatch.socket >/dev/null 2>&1 \
      && MAINTENANCE_DISPATCH_ACTIVE=1
    install -d -m 0700 "$backup_files" || return 1
    cp -a -- "$MAINTENANCE_DISPATCHER" "$backup_files/routegate-maintenance-dispatch.py" || return 1
    cp -a -- "$MAINTENANCE_SOCKET_UNIT" "$backup_files/routegate-maintenance-dispatch.socket" || return 1
    cp -a -- "$MAINTENANCE_SERVICE_UNIT" "$backup_files/routegate-maintenance-dispatch@.service" || return 1
  else
    printf '[production-like] privileged maintenance dispatch state is partial or unsafe\n' >&2
    return 1
  fi

  cat >"$backup_dir/maintenance-dispatch.meta" <<EOF_MAINTENANCE_DISPATCH
FORMAT_VERSION=1
STATE=$MAINTENANCE_DISPATCH_STATE
ENABLED=$MAINTENANCE_DISPATCH_ENABLED
ACTIVE=$MAINTENANCE_DISPATCH_ACTIVE
EOF_MAINTENANCE_DISPATCH
  chmod 0600 "$backup_dir/maintenance-dispatch.meta" || return 1
  log "maintenance dispatch backup state=$MAINTENANCE_DISPATCH_STATE"
}

install_maintenance_dispatch() {
  local candidate_dispatcher="$WORK_DIR/tools/routegate-maintenance-dispatch.py"
  local candidate_socket="$WORK_DIR/systemd/routegate-maintenance-dispatch.socket"
  local candidate_service="$WORK_DIR/systemd/routegate-maintenance-dispatch@.service"
  local path

  for path in "$candidate_dispatcher" "$candidate_socket" "$candidate_service"; do
    [[ -f "$path" && ! -L "$path" ]] || {
      printf '[production-like] release bundle is missing a safe maintenance dispatch component: %s\n' "$path" >&2
      return 1
    }
  done
  python3 -c 'import ast, pathlib, sys; ast.parse(pathlib.Path(sys.argv[1]).read_text())' \
    "$candidate_dispatcher" || return 1

  MAINTENANCE_DISPATCH_MUTATED=1
  install -d -m 0755 "$(dirname "$MAINTENANCE_DISPATCHER")" || return 1
  install -m 0755 "$candidate_dispatcher" "$MAINTENANCE_DISPATCHER" || return 1
  install -m 0644 "$candidate_socket" "$MAINTENANCE_SOCKET_UNIT" || return 1
  install -m 0644 "$candidate_service" "$MAINTENANCE_SERVICE_UNIT" || return 1
  install -d -o root -g root -m 0755 /run/routegate || return 1
  systemctl daemon-reload || return 1
  systemctl enable --now routegate-maintenance-dispatch.socket || return 1
  systemctl is-active --quiet routegate-maintenance-dispatch.socket || return 1
  [[ -S /run/routegate/maintenance-dispatch.sock ]] || return 1
  log "maintenance dispatch socket=active"
}

restore_maintenance_dispatch() {
  local backup_dir=$1
  local backup_files="$backup_dir/maintenance-dispatch"
  local rollback_rc=0

  ((MAINTENANCE_DISPATCH_MUTATED == 1)) || return 0
  systemctl disable --now routegate-maintenance-dispatch.socket >/dev/null 2>&1 || true
  rm -f -- "$MAINTENANCE_DISPATCHER" "$MAINTENANCE_SOCKET_UNIT" "$MAINTENANCE_SERVICE_UNIT" \
    || rollback_rc=1

  if [[ "$MAINTENANCE_DISPATCH_STATE" == complete ]]; then
    install -m 0755 "$backup_files/routegate-maintenance-dispatch.py" "$MAINTENANCE_DISPATCHER" \
      || rollback_rc=1
    install -m 0644 "$backup_files/routegate-maintenance-dispatch.socket" "$MAINTENANCE_SOCKET_UNIT" \
      || rollback_rc=1
    install -m 0644 "$backup_files/routegate-maintenance-dispatch@.service" "$MAINTENANCE_SERVICE_UNIT" \
      || rollback_rc=1
  elif [[ "$MAINTENANCE_DISPATCH_STATE" != absent ]]; then
    rollback_rc=1
  fi

  systemctl daemon-reload || rollback_rc=1
  if [[ "$MAINTENANCE_DISPATCH_STATE" == complete ]]; then
    if ((MAINTENANCE_DISPATCH_ENABLED == 1)); then
      systemctl enable routegate-maintenance-dispatch.socket >/dev/null 2>&1 || rollback_rc=1
    fi
    if ((MAINTENANCE_DISPATCH_ACTIVE == 1)); then
      systemctl start routegate-maintenance-dispatch.socket >/dev/null 2>&1 || rollback_rc=1
    fi
  fi
  log "maintenance dispatch rollback restored state=$MAINTENANCE_DISPATCH_STATE"
  return "$rollback_rc"
}

reconcile_subscription_proxy_route() {
  local template="$WORK_DIR/nginx/routegate.conf.example"
  local candidate
  local api_location_count

  [[ -f "$template" && ! -L "$template" ]] \
    || { printf '[production-like] release bundle is missing nginx route template.\n' >&2; return 1; }
  [[ -f "$NGINX_SITE" && ! -L "$NGINX_SITE" ]] \
    || { printf '[production-like] RouteGate nginx site is missing or unsafe: %s\n' "$NGINX_SITE" >&2; return 1; }
  [[ -x "$NGINX_BIN" ]] \
    || { printf '[production-like] nginx binary is unavailable: %s\n' "$NGINX_BIN" >&2; return 1; }

  if grep -Eq '^[[:space:]]*location[[:space:]]+/sub/[[:space:]]*\{' "$NGINX_SITE"; then
    log "nginx subscription proxy route=present"
    return 0
  fi

  grep -Eq '^[[:space:]]*location[[:space:]]+/sub/[[:space:]]*\{' "$template" \
    || { printf '[production-like] bundle nginx template has no /sub/ route.\n' >&2; return 1; }
  grep -Fq 'access_log off;' "$template" \
    || { printf '[production-like] bundle /sub/ route does not disable access logging.\n' >&2; return 1; }

  api_location_count=$(grep -Ec '^[[:space:]]*location[[:space:]]+/api/[[:space:]]*\{' "$NGINX_SITE" || true)
  [[ "$api_location_count" == "1" ]] \
    || { printf '[production-like] expected exactly one /api/ nginx location, found %s.\n' "$api_location_count" >&2; return 1; }

  NGINX_BACKUP="$BACKUP_DIR/nginx-routegate.conf"
  cp -a -- "$NGINX_SITE" "$NGINX_BACKUP" || return 1
  candidate=$(mktemp "$(dirname "$NGINX_SITE")/.routegate-subscription-route.XXXXXX") || return 1

  if ! python3 - "$NGINX_SITE" "$template" "$candidate" <<'PY'
from pathlib import Path
import sys

live_path, template_path, output_path = map(Path, sys.argv[1:])
live = live_path.read_text().splitlines(keepends=True)
template = template_path.read_text().splitlines(keepends=True)


def extract_location(lines, marker):
    starts = [i for i, line in enumerate(lines) if line.strip() == marker]
    if len(starts) != 1:
        raise SystemExit(f"expected one {marker!r} block, found {len(starts)}")
    start = starts[0]
    depth = 0
    block = []
    for line in lines[start:]:
        block.append(line)
        depth += line.count("{") - line.count("}")
        if depth == 0:
            break
    if not block or depth != 0:
        raise SystemExit(f"unterminated {marker!r} block")
    return block


block = extract_location(template, "location /sub/ {")
block_text = "".join(block)
if "access_log off;" not in block_text or "proxy_pass http://127.0.0.1:8080;" not in block_text:
    raise SystemExit("subscription block is missing required security/proxy directives")

api_positions = [i for i, line in enumerate(live) if line.strip() == "location /api/ {"]
if len(api_positions) != 1:
    raise SystemExit(f"expected one live /api/ location, found {len(api_positions)}")
insert_at = api_positions[0]
indent = live[insert_at][: len(live[insert_at]) - len(live[insert_at].lstrip())]
base_indent = block[0][: len(block[0]) - len(block[0].lstrip())]
rendered = []
for line in block:
    if line.strip():
        if not line.startswith(base_indent):
            raise SystemExit("subscription block indentation is inconsistent")
        rendered.append(indent + line[len(base_indent):])
    else:
        rendered.append(line)

if rendered and not rendered[-1].endswith("\n"):
    rendered[-1] += "\n"
rendered.append("\n")
output_path.write_text("".join(live[:insert_at] + rendered + live[insert_at:]))
PY
  then
    rm -f -- "$candidate"
    return 1
  fi

  cat -- "$candidate" > "$NGINX_SITE" || { rm -f -- "$candidate"; return 1; }
  rm -f -- "$candidate"

  if ! "$NGINX_BIN" -t; then
    cp -a -- "$NGINX_BACKUP" "$NGINX_SITE"
    "$NGINX_BIN" -t >/dev/null 2>&1 || true
    printf '[production-like] nginx validation rejected the /sub/ route; original site restored.\n' >&2
    return 1
  fi
  if ! systemctl reload nginx; then
    cp -a -- "$NGINX_BACKUP" "$NGINX_SITE"
    "$NGINX_BIN" -t >/dev/null 2>&1 || true
    systemctl reload nginx >/dev/null 2>&1 || true
    printf '[production-like] nginx reload failed; original site restored.\n' >&2
    return 1
  fi

  NGINX_MUTATED=1
  log "nginx subscription proxy route=reconciled"
}

reconcile_bootstrap_artifact_route() {
  local template="$WORK_DIR/nginx/routegate.conf.example"
  local candidate
  local route_count

  [[ -f "$template" && ! -L "$template" ]] \
    || { printf '[production-like] release bundle is missing nginx route template.\n' >&2; return 1; }
  [[ -f "$NGINX_SITE" && ! -L "$NGINX_SITE" ]] \
    || { printf '[production-like] RouteGate nginx site is missing or unsafe: %s\n' "$NGINX_SITE" >&2; return 1; }
  [[ -x "$NGINX_BIN" ]] \
    || { printf '[production-like] nginx binary is unavailable: %s\n' "$NGINX_BIN" >&2; return 1; }

  route_count=$(grep -Ec '^[[:space:]]*location[[:space:]]+\^~[[:space:]]+/bootstrap/[[:space:]]*\{' "$NGINX_SITE" || true)
  if [[ "$route_count" == "1" ]]; then
    grep -Fq 'root /var/www/routegate;' "$NGINX_SITE" \
      || { printf '[production-like] existing bootstrap route does not use the fixed RouteGate document root.\n' >&2; return 1; }
    grep -Fq "try_files \$uri =404;" "$NGINX_SITE" \
      || { printf '[production-like] existing bootstrap route may fall back to the SPA.\n' >&2; return 1; }
    log "nginx bootstrap artifact route=present"
    return 0
  fi
  [[ "$route_count" == "0" ]] \
    || { printf '[production-like] expected at most one bootstrap artifact route, found %s.\n' "$route_count" >&2; return 1; }

  grep -Eq '^[[:space:]]*location[[:space:]]+\^~[[:space:]]+/bootstrap/[[:space:]]*\{' "$template" \
    || { printf '[production-like] bundle nginx template has no /bootstrap/ route.\n' >&2; return 1; }
  grep -Fq 'root /var/www/routegate;' "$template" \
    || { printf '[production-like] bundle /bootstrap/ route does not pin the RouteGate document root.\n' >&2; return 1; }
  grep -Fq "try_files \$uri =404;" "$template" \
    || { printf '[production-like] bundle /bootstrap/ route does not fail closed.\n' >&2; return 1; }

  if [[ -z "$NGINX_BACKUP" ]]; then
    NGINX_BACKUP="$BACKUP_DIR/nginx-routegate.conf"
    cp -a -- "$NGINX_SITE" "$NGINX_BACKUP" || return 1
  fi
  candidate=$(mktemp "$(dirname "$NGINX_SITE")/.routegate-bootstrap-route.XXXXXX") || return 1

  if ! python3 - "$NGINX_SITE" "$template" "$candidate" <<'PY'
from pathlib import Path
import sys

live_path, template_path, output_path = map(Path, sys.argv[1:])
live = live_path.read_text().splitlines(keepends=True)
template = template_path.read_text().splitlines(keepends=True)

def extract_location(lines, marker):
    starts = [i for i, line in enumerate(lines) if line.strip() == marker]
    if len(starts) != 1:
        raise SystemExit(f"expected one {marker!r} block, found {len(starts)}")
    start = starts[0]
    depth = 0
    block = []
    for line in lines[start:]:
        block.append(line)
        depth += line.count("{") - line.count("}")
        if depth == 0:
            break
    if not block or depth != 0:
        raise SystemExit(f"unterminated {marker!r} block")
    return block

block = extract_location(template, "location ^~ /bootstrap/ {")
block_text = "".join(block)
required = (
    "root /var/www/routegate;",
    "try_files $uri =404;",
    "default_type application/octet-stream;",
)
for directive in required:
    if directive not in block_text:
        raise SystemExit(f"bootstrap block is missing {directive!r}")

anchor_positions = [i for i, line in enumerate(live) if line.strip() == "location /sub/ {"]
if not anchor_positions:
    anchor_positions = [i for i, line in enumerate(live) if line.strip() == "location /api/ {"]
if len(anchor_positions) != 1:
    raise SystemExit(f"expected one live insertion anchor, found {len(anchor_positions)}")
insert_at = anchor_positions[0]
indent = live[insert_at][: len(live[insert_at]) - len(live[insert_at].lstrip())]
base_indent = block[0][: len(block[0]) - len(block[0].lstrip())]
rendered = []
for line in block:
    if line.strip():
        if not line.startswith(base_indent):
            raise SystemExit("bootstrap block indentation is inconsistent")
        rendered.append(indent + line[len(base_indent):])
    else:
        rendered.append(line)

if rendered and not rendered[-1].endswith("\n"):
    rendered[-1] += "\n"
rendered.append("\n")
output_path.write_text("".join(live[:insert_at] + rendered + live[insert_at:]))
PY
  then
    rm -f -- "$candidate"
    return 1
  fi

  cat -- "$candidate" > "$NGINX_SITE" || { rm -f -- "$candidate"; return 1; }
  rm -f -- "$candidate"

  if ! "$NGINX_BIN" -t; then
    cp -a -- "$NGINX_BACKUP" "$NGINX_SITE"
    "$NGINX_BIN" -t >/dev/null 2>&1 || true
    printf '[production-like] nginx validation rejected the /bootstrap/ route; original site restored.\n' >&2
    return 1
  fi
  if ! systemctl reload nginx; then
    cp -a -- "$NGINX_BACKUP" "$NGINX_SITE"
    "$NGINX_BIN" -t >/dev/null 2>&1 || true
    systemctl reload nginx >/dev/null 2>&1 || true
    printf '[production-like] nginx reload failed for the /bootstrap/ route; original site restored.\n' >&2
    return 1
  fi

  NGINX_MUTATED=1
  log "nginx bootstrap artifact route=reconciled"
}

reconcile_hysteria_acme_bridge() {
  local template="$WORK_DIR/nginx/routegate.conf.example"
  local candidate
  local bridge_count

  [[ -f "$template" && ! -L "$template" ]] \
    || { printf '[production-like] release bundle is missing nginx route template.\n' >&2; return 1; }
  [[ -f "$NGINX_SITE" && ! -L "$NGINX_SITE" ]] \
    || { printf '[production-like] RouteGate nginx site is missing or unsafe: %s\n' "$NGINX_SITE" >&2; return 1; }
  [[ -x "$NGINX_BIN" ]] \
    || { printf '[production-like] nginx binary is unavailable: %s\n' "$NGINX_BIN" >&2; return 1; }

  bridge_count=$(grep -Ec '^[[:space:]]*location[[:space:]]+\^~[[:space:]]+/\.well-known/acme-challenge/[[:space:]]*\{' "$NGINX_SITE" || true)
  if [[ "$bridge_count" == "1" ]]; then
    grep -Fq 'proxy_pass http://127.0.0.1:9080;' "$NGINX_SITE" \
      || { printf '[production-like] existing Hysteria2 ACME bridge does not use the fixed loopback endpoint.\n' >&2; return 1; }
    log "nginx Hysteria2 ACME bridge=present"
    return 0
  fi
  [[ "$bridge_count" == "0" ]] \
    || { printf '[production-like] expected at most one Hysteria2 ACME bridge, found %s.\n' "$bridge_count" >&2; return 1; }

  grep -Eq '^[[:space:]]*location[[:space:]]+\^~[[:space:]]+/\.well-known/acme-challenge/[[:space:]]*\{' "$template" \
    || { printf '[production-like] bundle nginx template has no Hysteria2 ACME bridge.\n' >&2; return 1; }
  grep -Fq 'proxy_pass http://127.0.0.1:9080;' "$template" \
    || { printf '[production-like] bundle Hysteria2 ACME bridge does not use the fixed loopback endpoint.\n' >&2; return 1; }

  if [[ -z "$NGINX_BACKUP" ]]; then
    NGINX_BACKUP="$BACKUP_DIR/nginx-routegate.conf"
    cp -a -- "$NGINX_SITE" "$NGINX_BACKUP" || return 1
  fi
  candidate=$(mktemp "$(dirname "$NGINX_SITE")/.routegate-hysteria-acme-bridge.XXXXXX") || return 1

  if ! python3 - "$NGINX_SITE" "$template" "$candidate" <<'PY'
from pathlib import Path
import sys

live_path, template_path, output_path = map(Path, sys.argv[1:])
live = live_path.read_text().splitlines(keepends=True)
template = template_path.read_text().splitlines(keepends=True)
marker = "location ^~ /.well-known/acme-challenge/ {"

matches = [i for i, line in enumerate(template) if line.strip() == marker]
if len(matches) != 1:
    raise SystemExit(f"expected one Hysteria2 ACME bridge marker, found {len(matches)}")

depth = 0
server_start = None
for index, line in enumerate(template[: matches[0] + 1]):
    stripped = line.strip()
    if depth == 0 and stripped == "server {":
        server_start = index
    depth += line.count("{") - line.count("}")
if server_start is None:
    raise SystemExit("Hysteria2 ACME bridge is not inside a server block")

depth = 0
server_block = []
for line in template[server_start:]:
    server_block.append(line)
    depth += line.count("{") - line.count("}")
    if depth == 0:
        break
if not server_block or depth != 0:
    raise SystemExit("unterminated Hysteria2 ACME bridge server block")

block_text = "".join(server_block)
required = (
    "listen 80 default_server;",
    "server_name _;",
    "proxy_pass http://127.0.0.1:9080;",
    "return 404;",
)
for directive in required:
    if directive not in block_text:
        raise SystemExit(f"Hysteria2 ACME bridge is missing {directive!r}")

rendered = "".join(live)
if rendered and not rendered.endswith("\n"):
    rendered += "\n"
rendered += "\n" + block_text
output_path.write_text(rendered)
PY
  then
    rm -f -- "$candidate"
    return 1
  fi

  cat -- "$candidate" > "$NGINX_SITE" || { rm -f -- "$candidate"; return 1; }
  rm -f -- "$candidate"

  if ! "$NGINX_BIN" -t; then
    cp -a -- "$NGINX_BACKUP" "$NGINX_SITE"
    "$NGINX_BIN" -t >/dev/null 2>&1 || true
    printf '[production-like] nginx validation rejected the Hysteria2 ACME bridge; original site restored.\n' >&2
    return 1
  fi
  if ! systemctl reload nginx; then
    cp -a -- "$NGINX_BACKUP" "$NGINX_SITE"
    "$NGINX_BIN" -t >/dev/null 2>&1 || true
    systemctl reload nginx >/dev/null 2>&1 || true
    printf '[production-like] nginx reload failed for the Hysteria2 ACME bridge; original site restored.\n' >&2
    return 1
  fi

  NGINX_MUTATED=1
  log "nginx Hysteria2 ACME bridge=reconciled"
}

bootstrap_checksum_for() {
  local name=$1
  awk -v name="$name" '$2 == name || $2 == "*" name {print $1; exit}' "$BOOTSTRAP_CHECKSUMS"
}

verify_bootstrap_bundle() {
  local bundle=$1
  local name=$2
  local arch=$3
  local expected actual manifest version commit os manifest_arch

  [[ -f "$bundle" && ! -L "$bundle" ]] || {
    printf '[production-like] bootstrap bundle is missing or unsafe: %s\n' "$bundle" >&2
    return 1
  }
  expected=$(bootstrap_checksum_for "$name")
  [[ "$expected" =~ ^[a-fA-F0-9]{64}$ ]] || {
    printf '[production-like] bootstrap checksum entry is missing for %s\n' "$name" >&2
    return 1
  }
  actual=$(sha256sum "$bundle" | awk '{print $1}')
  [[ "$actual" == "$expected" ]] || {
    printf '[production-like] bootstrap bundle checksum mismatch for %s\n' "$name" >&2
    return 1
  }

  manifest=$(tar -xOf "$bundle" ./metadata/manifest.env 2>/dev/null) || {
    printf '[production-like] bootstrap bundle manifest is unavailable for %s\n' "$name" >&2
    return 1
  }
  version=$(sed -n 's/^VERSION=//p' <<<"$manifest" | head -n1)
  commit=$(sed -n 's/^COMMIT=//p' <<<"$manifest" | head -n1)
  os=$(sed -n 's/^OS=//p' <<<"$manifest" | head -n1)
  manifest_arch=$(sed -n 's/^ARCH=//p' <<<"$manifest" | head -n1)
  [[ "$version" == "production-like" && "$commit" == "$EXPECTED_COMMIT" && "$os" == "linux" && "$manifest_arch" == "$arch" ]] || {
    printf '[production-like] bootstrap bundle identity mismatch for %s\n' "$name" >&2
    return 1
  }
}

verify_bootstrap_artifacts() {
  if [[ -z "$BOOTSTRAP_ARM64_BUNDLE" && -z "$BOOTSTRAP_CHECKSUMS" ]]; then
    log "bootstrap artifact publication=not requested"
    return 0
  fi
  [[ -n "$BOOTSTRAP_ARM64_BUNDLE" && -n "$BOOTSTRAP_CHECKSUMS" ]] || {
    printf '[production-like] bootstrap artifacts must provide both arm64 bundle and SHA256SUMS\n' >&2
    return 1
  }
  [[ -f "$BOOTSTRAP_CHECKSUMS" && ! -L "$BOOTSTRAP_CHECKSUMS" ]] || {
    printf '[production-like] bootstrap checksum file is missing or unsafe\n' >&2
    return 1
  }
  verify_bootstrap_bundle "$BUNDLE_FILE" "routegate-production-like-linux-amd64.tar.gz" amd64
  verify_bootstrap_bundle "$BOOTSTRAP_ARM64_BUNDLE" "routegate-production-like-linux-arm64.tar.gz" arm64
}

publish_bootstrap_artifacts() {
  local staging
  [[ -n "$BOOTSTRAP_ARM64_BUNDLE" && -n "$BOOTSTRAP_CHECKSUMS" ]] || return 0
  [[ ! -L "$BOOTSTRAP_ROOT" ]] || {
    printf '[production-like] bootstrap artifact root must not be a symlink\n' >&2
    return 1
  }
  install -d -m 0755 "$BOOTSTRAP_ROOT" || return 1
  staging=$(mktemp -d "${BOOTSTRAP_ROOT}/.${EXPECTED_COMMIT}.XXXXXX") || return 1
  # mktemp creates directories with mode 0700. nginx runs as an unprivileged
  # worker and must be able to traverse the published commit directory.
  chmod 0755 "$staging" || { rm -rf -- "$staging"; return 1; }
  install -m 0644 "$BUNDLE_FILE" "$staging/routegate-production-like-linux-amd64.tar.gz" || {
    rm -rf -- "$staging"
    return 1
  }
  install -m 0644 "$BOOTSTRAP_ARM64_BUNDLE" "$staging/routegate-production-like-linux-arm64.tar.gz" || {
    rm -rf -- "$staging"
    return 1
  }
  install -m 0644 "$BOOTSTRAP_CHECKSUMS" "$staging/SHA256SUMS" || {
    rm -rf -- "$staging"
    return 1
  }

  if [[ -e "$BOOTSTRAP_DIR" || -L "$BOOTSTRAP_DIR" ]]; then
    [[ -d "$BOOTSTRAP_DIR" && ! -L "$BOOTSTRAP_DIR" ]] || {
      rm -rf -- "$staging"
      printf '[production-like] existing bootstrap artifact path is unsafe: %s\n' "$BOOTSTRAP_DIR" >&2
      return 1
    }
    rm -rf -- "$BOOTSTRAP_DIR" || {
      rm -rf -- "$staging"
      return 1
    }
  fi
  mv -- "$staging" "$BOOTSTRAP_DIR" || return 1

  find "$BOOTSTRAP_ROOT" -mindepth 1 -maxdepth 1 -type d -mtime +2 -exec rm -rf -- {} + || return 1
  log "bootstrap artifacts published commit=$EXPECTED_COMMIT"
}

runtime_status() {
  local label=$1
  local service=$2
  local load_state
  local state

  load_state=$(systemctl show --property=LoadState --value "$service" 2>/dev/null || true)
  if [[ "$load_state" != "loaded" ]]; then
    log "runtime ${label}=not-installed-or-unmanaged"
    return 0
  fi

  state=$(systemctl is-active "$service" 2>/dev/null || true)
  [[ -n "$state" ]] || state=unknown
  log "runtime ${label} service=${service} state=${state}"
}

log_runtime_diagnostics() {
  runtime_status sing-box sing-box
  runtime_status wireguard wg-quick@routegate-wg0
  runtime_status hysteria2 hysteria-server
  runtime_status mtproto routegate-mtproto

  if command -v sing-box >/dev/null 2>&1 && [[ -r /etc/sing-box/config.json ]]; then
    if sing-box check -c /etc/sing-box/config.json >/dev/null 2>&1; then
      log "runtime sing-box config=valid"
    else
      log "runtime sing-box config=invalid"
    fi
  fi
}

wait_for_active_agent_jobs() {
  local db_url=$1
  local timeout_seconds=${2:-360}
  local poll_seconds=${3:-2}
  local deadline=$((SECONDS + timeout_seconds))
  local active_jobs
  local quiet_polls=0

  while :; do
    active_jobs=$(psql "$db_url" -v ON_ERROR_STOP=1 -qAtc "
SELECT
  (SELECT count(*) FROM config_apply_jobs WHERE status IN ('pending', 'in_progress')) +
  (SELECT count(*) FROM agent_operation_jobs WHERE status IN ('pending', 'in_progress'));
") || return 1

    if [[ ! "$active_jobs" =~ ^[0-9]+$ ]]; then
      printf '[production-like] invalid active Agent job count: %s\n' "$active_jobs" >&2
      return 1
    fi

    if ((active_jobs == 0)); then
      quiet_polls=$((quiet_polls + 1))
      if ((quiet_polls >= 2)); then
        log "active Agent job drain complete"
        return 0
      fi
    else
      quiet_polls=0
      log "waiting for ${active_jobs} active Agent job(s) before platform deploy"
    fi

    if ((SECONDS >= deadline)); then
      printf '[production-like] timed out waiting for active Agent jobs to finish.\n' >&2
      return 1
    fi
    sleep "$poll_seconds"
  done
}

capture_manager_failure_diagnostics() {
  local active_state sub_state result restarts exec_code exec_status
  local journal_file failure_class migration_version

  active_state=$(systemctl show --property=ActiveState --value "$RG_UPDATE_MANAGER_SERVICE" 2>/dev/null || true)
  sub_state=$(systemctl show --property=SubState --value "$RG_UPDATE_MANAGER_SERVICE" 2>/dev/null || true)
  result=$(systemctl show --property=Result --value "$RG_UPDATE_MANAGER_SERVICE" 2>/dev/null || true)
  restarts=$(systemctl show --property=NRestarts --value "$RG_UPDATE_MANAGER_SERVICE" 2>/dev/null || true)
  exec_code=$(systemctl show --property=ExecMainCode --value "$RG_UPDATE_MANAGER_SERVICE" 2>/dev/null || true)
  exec_status=$(systemctl show --property=ExecMainStatus --value "$RG_UPDATE_MANAGER_SERVICE" 2>/dev/null || true)
  log "manager failure active=${active_state:-unknown} sub=${sub_state:-unknown} result=${result:-unknown} restarts=${restarts:-unknown} exec-code=${exec_code:-unknown} exec-status=${exec_status:-unknown}"

  [[ -n "$BACKUP_DIR" && -d "$BACKUP_DIR" ]] || return 0
  command -v journalctl >/dev/null 2>&1 || {
    log "manager failure journal=unavailable"
    return 0
  }

  journal_file="$BACKUP_DIR/manager-failure.log"
  journalctl -u "$RG_UPDATE_MANAGER_SERVICE" -n 120 --no-pager -o cat >"$journal_file" 2>/dev/null || true
  chmod 0600 "$journal_file" 2>/dev/null || true

  failure_class=startup
  migration_version=""
  if grep -Eqi 'apply migration|record migration|commit migration' "$journal_file"; then
    failure_class=migration
    migration_version=$(grep -Eio 'migration[[:space:]]+[A-Za-z0-9._-]+' "$journal_file" | tail -n 1 | awk '{print $2}' || true)
  elif grep -Eqi 'SQLSTATE|postgres|database|pgx' "$journal_file"; then
    failure_class=database
  elif grep -Eqi 'address already in use|bind:' "$journal_file"; then
    failure_class=listener-conflict
  elif grep -Eqi 'permission denied|operation not permitted' "$journal_file"; then
    failure_class=permission
  fi

  if [[ -n "$migration_version" ]]; then
    log "manager failure class=${failure_class} migration=${migration_version} raw-journal=retained-on-host"
  else
    log "manager failure class=${failure_class} raw-journal=retained-on-host"
  fi
}

rollback_database_to_backup() {
  local backup_dir=$1
  local db_url=$2
  local target_schema current_schema migrations_dir version down_file
  local target_seen=0
  local restore_rc=0

  RG_UPDATE_DB_RESTORE_RC=0

  [[ -n "$db_url" && -s "$backup_dir/routegate.pgdump" ]] || {
    RG_UPDATE_DB_RESTORE_RC=1
    printf '[production-like] WARNING: database restore requested but database backup is unavailable\n' >&2
    return 1
  }

  target_schema=$(sed -n 's/^DATABASE_SCHEMA=//p' "$backup_dir/backup.meta" | head -n 1)
  [[ "$target_schema" =~ ^[A-Za-z0-9][A-Za-z0-9._-]*$ ]] || {
    RG_UPDATE_DB_RESTORE_RC=1
    printf '[production-like] WARNING: backup database schema metadata is missing or invalid\n' >&2
    return 1
  }

  migrations_dir=$(rg_update_path /opt/routegate-manager/migrations) || {
    RG_UPDATE_DB_RESTORE_RC=1
    return 1
  }

  current_schema=$(psql "$db_url" -v ON_ERROR_STOP=1 -qAtc "SELECT version FROM schema_migrations ORDER BY applied_at DESC, version DESC LIMIT 1") || {
    RG_UPDATE_DB_RESTORE_RC=1
    return 1
  }

  if [[ "$current_schema" != "$target_schema" ]]; then
    log "database rollback schema current=${current_schema:-missing} target=${target_schema}"
    while IFS= read -r version; do
      [[ -n "$version" ]] || continue
      if [[ "$version" == "$target_schema" ]]; then
        target_seen=1
        break
      fi
      [[ "$version" =~ ^[A-Za-z0-9][A-Za-z0-9._-]*$ ]] || {
        RG_UPDATE_DB_RESTORE_RC=1
        printf '[production-like] WARNING: unsafe migration identifier during rollback\n' >&2
        return 1
      }
      down_file="$migrations_dir/${version}.down.sql"
      [[ -f "$down_file" && ! -L "$down_file" ]] || {
        RG_UPDATE_DB_RESTORE_RC=1
        printf '[production-like] WARNING: missing safe down migration for %s; database rollback stopped\n' "$version" >&2
        return 1
      }

      log "database rollback migration=${version}"
      if ! run_atomic_down_migration "$db_url" "$down_file" "$version"; then
        RG_UPDATE_DB_RESTORE_RC=1
        printf '[production-like] WARNING: atomic down migration failed for %s; database rollback stopped\n' "$version" >&2
        return 1
      fi
    done < <(psql "$db_url" -v ON_ERROR_STOP=1 -qAtc "SELECT version FROM schema_migrations ORDER BY applied_at DESC, version DESC")

    if ((target_seen == 0)); then
      RG_UPDATE_DB_RESTORE_RC=1
      printf '[production-like] WARNING: backup schema %s is not present in current migration history\n' "$target_schema" >&2
      return 1
    fi

    current_schema=$(psql "$db_url" -v ON_ERROR_STOP=1 -qAtc "SELECT version FROM schema_migrations ORDER BY applied_at DESC, version DESC LIMIT 1") || {
      RG_UPDATE_DB_RESTORE_RC=1
      return 1
    }
    [[ "$current_schema" == "$target_schema" ]] || {
      RG_UPDATE_DB_RESTORE_RC=1
      printf '[production-like] WARNING: database down-migration stopped at %s instead of %s\n' "${current_schema:-missing}" "$target_schema" >&2
      return 1
    }
  fi

  pg_restore \
    --clean \
    --if-exists \
    --no-owner \
    --no-privileges \
    --exit-on-error \
    --dbname="$db_url" \
    "$backup_dir/routegate.pgdump" >/dev/null || restore_rc=$?

  RG_UPDATE_DB_RESTORE_RC=$restore_rc
  if ((restore_rc != 0)); then
    printf '[production-like] WARNING: database restore failed after schema rollback (exit %d)\n' "$restore_rc" >&2
    return "$restore_rc"
  fi

  log "database rollback restored backup schema=${target_schema}"
  return 0
}

cleanup() {
  rm -rf "$WORK_DIR"
  rm -f "$BUNDLE_FILE" "$VALIDATION_SCRIPT" "$UPDATE_CORE"
  [[ -z "$BOOTSTRAP_ARM64_BUNDLE" ]] || rm -f "$BOOTSTRAP_ARM64_BUNDLE"
  [[ -z "$BOOTSTRAP_CHECKSUMS" ]] || rm -f "$BOOTSTRAP_CHECKSUMS"
}

rollback() {
  local rc=$?
  local rollback_rc=0
  local db_rollback_rc=0
  trap - ERR
  set +e

  if [[ "$MUTATED" == "1" && -n "$BACKUP_DIR" ]]; then
    log "Failure at stage=${STAGE}; restoring production-like baseline."

    if ! systemctl stop "$RG_UPDATE_AGENT_SERVICE" "$RG_UPDATE_MANAGER_SERVICE" >/dev/null 2>&1; then
      log "ROLLBACK INCOMPLETE: service stop failed; restore withheld; backup retained at ${BACKUP_DIR}"
      cleanup
      exit "$rc"
    fi

    if ! rg_update_check_manager_restore_environment "$BACKUP_DIR"; then
      log "ROLLBACK INCOMPLETE: environment/unit preflight failed; database and file restore withheld; backup retained at ${BACKUP_DIR}"
      cleanup
      exit "$rc"
    fi

    if [[ "$DB_MAY_BE_MUTATED" == "1" ]]; then
      rollback_database_to_backup "$BACKUP_DIR" "$DB_URL" || db_rollback_rc=$?
    else
      RG_UPDATE_DB_RESTORE_RC=0
    fi

    restore_maintenance_dispatch "$BACKUP_DIR" || rollback_rc=$?
    rg_update_restore_backup "$BACKUP_DIR" "$DB_URL" 0 "$db_rollback_rc" || rollback_rc=$?
    set +e
    if [[ "$NGINX_MUTATED" == "1" && -n "$NGINX_BACKUP" && -f "$NGINX_BACKUP" ]]; then
      cp -a -- "$NGINX_BACKUP" "$NGINX_SITE" || rollback_rc=1
      "$NGINX_BIN" -t >/dev/null 2>&1 || rollback_rc=1
      systemctl reload nginx >/dev/null 2>&1 || rollback_rc=1
    fi
    if ((db_rollback_rc != 0)); then
      rollback_rc=1
    fi
    if ((rollback_rc != 0)); then
      printf '[production-like] WARNING: rollback reported an incomplete restore. Backup retained at %s\n' "$BACKUP_DIR" >&2
    fi
    if ((RG_UPDATE_DB_RESTORE_RC != 0)); then
      printf '[production-like] WARNING: database restore reported exit %d. Backup retained at %s\n' \
        "$RG_UPDATE_DB_RESTORE_RC" "$BACKUP_DIR" >&2
    fi
  fi

  cleanup
  exit "$rc"
}

trap rollback ERR
trap cleanup EXIT

rg_update_require_root
rg_update_require_commands curl date find grep pg_dump pg_restore psql python3 sha256sum tar systemctl
[[ -r /etc/routegate/manager.env ]] || { printf 'Missing /etc/routegate/manager.env\n' >&2; exit 1; }
[[ -r "$VALIDATION_SCRIPT" ]] || { printf 'Validation script is not readable.\n' >&2; exit 1; }

STAGE=preflight
rg_update_control_plane_preflight
log_runtime_diagnostics

STAGE=bundle_verification
rg_update_verify_and_extract_bundle \
  "$BUNDLE_FILE" \
  "$EXPECTED_BUNDLE_SHA" \
  "$EXPECTED_COMMIT" \
  linux \
  amd64 \
  "$WORK_DIR"
EXPECTED_SCHEMA=$RG_UPDATE_EXPECTED_SCHEMA
verify_bootstrap_artifacts

set -a
# shellcheck disable=SC1091
source /etc/routegate/manager.env
set +a
DB_URL=${ROUTEGATE_DATABASE_URL:?ROUTEGATE_DATABASE_URL is required}

STAGE=backup
BACKUP_SCHEMA=$(psql "$DB_URL" -v ON_ERROR_STOP=1 -qAtc "SELECT version FROM schema_migrations ORDER BY applied_at DESC, version DESC LIMIT 1")
[[ "$BACKUP_SCHEMA" =~ ^[A-Za-z0-9][A-Za-z0-9._-]*$ ]] \
  || { printf '[production-like] invalid current database schema before backup: %s\n' "${BACKUP_SCHEMA:-missing}" >&2; exit 1; }
BACKUP_DIR="/root/routegate-backups/rg96-${EXPECTED_COMMIT}-$(date -u +%Y%m%dT%H%M%SZ)"
rg_update_create_backup "$BACKUP_DIR" "$DB_URL"
backup_maintenance_dispatch "$BACKUP_DIR"
printf 'DATABASE_SCHEMA=%s\n' "$BACKUP_SCHEMA" >>"$BACKUP_DIR/backup.meta"
chmod 0600 "$BACKUP_DIR/backup.meta"
log "backup database schema=${BACKUP_SCHEMA}"

# The production-like deploy replaces and restarts Manager and Agent binaries.
# Do not interrupt a protocol activation or runtime operation that is already
# being processed. Two consecutive idle polls also narrow the race with an
# operation that was queued while the backup was being created.
STAGE=drain_agent_jobs
wait_for_active_agent_jobs "$DB_URL" 360 2

STAGE=deploy_files
MUTATED=1
rg_update_apply_platform_files "$WORK_DIR"

STAGE=bootstrap_artifacts
publish_bootstrap_artifacts

STAGE=maintenance_dispatch
install_maintenance_dispatch

if grep -q '^ROUTEGATE_PUBLIC_URL=' /etc/routegate/manager.env; then
  sed -i "s#^ROUTEGATE_PUBLIC_URL=.*#ROUTEGATE_PUBLIC_URL=\"${PUBLIC_URL}\"#" /etc/routegate/manager.env
else
  printf 'ROUTEGATE_PUBLIC_URL="%s"\n' "$PUBLIC_URL" >> /etc/routegate/manager.env
fi
chmod 0600 /etc/routegate/manager.env

STAGE=manager_start
DB_MAY_BE_MUTATED=1
if ! rg_update_wait_manager 45; then
  capture_manager_failure_diagnostics
  false
fi

STAGE=schema_validation
rg_update_validate_database_schema "$DB_URL" "$EXPECTED_SCHEMA"

STAGE=agent_start
rg_update_wait_agent 30

STAGE=nginx_subscription_proxy
reconcile_subscription_proxy_route

STAGE=nginx_hysteria_acme_bridge
reconcile_hysteria_acme_bridge

STAGE=nginx_bootstrap_artifacts
reconcile_bootstrap_artifact_route

STAGE=observability_validation
chmod 0700 "$VALIDATION_SCRIPT"
"$VALIDATION_SCRIPT" "$EXPECTED_COMMIT"

STAGE=final_health
systemctl is-active --quiet routegate-manager
systemctl is-active --quiet routegate-agent
systemctl is-active --quiet routegate-maintenance-dispatch.socket
[[ -S /run/routegate/maintenance-dispatch.sock ]]
public_status=$(curl -sS -o /dev/null -w '%{http_code}' "$PUBLIC_URL/")
[[ "$public_status" == 200 ]]
subscription_probe_status=$(curl -sS -o /dev/null -w '%{http_code}' "$PUBLIC_URL/sub/routegate-deploy-probe")
[[ "$subscription_probe_status" == 404 ]]
log "subscription proxy probe=http_$subscription_probe_status"

bootstrap_checksum_url="$PUBLIC_URL/bootstrap/$EXPECTED_COMMIT/SHA256SUMS"
bootstrap_checksum_probe=$(curl -fsSL --max-time 30 "$bootstrap_checksum_url")
bootstrap_amd64_sha=$(awk '$2 == "routegate-production-like-linux-amd64.tar.gz" || $2 == "*routegate-production-like-linux-amd64.tar.gz" {print $1; exit}' <<<"$bootstrap_checksum_probe")
bootstrap_arm64_sha=$(awk '$2 == "routegate-production-like-linux-arm64.tar.gz" || $2 == "*routegate-production-like-linux-arm64.tar.gz" {print $1; exit}' <<<"$bootstrap_checksum_probe")
[[ "$bootstrap_amd64_sha" =~ ^[a-fA-F0-9]{64}$ ]]
[[ "$bootstrap_arm64_sha" =~ ^[a-fA-F0-9]{64}$ ]]
[[ "$bootstrap_amd64_sha" == "$(bootstrap_checksum_for routegate-production-like-linux-amd64.tar.gz)" ]]
[[ "$bootstrap_arm64_sha" == "$(bootstrap_checksum_for routegate-production-like-linux-arm64.tar.gz)" ]]
log "bootstrap public checksum probe=verified"

log_runtime_diagnostics

STAGE=complete
trap - ERR
log "production-like deploy and validation PASSED"
log "deployed_commit=$EXPECTED_COMMIT"
log "backup=$BACKUP_DIR"
