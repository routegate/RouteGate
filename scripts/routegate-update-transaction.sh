#!/usr/bin/env bash

set -Eeuo pipefail
IFS=$'\n\t'
umask 077

SCRIPT_DIR=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
# shellcheck source=scripts/routegate-update-core.sh
source "$SCRIPT_DIR/routegate-update-core.sh"
# shellcheck source=scripts/routegate-update-role.sh
source "$SCRIPT_DIR/routegate-update-role.sh"

OPERATION=${1:-}
if [[ -n "$OPERATION" ]]; then
  shift
fi

BUNDLE=""
EXPECTED_SHA=""
EXPECTED_COMMIT=""
REQUESTED_ROLE="auto"
LOCK_FILE=${RG_UPDATE_LOCK_FILE:-/run/lock/routegate-update.lock}
BACKUP_ROOT=${RG_UPDATE_BACKUP_ROOT:-/root/routegate-backups}
WORK_DIR=""
BACKUP_DIR=""
DB_URL=""
ROLE=""
TARGET_ARCH=""
MUTATED=0
DB_MAY_BE_MUTATED=0
STAGE=initializing
# Exit 75 is a fixed machine-readable boundary meaning the transaction mutated
# host state and could not prove that rollback restored it completely. Remote
# callers must classify this as outcome_unknown, never as an ordinary failure.
ROLLBACK_INCOMPLETE_EXIT=75

usage() {
  cat <<'USAGE'
RouteGate local host update transaction

Usage:
  sudo routegate-update-transaction.sh apply \
    --bundle /path/to/verified-routegate-bundle.tar.gz \
    --sha256 <64-character-sha256> \
    --commit <40-character-git-sha> \
    [--role auto|management|vpn|hybrid]

This command is a root-only local transaction primitive. It performs no release
discovery, network download, or provenance lookup. Do not expose it directly to
Manager/Web input until the privileged update boundary independently verifies
RouteGate release provenance.
USAGE
}

log() {
  RG_UPDATE_LOG_PREFIX='[routegate-update-transaction]'
  rg_update_log "$*"
}

die() {
  RG_UPDATE_LOG_PREFIX='[routegate-update-transaction]'
  rg_update_die "$*"
  exit 1
}

