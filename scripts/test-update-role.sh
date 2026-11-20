#!/usr/bin/env bash

set -Eeuo pipefail
IFS=$'\n\t'

ROOT_DIR=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
# shellcheck source=scripts/routegate-update-core.sh
source "$ROOT_DIR/scripts/routegate-update-core.sh"
# shellcheck source=scripts/routegate-update-role.sh
source "$ROOT_DIR/scripts/routegate-update-role.sh"

TMP_DIR=$(mktemp -d)
trap 'rm -rf "$TMP_DIR"' EXIT

fail() {
  printf 'test-update-role: %s\n' "$*" >&2
  exit 1
}

assert_eq() {
  local actual=$1
  local expected=$2
  local label=$3
  [[ "$actual" == "$expected" ]] || fail "$label: got '$actual', want '$expected'"
}

assert_file_content() {
  local path=$1
  local expected=$2
  local actual
  actual=$(cat "$path")
  assert_eq "$actual" "$expected" "$path"
}

install_stubs() {
  local stub_dir=$1
  mkdir -p "$stub_dir"

  cat >"$stub_dir/systemctl" <<'EOF_SYSTEMCTL'
#!/usr/bin/env bash
set -euo pipefail
action=${1:-}
if [[ -n ${RG_TEST_SYSTEMCTL_FAIL_ACTION:-} && "$action" == "$RG_TEST_SYSTEMCTL_FAIL_ACTION" ]]; then
  exit 73
fi
case "$action" in
  is-active) printf 'active\n'; exit 0 ;;
  *) exit 0 ;;
esac
EOF_SYSTEMCTL

  cat >"$stub_dir/chown" <<'EOF_CHOWN'
#!/usr/bin/env bash
exit 0
EOF_CHOWN

  cat >"$stub_dir/pg_dump" <<'EOF_PG_DUMP'
#!/usr/bin/env bash
set -euo pipefail
[[ ${RG_TEST_PG_DUMP_FAIL:-0} != 1 ]] || exit 74
for arg in "$@"; do
  case "$arg" in
    --file=*) printf 'db-backup\n' >"${arg#--file=}"; exit 0 ;;
  esac
done
exit 2
EOF_PG_DUMP

  cat >"$stub_dir/pg_restore" <<'EOF_PG_RESTORE'
