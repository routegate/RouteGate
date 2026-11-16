#!/usr/bin/env bash
# Hermetic Git-only tests. Uses a private local bare origin; never pushes a
# RouteGate tag, fetches a remote service or creates a GitHub Release.
set -Eeuo pipefail
IFS=$'\n\t'
umask 077
ROOT=$(cd "$(dirname "$0")/.." && pwd)
TEMP=$(mktemp -d)
trap 'rm -rf "$TEMP"' EXIT
git init -q --bare "$TEMP/origin.git"
git init -q "$TEMP/work"
git -C "$TEMP/work" config user.name "RouteGate CI"
git -C "$TEMP/work" config user.email "routegate-ci@example.invalid"
git -C "$TEMP/work" remote add origin "$TEMP/origin.git"
printf 'first\n' >"$TEMP/work/source.txt"
git -C "$TEMP/work" add source.txt
git -C "$TEMP/work" commit -qm "first"
first=$(git -C "$TEMP/work" rev-parse HEAD)

check_ok() {
  (cd "$TEMP/work" && bash "$ROOT/scripts/check-release-tag-identity.sh" "$1" "$2") >"$TEMP/check.log" 2>&1 || {
    echo "[tag-identity-test] Expected $1 to match tested commit" >&2
    exit 1
  }
}
check_reject() {
  if (cd "$TEMP/work" && bash "$ROOT/scripts/check-release-tag-identity.sh" "$1" "$2") >"$TEMP/check.log" 2>&1; then
    echo "[tag-identity-test] Unexpected acceptance for tag $1" >&2
    exit 1
  fi
}

git -C "$TEMP/work" tag v9.1.0 "$first"
git -C "$TEMP/work" push -q origin refs/tags/v9.1.0
check_ok v9.1.0 "$first"

git -C "$TEMP/work" tag -a v9.2.0 -m "annotated release" "$first"
git -C "$TEMP/work" push -q origin refs/tags/v9.2.0
check_ok v9.2.0 "$first"

printf 'second\n' >>"$TEMP/work/source.txt"
git -C "$TEMP/work" add source.txt
git -C "$TEMP/work" commit -qm "second"
second=$(git -C "$TEMP/work" rev-parse HEAD)
check_reject v9.2.0 "$second"    # valid annotated tag, wrong expected SHA
check_reject v9.1.0 "$second"    # valid lightweight tag, wrong expected SHA

# Repoint only the remote tag. The local ref can remain stale, proving that
# verification queries actual origin and never trusts the checkout's cache.
git -C "$TEMP/work" push -q --force origin "$second:refs/tags/v9.1.0"
check_reject v9.1.0 "$first"
grep -Fq 'tag v9.1.0 moved' "$TEMP/check.log"
check_ok v9.1.0 "$second"

git -C "$TEMP/work" push -q origin :refs/tags/v9.1.0
check_reject v9.1.0 "$second"    # deleted tag
check_reject v9.3.0 "$first"     # nonexistent tag
check_reject main "$first"       # invalid version/ref
check_reject v9.2.0 deadbeef    # invalid SHA
printf '[tag-identity-test] PASS: lightweight, annotated, moved, deleted, nonexistent and invalid tags.\n'