parse_args() {
  while (($# > 0)); do
    case "$1" in
      --bundle)
        (($# >= 2)) || die "--bundle requires a value"
        BUNDLE=$2
        shift 2
        ;;
      --sha256)
        (($# >= 2)) || die "--sha256 requires a value"
        EXPECTED_SHA=$2
        shift 2
        ;;
      --commit)
        (($# >= 2)) || die "--commit requires a value"
        EXPECTED_COMMIT=$2
        shift 2
        ;;
      --role)
        (($# >= 2)) || die "--role requires a value"
        REQUESTED_ROLE=$2
        shift 2
        ;;
      --help|-h)
        usage
        exit 0
        ;;
      *) die "unknown argument: $1" ;;
    esac
  done

  [[ -n "$BUNDLE" ]] || die "--bundle is required"
  [[ -n "$EXPECTED_SHA" ]] || die "--sha256 is required"
  [[ -n "$EXPECTED_COMMIT" ]] || die "--commit is required"
  [[ "$REQUESTED_ROLE" == "auto" ]] || rg_update_validate_role "$REQUESTED_ROLE" || exit 1
  [[ -f "$BUNDLE" && ! -L "$BUNDLE" ]] || die "bundle must be a regular local file"
}

platform_architecture() {
  case "$1" in
    x86_64|amd64) printf 'amd64\n' ;;
    aarch64|arm64) printf 'arm64\n' ;;
    *) return 1 ;;
  esac
}

cleanup() {
  if [[ -n "$WORK_DIR" && -d "$WORK_DIR" ]]; then
    rm -rf "$WORK_DIR"
  fi
}

trusted_path_is_secure() {
  local path=$1
  local label=$2
  local owner permissions

  owner=$(stat -c '%u' -- "$path") || return 1
  permissions=$(stat -c '%A' -- "$path") || return 1

  [[ "$owner" == "$EUID" ]] || {
    rg_update_die "$label is not owned by the privileged updater user: $path"
    return 1
  }
  [[ "${permissions:5:1}" != "w" && "${permissions:8:1}" != "w" ]] || {
    rg_update_die "$label is group/world writable: $path"
    return 1
  }
}

validate_trusted_toolchain_security() {
  local state tool_dir entrypoint tool_parent entrypoint_parent path file
  local expected_entrypoint actual_entrypoint

  state=$(rg_update_toolchain_state) || return 1
  tool_dir=$(rg_update_path "$RG_UPDATE_TOOLCHAIN_DIR") || return 1
  entrypoint=$(rg_update_path "$RG_UPDATE_ENTRYPOINT") || return 1
  tool_parent=$(rg_update_path /usr/local/lib/routegate) || return 1
  entrypoint_parent=$(dirname "$entrypoint") || return 1

  for path in "$tool_parent" "$entrypoint_parent"; do
    if [[ -e "$path" || -L "$path" ]]; then
      [[ -d "$path" && ! -L "$path" ]] || {
        rg_update_die "trusted updater parent path is unsafe: $path"
        return 1
      }
      trusted_path_is_secure "$path" "trusted updater parent" || return 1
    fi
  done

  if [[ "$state" == "absent" ]]; then
    return 0
  fi

  trusted_path_is_secure "$tool_dir" "trusted updater directory" || return 1
  trusted_path_is_secure "$entrypoint" "trusted updater entrypoint" || return 1
  while IFS= read -r file; do
    trusted_path_is_secure "$tool_dir/$file" "trusted updater component" || return 1
  done < <(rg_update_toolchain_files)

  [[ -x "$tool_dir/routegate-update-transaction.sh" \
    && -x "$tool_dir/routegate-update-verified.sh" \
    && -x "$tool_dir/routegate-update-dispatch.py" ]] || {
    rg_update_die "trusted updater executable components are not executable"
    return 1
  }

  expected_entrypoint=$'#!/usr/bin/env bash\nset -Eeuo pipefail\nexec /usr/local/lib/routegate/update/routegate-update-verified.sh "$@"'
  actual_entrypoint=$(cat -- "$entrypoint") || return 1
  [[ "$actual_entrypoint" == "$expected_entrypoint" ]] || {
    rg_update_die "trusted updater entrypoint content is not canonical"
    return 1
  }
}

dispatch_unit_paths() {
  local update_socket update_service maintenance_socket maintenance_service
  update_socket=$(rg_update_path /etc/systemd/system/routegate-update-dispatch.socket) || return 1
  update_service=$(rg_update_path /etc/systemd/system/routegate-update-dispatch@.service) || return 1
  maintenance_socket=$(rg_update_path /etc/systemd/system/routegate-maintenance-dispatch.socket) || return 1
  maintenance_service=$(rg_update_path /etc/systemd/system/routegate-maintenance-dispatch@.service) || return 1
  printf '%s\n%s\n%s\n%s\n' "$update_socket" "$update_service" "$maintenance_socket" "$maintenance_service"
}

validate_dispatch_candidate() {
  local role=$1 work_dir=$2 source
  rg_update_role_has_management "$role" || return 0
  for source in \
    "$work_dir/systemd/routegate-update-dispatch.socket" \
    "$work_dir/systemd/routegate-update-dispatch@.service" \
    "$work_dir/systemd/routegate-maintenance-dispatch.socket" \
    "$work_dir/systemd/routegate-maintenance-dispatch@.service" \
    "$work_dir/tools/routegate-maintenance-dispatch.py"; do
    [[ -f "$source" && ! -L "$source" ]] || {
      rg_update_die "release bundle is missing privileged dispatch component: $source"
      return 1
    }
  done
  python3 -c 'import ast, pathlib, sys; ast.parse(pathlib.Path(sys.argv[1]).read_text())' \
    "$work_dir/tools/routegate-maintenance-dispatch.py" || {
    rg_update_die "release bundle contains an invalid privileged maintenance dispatch executable"
    return 1
  }
}

dispatch_units_state() {
  local update_socket update_service maintenance_socket maintenance_service maintenance_dispatcher
  local -a paths
  mapfile -t paths < <(dispatch_unit_paths) || return 1
  update_socket=${paths[0]}
  update_service=${paths[1]}
  maintenance_socket=${paths[2]}
  maintenance_service=${paths[3]}
  maintenance_dispatcher=$(rg_update_path "$RG_UPDATE_TOOLCHAIN_DIR/routegate-maintenance-dispatch.py") || return 1

  if [[ ! -e "$update_socket" && ! -L "$update_socket" && ! -e "$update_service" && ! -L "$update_service" \
    && ! -e "$maintenance_socket" && ! -L "$maintenance_socket" \
    && ! -e "$maintenance_service" && ! -L "$maintenance_service" \
    && ! -e "$maintenance_dispatcher" && ! -L "$maintenance_dispatcher" ]]; then
    printf 'absent\n'
    return 0
  fi
  [[ -f "$update_socket" && ! -L "$update_socket" && -f "$update_service" && ! -L "$update_service" ]] || {
    rg_update_die "privileged update dispatch unit state is partial or unsafe"
    return 1
  }
  trusted_path_is_secure "$update_socket" "privileged update dispatch socket unit" || return 1
  trusted_path_is_secure "$update_service" "privileged update dispatch service unit" || return 1
  if [[ ! -e "$maintenance_socket" && ! -L "$maintenance_socket" \
    && ! -e "$maintenance_service" && ! -L "$maintenance_service" \
    && ! -e "$maintenance_dispatcher" && ! -L "$maintenance_dispatcher" ]]; then
    printf 'update_only\n'
    return 0
  fi
  [[ -f "$maintenance_socket" && ! -L "$maintenance_socket" \
    && -f "$maintenance_service" && ! -L "$maintenance_service" \
    && -f "$maintenance_dispatcher" && ! -L "$maintenance_dispatcher" \
    && -x "$maintenance_dispatcher" ]] || {
    rg_update_die "privileged maintenance dispatch state is partial or unsafe"
    return 1
  }
  trusted_path_is_secure "$maintenance_socket" "privileged maintenance dispatch socket unit" || return 1
  trusted_path_is_secure "$maintenance_service" "privileged maintenance dispatch service unit" || return 1
  trusted_path_is_secure "$maintenance_dispatcher" "privileged maintenance dispatch executable" || return 1
  printf 'complete\n'
}

create_dispatch_units_backup() {
  local role=$1 backup_dir=$2 state update_enabled=0 update_active=0 maintenance_enabled=0 maintenance_active=0
  local update_socket update_service maintenance_socket maintenance_service maintenance_dispatcher backup_units
  local -a paths
  rg_update_role_has_management "$role" || return 0

  state=$(dispatch_units_state) || return 1
  if [[ -z "$RG_UPDATE_ROOT" && ( "$state" == "complete" || "$state" == "update_only" ) ]]; then
    systemctl is-enabled --quiet routegate-update-dispatch.socket >/dev/null 2>&1 && update_enabled=1
    systemctl is-active --quiet routegate-update-dispatch.socket >/dev/null 2>&1 && update_active=1
  fi
  if [[ -z "$RG_UPDATE_ROOT" && "$state" == "complete" ]]; then
    systemctl is-enabled --quiet routegate-maintenance-dispatch.socket >/dev/null 2>&1 && maintenance_enabled=1
    systemctl is-active --quiet routegate-maintenance-dispatch.socket >/dev/null 2>&1 && maintenance_active=1
  fi
  cat >"$backup_dir/dispatch-units.meta" <<EOF_DISPATCH_META
FORMAT_VERSION=2
STATE=$state
UPDATE_ENABLED=$update_enabled
UPDATE_ACTIVE=$update_active
MAINTENANCE_ENABLED=$maintenance_enabled
MAINTENANCE_ACTIVE=$maintenance_active
EOF_DISPATCH_META
  chmod 0600 "$backup_dir/dispatch-units.meta" || return 1

  if [[ "$state" == "complete" || "$state" == "update_only" ]]; then
    mapfile -t paths < <(dispatch_unit_paths) || return 1
    update_socket=${paths[0]}
    update_service=${paths[1]}
    maintenance_socket=${paths[2]}
    maintenance_service=${paths[3]}
    backup_units="$backup_dir/dispatch-units"
    install -d -m 0700 "$backup_units" || return 1
    cp -a -- "$update_socket" "$backup_units/routegate-update-dispatch.socket" || return 1
    cp -a -- "$update_service" "$backup_units/routegate-update-dispatch@.service" || return 1
    if [[ "$state" == "complete" ]]; then
      maintenance_dispatcher=$(rg_update_path "$RG_UPDATE_TOOLCHAIN_DIR/routegate-maintenance-dispatch.py") || return 1
      cp -a -- "$maintenance_socket" "$backup_units/routegate-maintenance-dispatch.socket" || return 1
      cp -a -- "$maintenance_service" "$backup_units/routegate-maintenance-dispatch@.service" || return 1
      cp -a -- "$maintenance_dispatcher" "$backup_units/routegate-maintenance-dispatch.py" || return 1
    fi
  fi
  log "privileged dispatch backup state=$state"
}

install_dispatch_units() {
  local role=$1 work_dir=$2 update_socket update_service maintenance_socket maintenance_service maintenance_dispatcher
  local -a paths
  rg_update_role_has_management "$role" || return 0
  validate_dispatch_candidate "$role" "$work_dir" || return 1
  mapfile -t paths < <(dispatch_unit_paths) || return 1
  update_socket=${paths[0]}
  update_service=${paths[1]}
  maintenance_socket=${paths[2]}
  maintenance_service=${paths[3]}
  maintenance_dispatcher=$(rg_update_path "$RG_UPDATE_TOOLCHAIN_DIR/routegate-maintenance-dispatch.py") || return 1

  install -d -m 0755 "$(dirname "$update_socket")" || return 1
  install -m 0644 "$work_dir/systemd/routegate-update-dispatch.socket" "$update_socket" || return 1
  install -m 0644 "$work_dir/systemd/routegate-update-dispatch@.service" "$update_service" || return 1
  install -m 0755 "$work_dir/tools/routegate-maintenance-dispatch.py" "$maintenance_dispatcher" || return 1
  install -m 0644 "$work_dir/systemd/routegate-maintenance-dispatch.socket" "$maintenance_socket" || return 1
  install -m 0644 "$work_dir/systemd/routegate-maintenance-dispatch@.service" "$maintenance_service" || return 1
  trusted_path_is_secure "$update_socket" "privileged update dispatch socket unit" || return 1
  trusted_path_is_secure "$update_service" "privileged update dispatch service unit" || return 1
  trusted_path_is_secure "$maintenance_dispatcher" "privileged maintenance dispatch executable" || return 1
  trusted_path_is_secure "$maintenance_socket" "privileged maintenance dispatch socket unit" || return 1
  trusted_path_is_secure "$maintenance_service" "privileged maintenance dispatch service unit" || return 1
  if [[ -z "$RG_UPDATE_ROOT" ]]; then
    systemctl daemon-reload || return 1
  fi
  log "privileged update and maintenance dispatch components promoted"
}

activate_dispatch_units() {
  local role=$1
  rg_update_role_has_management "$role" || return 0
  if [[ -z "$RG_UPDATE_ROOT" ]]; then
    install -d -o root -g root -m 0755 /run/routegate || return 1
    systemctl enable --now routegate-update-dispatch.socket || return 1
    systemctl enable --now routegate-maintenance-dispatch.socket || return 1
  fi
  log "privileged update and maintenance dispatch sockets active"
}

restore_dispatch_units_backup() {
  local role=$1 backup_dir=$2 meta state update_enabled update_active maintenance_enabled maintenance_active rollback_rc=0
  local update_socket update_service maintenance_socket maintenance_service maintenance_dispatcher backup_units
  local -a paths
  rg_update_role_has_management "$role" || return 0

  meta="$backup_dir/dispatch-units.meta"
  [[ -f "$meta" && ! -L "$meta" ]] || {
    rg_update_die "privileged dispatch backup metadata is missing"
    return 1
  }
  [[ "$(sed -n 's/^FORMAT_VERSION=//p' "$meta" | head -n1)" == "2" ]] || return 1
  state=$(sed -n 's/^STATE=//p' "$meta" | head -n1) || return 1
  update_enabled=$(sed -n 's/^UPDATE_ENABLED=//p' "$meta" | head -n1) || return 1
  update_active=$(sed -n 's/^UPDATE_ACTIVE=//p' "$meta" | head -n1) || return 1
  maintenance_enabled=$(sed -n 's/^MAINTENANCE_ENABLED=//p' "$meta" | head -n1) || return 1
  maintenance_active=$(sed -n 's/^MAINTENANCE_ACTIVE=//p' "$meta" | head -n1) || return 1
  [[ "$state" == "absent" || "$state" == "update_only" || "$state" == "complete" ]] || return 1
  for value in "$update_enabled" "$update_active" "$maintenance_enabled" "$maintenance_active"; do
    [[ "$value" == "0" || "$value" == "1" ]] || return 1
  done

  mapfile -t paths < <(dispatch_unit_paths) || return 1
  update_socket=${paths[0]}
  update_service=${paths[1]}
  maintenance_socket=${paths[2]}
  maintenance_service=${paths[3]}
  maintenance_dispatcher=$(rg_update_path "$RG_UPDATE_TOOLCHAIN_DIR/routegate-maintenance-dispatch.py") || return 1
  if [[ -z "$RG_UPDATE_ROOT" ]]; then
    systemctl disable --now routegate-update-dispatch.socket >/dev/null 2>&1 || true
    systemctl disable --now routegate-maintenance-dispatch.socket >/dev/null 2>&1 || true
  fi
  rm -f -- "$update_socket" "$update_service" "$maintenance_socket" "$maintenance_service" "$maintenance_dispatcher" || rollback_rc=1

  if [[ "$state" == "complete" || "$state" == "update_only" ]]; then
    backup_units="$backup_dir/dispatch-units"
    install -d -m 0755 "$(dirname "$update_socket")" || rollback_rc=1
    install -m 0644 "$backup_units/routegate-update-dispatch.socket" "$update_socket" || rollback_rc=1
    install -m 0644 "$backup_units/routegate-update-dispatch@.service" "$update_service" || rollback_rc=1
    if [[ "$state" == "complete" ]]; then
      install -m 0644 "$backup_units/routegate-maintenance-dispatch.socket" "$maintenance_socket" || rollback_rc=1
      install -m 0644 "$backup_units/routegate-maintenance-dispatch@.service" "$maintenance_service" || rollback_rc=1
      install -m 0755 "$backup_units/routegate-maintenance-dispatch.py" "$maintenance_dispatcher" || rollback_rc=1
    fi
  fi

  if [[ -z "$RG_UPDATE_ROOT" ]]; then
    systemctl daemon-reload || rollback_rc=1
    if [[ "$state" == "complete" || "$state" == "update_only" ]]; then
      if [[ "$update_enabled" == "1" ]]; then
        systemctl enable routegate-update-dispatch.socket >/dev/null 2>&1 || rollback_rc=1
      fi
      if [[ "$update_active" == "1" ]]; then
        systemctl start routegate-update-dispatch.socket >/dev/null 2>&1 || rollback_rc=1
      fi
    fi
    if [[ "$state" == "complete" ]]; then
      if [[ "$maintenance_enabled" == "1" ]]; then
        systemctl enable routegate-maintenance-dispatch.socket >/dev/null 2>&1 || rollback_rc=1
      fi
      if [[ "$maintenance_active" == "1" ]]; then
        systemctl start routegate-maintenance-dispatch.socket >/dev/null 2>&1 || rollback_rc=1
      fi
    fi
  fi
  log "privileged dispatch rollback restored state=$state"
  return "$rollback_rc"
}

rollback() {
  local rc=${1:-$?}
  local platform_rollback_rc=0
  local dispatch_rollback_rc=0
  local toolchain_rollback_rc=0
  trap - ERR INT TERM
  set +e

  if [[ "$MUTATED" == "1" && -n "$BACKUP_DIR" && -n "$ROLE" ]]; then
    log "failure stage=$STAGE; starting role-aware rollback"

    rg_update_restore_role_backup "$ROLE" "$BACKUP_DIR" "$DB_URL" "$DB_MAY_BE_MUTATED" \
      || platform_rollback_rc=$?
    rg_update_restore_toolchain_backup "$BACKUP_DIR" \
      || toolchain_rollback_rc=$?
    restore_dispatch_units_backup "$ROLE" "$BACKUP_DIR" \
      || dispatch_rollback_rc=$?
    if ((toolchain_rollback_rc == 0)); then
      validate_trusted_toolchain_security || toolchain_rollback_rc=$?
    fi

    if ((platform_rollback_rc != 0)); then
      printf '[routegate-update-transaction] WARNING: platform rollback reported exit %d; backup=%s\n' \
        "$platform_rollback_rc" "$BACKUP_DIR" >&2
    fi
    if ((dispatch_rollback_rc != 0)); then
      printf '[routegate-update-transaction] WARNING: privileged dispatch unit rollback reported exit %d; backup=%s\n' \
        "$dispatch_rollback_rc" "$BACKUP_DIR" >&2
    fi
    if ((toolchain_rollback_rc != 0)); then
      printf '[routegate-update-transaction] WARNING: trusted updater rollback reported exit %d; backup=%s\n' \
        "$toolchain_rollback_rc" "$BACKUP_DIR" >&2
    fi
    if ((RG_UPDATE_DB_RESTORE_RC != 0)); then
      printf '[routegate-update-transaction] WARNING: database restore reported exit %d; backup=%s\n' \
        "$RG_UPDATE_DB_RESTORE_RC" "$BACKUP_DIR" >&2
    fi

    if ((platform_rollback_rc != 0 || dispatch_rollback_rc != 0 || toolchain_rollback_rc != 0 || RG_UPDATE_DB_RESTORE_RC != 0)); then
      printf '[routegate-update-transaction] WARNING: rollback completeness is unknown; reporting reserved exit %d\n' \
        "$ROLLBACK_INCOMPLETE_EXIT" >&2
      rc=$ROLLBACK_INCOMPLETE_EXIT
    fi
  fi

  cleanup
  exit "$rc"
}

acquire_lock() {
  install -d -m 0755 "$(dirname "$LOCK_FILE")"
  exec 9>"$LOCK_FILE"
  flock -n 9 || die "another RouteGate host update transaction is already running"
}

require_role_commands() {
  local role=$1
  rg_update_require_commands \
    awk \
    bash \
    basename \
    cat \
    chmod \
    cp \
    date \
    dirname \
    find \
    flock \
    grep \
    gzip \
    head \
    install \
    mktemp \
    python3 \
    rm \
    sed \
    sha256sum \
    sleep \
    sort \
    stat \
    tar \
    tail \
    systemctl \
    uname \
    wc || return 1
  if rg_update_role_has_management "$role"; then
    rg_update_require_commands chown curl pg_dump pg_restore psql || return 1
  fi
}

main() {
  if [[ "$OPERATION" == "--help" || "$OPERATION" == "-h" || -z "$OPERATION" ]]; then
    usage
    if [[ -n "$OPERATION" ]]; then
      exit 0
    fi
    exit 2
  fi
  [[ "$OPERATION" == "apply" ]] || die "unsupported operation: $OPERATION"

  parse_args "$@"
  rg_update_require_root || exit 1
  rg_update_require_commands flock install uname || exit 1
  acquire_lock
  trap cleanup EXIT

  TARGET_ARCH=$(platform_architecture "$(uname -m)") || die "unsupported host architecture: $(uname -m)"
  ROLE=$(rg_update_resolve_role "$REQUESTED_ROLE") || exit 1
  require_role_commands "$ROLE" || exit 1
  log "resolved role=$ROLE arch=$TARGET_ARCH"

  STAGE=toolchain_preflight
  validate_trusted_toolchain_security

  STAGE=preflight
  rg_update_role_preflight "$ROLE"

  WORK_DIR=$(mktemp -d /tmp/routegate-update.XXXXXX)
  STAGE=bundle_verification
  rg_update_verify_and_extract_bundle \
    "$BUNDLE" \
    "$EXPECTED_SHA" \
    "$EXPECTED_COMMIT" \
    linux \
    "$TARGET_ARCH" \
    "$WORK_DIR"
  validate_dispatch_candidate "$ROLE" "$WORK_DIR"

  if rg_update_role_has_management "$ROLE"; then
    DB_URL=$(rg_update_read_administrative_database_url)
  fi

  STAGE=backup
  BACKUP_DIR="${BACKUP_ROOT%/}/update-${ROLE}-${EXPECTED_COMMIT}-$(date -u +%Y%m%dT%H%M%SZ)"
  rg_update_create_role_backup "$ROLE" "$BACKUP_DIR" "$DB_URL"
  rg_update_create_toolchain_backup "$BACKUP_DIR"
  create_dispatch_units_backup "$ROLE" "$BACKUP_DIR"
  trap rollback ERR
  trap 'rollback 130' INT
  trap 'rollback 143' TERM

  STAGE=apply
  MUTATED=1
  rg_update_apply_role_files "$ROLE" "$WORK_DIR"

  STAGE=health
  if rg_update_role_has_management "$ROLE"; then
    DB_MAY_BE_MUTATED=1
  fi
  rg_update_start_and_validate_role "$ROLE" "$DB_URL" "$RG_UPDATE_EXPECTED_SCHEMA"

  STAGE=toolchain_promotion
  rg_update_apply_toolchain "$WORK_DIR"

  STAGE=dispatch_unit_promotion
  install_dispatch_units "$ROLE" "$WORK_DIR"

  STAGE=toolchain_validation
  validate_trusted_toolchain_security
  rg_update_validate_toolchain

  STAGE=dispatch_activation
  activate_dispatch_units "$ROLE"

  STAGE=complete
  trap - ERR INT TERM
  log "status=completed role=$ROLE version=$RG_UPDATE_BUNDLE_VERSION commit=$RG_UPDATE_BUNDLE_COMMIT schema=${RG_UPDATE_EXPECTED_SCHEMA:-none} backup=$BACKUP_DIR"
}

main "$@"