#!/usr/bin/env bash
set -euo pipefail
[[ " $* " == *" --single-transaction "* ]] || { echo "non-atomic database restore" >&2; exit 76; }
[[ ${RG_TEST_PG_RESTORE_FAIL:-0} != 1 ]] || exit 75
exit 0
EOF_PG_RESTORE

  chmod 0755 "$stub_dir"/*
}

populate_management() {
  local root=$1
  mkdir -p \
    "$root/usr/local/bin" \
    "$root/opt/routegate-manager/migrations" \
    "$root/var/www/routegate" \
    "$root/etc/systemd/system" \
    "$root/etc/routegate"
  printf 'manager-old\n' >"$root/usr/local/bin/routegate-manager"
  printf 'migration-old\n' >"$root/opt/routegate-manager/migrations/000133_old.up.sql"
  printf 'frontend-old\n' >"$root/var/www/routegate/index.html"
  printf 'manager-unit-old\n' >"$root/etc/systemd/system/routegate-manager.service"
  printf 'ROUTEGATE_DATABASE_URL="postgres://routegate:test@127.0.0.1/routegate"\n' >"$root/etc/routegate/manager.env"
}

populate_vpn() {
  local root=$1
  mkdir -p "$root/usr/local/bin" "$root/etc/systemd/system" "$root/etc/routegate"
  printf 'agent-old\n' >"$root/usr/local/bin/routegate-agent"
  printf 'agent-unit-old\n' >"$root/etc/systemd/system/routegate-agent.service"
  printf 'manager_url: "https://manager.example"\nagent_token: "rg_agent_example"\n' >"$root/etc/routegate/agent.yaml"
}

make_work_dir() {
  local work=$1
  mkdir -p "$work/bin" "$work/manager/migrations" "$work/frontend" "$work/systemd"
  printf 'manager-new\n' >"$work/bin/routegate-manager"
  printf 'migrator-new\n' >"$work/bin/routegate-migrate"
  printf 'agent-new\n' >"$work/bin/routegate-agent"
  printf 'migration-new\n' >"$work/manager/migrations/000134_new.up.sql"
  printf 'frontend-new\n' >"$work/frontend/index.html"
  printf 'manager-unit-new\n' >"$work/systemd/routegate-manager.service"
  printf 'agent-unit-new\n' >"$work/systemd/routegate-agent.service"
}

test_role_inference() {
  local management="$TMP_DIR/infer-management"
  local vpn="$TMP_DIR/infer-vpn"
  local hybrid="$TMP_DIR/infer-hybrid"
  local partial="$TMP_DIR/infer-partial"

  populate_management "$management"
  RG_UPDATE_ROOT=$management
  assert_eq "$(rg_update_resolve_role auto)" management "Management role inference"

  populate_vpn "$vpn"
  RG_UPDATE_ROOT=$vpn
  assert_eq "$(rg_update_resolve_role auto)" vpn "VPN role inference"

  populate_management "$hybrid"
  populate_vpn "$hybrid"
  RG_UPDATE_ROOT=$hybrid
  assert_eq "$(rg_update_resolve_role auto)" hybrid "Hybrid role inference"

  mkdir -p "$partial/usr/local/bin"
  printf 'partial\n' >"$partial/usr/local/bin/routegate-manager"
  RG_UPDATE_ROOT=$partial
  if rg_update_resolve_role auto >/dev/null 2>&1; then
    fail "partial Management layout unexpectedly inferred a role"
  fi

  RG_UPDATE_ROOT=""
}

test_marker_policy() {
  local root="$TMP_DIR/marker"
  populate_management "$root"
  mkdir -p "$root/etc/routegate"
  printf 'management\n' >"$root/etc/routegate/node-role"
  RG_UPDATE_ROOT=$root

  assert_eq "$(rg_update_resolve_role auto)" management "marker auto role"
  if rg_update_resolve_role vpn >/dev/null 2>&1; then
    fail "explicit role mismatch unexpectedly passed"
  fi

  printf 'root\n' >"$root/etc/routegate/node-role"
  if rg_update_resolve_role auto >/dev/null 2>&1; then
    fail "invalid role marker unexpectedly passed"
  fi

  RG_UPDATE_ROOT=""
}

test_manager_env_is_data() {
  local root="$TMP_DIR/env-data"
  local marker="$TMP_DIR/env-executed"
  local value
  populate_management "$root"
  printf 'ROUTEGATE_DATABASE_URL="%s"\n' "\$(touch $marker)" >"$root/etc/routegate/manager.env"
  RG_UPDATE_ROOT=$root

  value=$(rg_update_read_manager_database_url)
  assert_eq "$value" "\$(touch $marker)" "database URL data parsing"
  [[ ! -e "$marker" ]] || fail "Manager environment was evaluated as shell code"

  RG_UPDATE_ROOT=""
}

test_runtime_verifier_invocation() {
  local root="$TMP_DIR/runtime-verifier" file binary
  mkdir -p "$root/etc/routegate" "$root/usr/local/bin" "$root/opt/routegate-manager"
  file="$root/etc/routegate/manager.env"
  binary="$root/usr/local/bin/routegate-migrate"
  cat >"$binary" <<'EOF_RUNTIME_CHECK'
#!/bin/bash
[[ $# == 1 && $1 == --check-runtime ]] || exit 2
[[ ${ROUTEGATE_DATABASE_URL:-} == postgres://runtime/test ]] || exit 3
[[ -z ${ROUTEGATE_MIGRATOR_DATABASE_URL:-} && -z ${PGPASSWORD:-} ]] || exit 4
[[ $PWD == */opt/routegate-manager ]] || exit 5
exit 23
EOF_RUNTIME_CHECK
  chmod 0755 "$binary"
  printf '  ROUTEGATE_DATABASE_MIGRATION_MODE=external\nROUTEGATE_DATABASE_URL="postgres://runtime/test"\n' >"$file"
  (
    export ROUTEGATE_MIGRATOR_DATABASE_URL=owner-secret PGPASSWORD=owner-secret
    RG_UPDATE_ROOT=$root
    local rc=0
    rg_update_verify_restored_runtime "$file" || rc=$?
    assert_eq "$rc" 23 "external verifier invoked with isolated environment"
    printf 'ROUTEGATE_DATABASE_MIGRATION_MODE=startup\n' >>"$file"
    if rg_update_verify_restored_runtime "$file"; then
      fail "duplicate external/startup assignment bypassed verification"
    fi
    printf 'ROUTEGATE_DATABASE_URL=test\n' >"$file"
    rg_update_verify_restored_runtime "$file" || fail "legacy startup restore rejected"
  )
}

