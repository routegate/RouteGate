#!/usr/bin/env bash
set -Eeuo pipefail

ROOT_DIR=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
# shellcheck source=install.sh
source "$ROOT_DIR/install.sh"
# shellcheck source=scripts/routegate-update-core.sh
source "$ROOT_DIR/scripts/routegate-update-core.sh"
# shellcheck source=scripts/routegate-update-role.sh
source "$ROOT_DIR/scripts/routegate-update-role.sh"

[[ $EUID -eq 0 ]] || { printf 'Run this isolated fixture test as root.\n' >&2; exit 1; }
id nobody >/dev/null
TEST_ROOT=$(mktemp -d)
trap 'rm -rf -- "$TEST_ROOT"' EXIT
chmod 0755 "$TEST_ROOT"
# Mirror the installer's verified bundle; both adapters use the same helper.
ROUTEGATE_WORK_DIR="$TEST_ROOT/release"
mkdir -p "$ROUTEGATE_WORK_DIR/extracted/tools"
cp "$ROOT_DIR/scripts/routegate-update-core.sh" "$ROUTEGATE_WORK_DIR/extracted/tools/"

fail() { printf 'migration ownership: %s\n' "$*" >&2; exit 1; }
deny() {
  if runuser -u nobody -- "$@" >/dev/null 2>&1; then
    fail "runtime unexpectedly permitted: $*"
  fi
}
protect() {
  if [[ $mode == install ]]; then
    secure_manager_migration_tree "$parent"
  else
    rg_update_secure_manager_migrations
  fi
}

for mode in install update; do
  RG_UPDATE_ROOT="$TEST_ROOT/$mode"
  parent="$RG_UPDATE_ROOT/opt/routegate-manager"
  mkdir -p "$parent/migrations"
  printf 'SELECT 1;\n' >"$parent/migrations/000001_fixture.up.sql"
  chmod 0755 "$RG_UPDATE_ROOT" "$RG_UPDATE_ROOT/opt"
  chown -R nobody:nogroup "$parent"
  chmod -R 0777 "$parent"
  tar -czf "$TEST_ROOT/$mode-legacy.tar.gz" -C "$parent" migrations

  # A legacy environment override must not restore runtime ownership.
  export RG_UPDATE_MANAGER_OWNER=nobody:nogroup
  protect || fail "$mode protection failed"
  [[ $(stat -c '%u:%g:%a' "$parent") == 0:0:755 ]] || fail 'parent is not protected'
  [[ $(stat -c '%u:%g:%a' "$parent/migrations") == 0:0:755 ]] || fail 'directory is not protected'
  [[ $(stat -c '%u:%g:%a' "$parent/migrations/000001_fixture.up.sql") == 0:0:644 ]] || fail 'SQL is not protected'
  runuser -u nobody -- cat "$parent/migrations/000001_fixture.up.sql" >/dev/null
  deny sh -c 'printf corrupted >> "$1"' sh "$parent/migrations/000001_fixture.up.sql"
  deny touch "$parent/migrations/000002_forged.up.sql"
  deny mv "$parent/migrations" "$parent/replaced"
  deny chmod 0777 "$parent/migrations/000001_fixture.up.sql"

  # Restoring an old tar preserves its unsafe owner/modes; normalize again.
  tar -xzf "$TEST_ROOT/$mode-legacy.tar.gz" -C "$parent"
  [[ $(stat -c '%u' "$parent/migrations/000001_fixture.up.sql") != 0 ]] || fail 'fixture did not restore legacy ownership'
  protect || fail "$mode legacy restore protection failed"
  deny touch "$parent/migrations/000002_forged.up.sql"
  protect || fail "$mode protection is not idempotent"

  printf 'outside\n' >"$TEST_ROOT/outside-$mode"
  chown nobody:nogroup "$TEST_ROOT/outside-$mode"
  ln -s "$TEST_ROOT/outside-$mode" "$parent/migrations/link"
  if protect; then fail "$mode accepted a symlink"; fi
  rm "$parent/migrations/link"
  ln "$TEST_ROOT/outside-$mode" "$parent/migrations/hardlink"
  if protect; then fail "$mode accepted a hard link"; fi
  [[ $(stat -c '%u' "$TEST_ROOT/outside-$mode") != 0 ]] || fail 'changed outside ownership'
  rm "$parent/migrations/hardlink"
  mv "$parent/migrations" "$parent/saved"
  ln -s saved "$parent/migrations"
  if protect; then fail "$mode accepted a linked migration root"; fi
  rm "$parent/migrations"
  mv "$parent/saved" "$parent/migrations"
done

# Verify all existing write paths invoke protection; behavior is exercised above
# with real kernel permissions, and the full update suites exercise each path.
for fn in rg_update_apply_platform_files rg_update_restore_backup rg_update_apply_management_files rg_update_restore_management_backup; do
  declare -f "$fn" | grep -q 'rg_update_secure_manager_migrations' || fail "$fn lacks protection"
done
declare -f install_files | grep -q 'secure_manager_migration_tree' || fail 'installer lacks protection'
printf 'Manager migration ownership fixtures passed.\n'
