#!/usr/bin/env bash
# Run a statically linked Manager as an unprivileged service identity in a
# disposable chroot. Invalid DB configuration identifies the pre-connect boundary.
set -euo pipefail
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
chmod 0755 "$work"
install -m 0755 "$1" "$work/manager"
mkdir -p "$work/var/lib"
latch="$work/var/lib/routegate-credential-mode"
run_case() {
  local expected=$1 output rc=0
  output=$(ROUTEGATE_DATABASE_URL='postgres://%' chroot --userspec=65534:65534 "$work" /manager 2>&1) || rc=$?
  if [[ $rc != 1 || $output != *"$expected"* ]]; then
    printf 'expected %s, rc=%d, got: %s\n' "$expected" "$rc" "$output" >&2
    return 1
  fi
  if [[ "$expected" != 'cannot parse' && "$output" == *'cannot parse'* ]]; then
    echo 'unexpected database configuration access' >&2
    return 1
  fi
}
run_case 'cannot parse'
mkdir -m 0700 "$latch"
mkdir "$latch/conversion.pending"
run_case 'permission denied'
ROUTEGATE_DATABASE_MIGRATION_MODE=external run_case 'permission denied'
chmod 0711 "$latch"
run_case 'credential conversion pending'
rmdir "$latch/conversion.pending"
ln -s missing "$latch/conversion.pending"
run_case 'credential conversion pending'
rm "$latch/conversion.pending"
touch "$latch/conversion.pending"
run_case 'credential conversion pending'
rm "$latch/conversion.pending"
run_case 'inspect legacy credential restore pin'
printf 'FORMAT_VERSION=1\nMODE=external\nENV_SHA256=%064d\nUNIT_SHA256=%064d\nBINARY_SHA256=%064d\n' 0 0 0 >"$latch/restore.pin"
chmod 0600 "$latch/restore.pin"
run_case 'cannot parse'
chmod 0700 "$latch"
run_case 'permission denied'
chmod 0711 "$latch"
mkdir "$latch/conversion.pending"
run_case 'credential conversion pending'
rm -r "$latch"
ln -s missing "$latch"
run_case 'invalid credential conversion directory'
rm "$latch"
python3 "$(dirname "$0")/exercise-manager-publication-race.py" "$work"
run_case 'permission denied'
echo 'Manager startup latch checks passed'