test_management_round_trip() {
  local root="$TMP_DIR/management-root"
  local work="$TMP_DIR/management-work"
  local backup="$TMP_DIR/management-backups/one"
  local db_url
  populate_management "$root"
  make_work_dir "$work"
  RG_UPDATE_ROOT=$root

  db_url=$(rg_update_read_manager_database_url)
  rg_update_create_role_backup management "$backup" "$db_url"
  rg_update_apply_role_files management "$work"

  assert_file_content "$root/usr/local/bin/routegate-manager" manager-new
  assert_file_content "$root/usr/local/bin/routegate-migrate" migrator-new
  assert_file_content "$backup/migrator.state" absent
  assert_file_content "$root/var/www/routegate/index.html" frontend-new
  [[ ! -e "$root/usr/local/bin/routegate-agent" ]] || fail "Management update created an Agent binary"

  export RG_TEST_SYSTEMCTL_FAIL_ACTION=stop
  if rg_update_restore_role_backup management "$backup" "$db_url" 1 >/dev/null 2>&1; then
    fail "Management restore continued after stop failure"
  fi
  unset RG_TEST_SYSTEMCTL_FAIL_ACTION
  assert_file_content "$root/usr/local/bin/routegate-manager" manager-new
  (
    rg_update_secure_manager_migrations() { return 1; }
    systemctl() {
      [[ ${1:-} != start ]] || fail "Management restarted with unprotected migrations"
      command systemctl "$@"
    }
    if rg_update_restore_role_backup management "$backup" "$db_url" 1 >/dev/null 2>&1; then
      fail "Management restore accepted unprotected migrations"
    fi
  )
  rg_update_restore_role_backup management "$backup" "$db_url" 1
  assert_file_content "$root/usr/local/bin/routegate-manager" manager-old
  assert_file_content "$root/opt/routegate-manager/migrations/000133_old.up.sql" migration-old
  assert_file_content "$root/var/www/routegate/index.html" frontend-old
  assert_file_content "$root/etc/systemd/system/routegate-manager.service" manager-unit-old
  assert_eq "$RG_UPDATE_DB_RESTORE_RC" 0 "Management database restore result"
  [[ ! -e "$root/usr/local/bin/routegate-migrate" ]] || fail "legacy rollback retained newer migrator"
  # A successful file rollback must not hide a failed database restoration.
  export RG_TEST_PG_RESTORE_FAIL=1
  if rg_update_restore_role_backup management "$backup" "$db_url" 1 >/dev/null 2>&1; then
    fail "Management rollback reported success after pg_restore failure"
  fi
  unset RG_TEST_PG_RESTORE_FAIL
  assert_eq "$RG_UPDATE_DB_RESTORE_RC" 75 "Management pg_restore failure preserved"
  assert_file_content "$root/usr/local/bin/routegate-manager" manager-old
  mv "$backup/routegate.pgdump" "$backup/saved.pgdump"
  if rg_update_restore_role_backup management "$backup" "$db_url" 1 >/dev/null 2>&1; then
    fail "Management rollback reported success without requested database backup"
  fi
  assert_eq "$RG_UPDATE_DB_RESTORE_RC" 1 "Management missing database backup"
  mv "$backup/saved.pgdump" "$backup/routegate.pgdump"
  (
    rg_update_verify_restored_runtime() { return 1; }
    systemctl() {
      [[ ${1:-} != start ]] || fail "Manager started after runtime verification failure"
      command systemctl "$@"
    }
    if rg_update_restore_role_backup management "$backup" "$db_url" 0 >/dev/null 2>&1; then
      fail "restore ignored runtime verification failure"
    fi
  )
  # Backups made by previous versions have neither state nor binary.
  rm "$backup/migrator.state"
  printf 'newer\n' >"$root/usr/local/bin/routegate-migrate"
  rg_update_restore_role_backup management "$backup" "$db_url" 0
  [[ ! -e "$root/usr/local/bin/routegate-migrate" ]] || fail "old-format rollback retained migrator"

  RG_UPDATE_ROOT=""
}

