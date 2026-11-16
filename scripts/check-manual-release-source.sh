#!/usr/bin/env bash
# Verify a manual release-artifact rehearsal uses the exact current main HEAD
# and a bounded dev-only label. No tags, API writes, or releases are created.
set -Eeuo pipefail
IFS=$'\n\t'

die() { printf '[RouteGate manual release] ERROR: %s\n' "$*" >&2; exit 1; }
[[ $# == 4 ]] || die "Usage: check-manual-release-source.sh EVENT REF SHA VERSION"
event=$1
ref=$2
expected=$3
version=$4

[[ "$event" == workflow_dispatch ]] || die "This check accepts workflow_dispatch only"
[[ "$ref" == refs/heads/main ]] ||
  die "Manual release rehearsal must run from canonical refs/heads/main"
[[ "$expected" =~ ^[a-f0-9]{40}$ ]] ||
  die "Expected checkout commit must be a full SHA"
# Manual artifacts must never look like published stable versions or use
# arbitrary text inside GITHUB_OUTPUT, filenames or attestation subject names.
[[ "$version" =~ ^dev([._-][a-z0-9][a-z0-9._-]{0,31})?$ ]] ||
  die "Manual release version must be dev or a bounded dev-prefixed label"

checkout=$(git rev-parse --verify HEAD) ||
  die "Cannot resolve checked-out source commit"
[[ "$checkout" == "$expected" ]] ||
  die "Checkout does not match the workflow event source commit"

# Consult the live remote main ref, not a stale local refs/remotes/origin/main.
# An unavailable/missing branch must fail closed.
lines=$(GIT_TERMINAL_PROMPT=0 git ls-remote --exit-code --heads origin refs/heads/main) ||
  die "Cannot resolve current origin/main"
remote=$(printf '%s\n' "$lines" |
  awk '$2 == "refs/heads/main" {print $1}')
[[ "$remote" =~ ^[a-f0-9]{40}$ ]] ||
  die "Canonical remote main ref missing or malformed"
[[ "$remote" == "$expected" ]] ||
  die "Canonical main moved or manual dispatch used a stale/non-main commit"

printf '[RouteGate manual release] Verified current main commit and safe dev label.\n'
