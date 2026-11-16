#!/usr/bin/env bash
# Hermetic local-Git release workflow policy tests. Never writes to GitHub.
set -Eeuo pipefail
IFS=$'\n\t'
umask 077
ROOT=$(cd "$(dirname "$0")/.." && pwd)
TEMP=$(mktemp -d)
trap 'rm -rf "$TEMP"' EXIT

git init -q --bare "$TEMP/origin.git"
git init -q -b main "$TEMP/work"
git -C "$TEMP/work" config user.name "RouteGate CI"
git -C "$TEMP/work" config user.email "routegate-ci@example.invalid"
git -C "$TEMP/work" remote add origin "$TEMP/origin.git"
echo first >"$TEMP/work/test.txt"
git -C "$TEMP/work" add test.txt
git -C "$TEMP/work" commit -qm first
first=$(git -C "$TEMP/work" rev-parse HEAD)
git -C "$TEMP/work" push -q origin main

accept() {
  (cd "$TEMP/work" && bash "$ROOT/scripts/check-manual-release-source.sh" "$@") >"$TEMP/check.log" 2>&1 || {
    echo "[manual-release-test] Expected acceptance for $4" >&2
    exit 1
  }
}
reject() {
  if (cd "$TEMP/work" && bash "$ROOT/scripts/check-manual-release-source.sh" "$@") >"$TEMP/check.log" 2>&1; then
    echo "[manual-release-test] Unexpected acceptance for $1/$2/$4" >&2
    exit 1
  fi
}
accept workflow_dispatch refs/heads/main "$first" dev
accept workflow_dispatch refs/heads/main "$first" dev-rc-check_12
reject workflow_dispatch refs/heads/feature "$first" dev
reject push refs/heads/main "$first" dev
reject workflow_dispatch refs/heads/main "$first" v1.2.3
reject workflow_dispatch refs/heads/main "$first" dev-../../escape
reject workflow_dispatch refs/heads/main "$first" 'dev
bad-output=true'
reject workflow_dispatch refs/heads/main "$first" shortsha
reject workflow_dispatch refs/heads/main "$first" 'dev-AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA'

# Main is modified remotely but checkout remains on the first commit.
# A local origin/main cache can remain stale; live ls-remote must detect it.
git -C "$TEMP/work" worktree add -q -b scratch "$TEMP/next" "$first"
echo second >>"$TEMP/next/test.txt"
git -C "$TEMP/next" add test.txt
git -C "$TEMP/next" commit -qm second
second=$(git -C "$TEMP/next" rev-parse HEAD)
git -C "$TEMP/next" push -q origin "$second:refs/heads/main"
reject workflow_dispatch refs/heads/main "$first" dev

# Sync and check out the new main: the same policy should accept it.
git -C "$TEMP/work" checkout -q --detach "$second"
accept workflow_dispatch refs/heads/main "$second" dev

# A deleted branch is never treated as sufficient just because the local
# current checkout has a matching SHA.
git -C "$TEMP/next" push -q origin :refs/heads/main
reject workflow_dispatch refs/heads/main "$second" dev
printf '[manual-release-test] PASS: main-only, safe version, current SHA, moved/deleted branch, injected inputs.\n'