test_vpn_round_trip() {
  local root="$TMP_DIR/vpn-root"
  local work="$TMP_DIR/vpn-work"
  local backup="$TMP_DIR/vpn-backups/one"
  populate_vpn "$root"
  make_work_dir "$work"
  RG_UPDATE_ROOT=$root

  rg_update_create_role_backup vpn "$backup"
  rg_update_apply_role_files vpn "$work"
  assert_file_content "$root/usr/local/bin/routegate-agent" agent-new
  assert_file_content "$root/etc/systemd/system/routegate-agent.service" agent-unit-new
  [[ ! -e "$root/usr/local/bin/routegate-manager" ]] || fail "VPN update created a Manager binary"
  [[ ! -e "$root/usr/local/bin/routegate-migrate" ]] || fail "VPN update created a migrator"

  rg_update_restore_role_backup vpn "$backup"
  assert_file_content "$root/usr/local/bin/routegate-agent" agent-old
  assert_file_content "$root/etc/systemd/system/routegate-agent.service" agent-unit-old
  assert_file_content "$root/etc/routegate/agent.yaml" 'manager_url: "https://manager.example"
agent_token: "rg_agent_example"'

  RG_UPDATE_ROOT=""
}

test_backup_failure_propagates() {
  local root="$TMP_DIR/backup-failure-root"
  local backup="$TMP_DIR/backup-failure/one"
  local db_url
  populate_management "$root"
  RG_UPDATE_ROOT=$root
  db_url=$(rg_update_read_manager_database_url)

  export RG_TEST_PG_DUMP_FAIL=1
  if rg_update_create_role_backup management "$backup" "$db_url" >/dev/null 2>&1; then
    fail "failed pg_dump unexpectedly produced a successful Management backup"
  fi
  unset RG_TEST_PG_DUMP_FAIL

  RG_UPDATE_ROOT=""
}

test_rollback_failure_is_reported_after_best_effort_restore() {
  local root="$TMP_DIR/rollback-failure-root"
  local work="$TMP_DIR/rollback-failure-work"
  local backup="$TMP_DIR/rollback-failure-backups/one"
  populate_vpn "$root"
  make_work_dir "$work"
  RG_UPDATE_ROOT=$root

  rg_update_create_role_backup vpn "$backup"
  rg_update_apply_role_files vpn "$work"
  assert_file_content "$root/usr/local/bin/routegate-agent" agent-new

  export RG_TEST_SYSTEMCTL_FAIL_ACTION=daemon-reload
  if rg_update_restore_role_backup vpn "$backup" >/dev/null 2>&1; then
    fail "rollback with failed daemon-reload unexpectedly returned success"
  fi
  unset RG_TEST_SYSTEMCTL_FAIL_ACTION

  assert_file_content "$root/usr/local/bin/routegate-agent" agent-old
  assert_file_content "$root/etc/systemd/system/routegate-agent.service" agent-unit-old
  RG_UPDATE_ROOT=""
}

test_management_apply_preserves_bootstrap() {
  local root="$TMP_DIR/management-bootstrap"
  local work="$TMP_DIR/management-bootstrap-work"

  populate_management "$root"
  mkdir -p "$root/var/www/routegate/bootstrap/oldcommit" "$root/var/www/routegate/assets"
  printf 'installer\n' >"$root/var/www/routegate/bootstrap/oldcommit/install-agent.sh"
  printf 'asset-old\n' >"$root/var/www/routegate/assets/old.js"
  make_work_dir "$work"
  RG_UPDATE_ROOT=$root

  rg_update_apply_management_files "$work" >/dev/null
  assert_file_content "$root/var/www/routegate/index.html" frontend-new
  assert_file_content "$root/var/www/routegate/bootstrap/oldcommit/install-agent.sh" installer
  [[ ! -e "$root/var/www/routegate/assets/old.js" ]] || fail "stale frontend asset survived the Management apply"

  rm -rf "$root/var/www/routegate/bootstrap"
  ln -s "$TMP_DIR" "$root/var/www/routegate/bootstrap"
  if rg_update_apply_management_files "$work" >/dev/null 2>&1; then
    fail "Management apply accepted a symlinked bootstrap directory"
  fi
  RG_UPDATE_ROOT=""
}

STUB_DIR="$TMP_DIR/stubs"
install_stubs "$STUB_DIR"
PATH="$STUB_DIR:$PATH"

test_role_inference
test_marker_policy
test_manager_env_is_data
test_runtime_verifier_invocation
test_management_round_trip
test_management_apply_preserves_bootstrap
test_vpn_round_trip
test_backup_failure_propagates
test_rollback_failure_is_reported_after_best_effort_restore

printf 'RouteGate role-aware update tests passed.\n'
