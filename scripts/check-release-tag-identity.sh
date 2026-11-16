#!/usr/bin/env bash
# Verify live remote tag ref against the exact tested source commit.
# Handles both lightweight and annotated tags; changes no Git refs.
set -Eeuo pipefail
IFS=$'\n\t'

die() { printf '[RouteGate release] ERROR: %s\n' "$*" >&2; exit 1; }

[[ $# == 2 ]] || die "Usage: check-release-tag-identity.sh TAG EXPECTED_COMMIT_SHA"
tag=$1
expected=$2
[[ "$tag" =~ ^v[0-9]+\.[0-9]+\.[0-9]+(-rc\.[0-9]+)?$ ]] ||
  die "Release tag must match vX.Y.Z or vX.Y.Z-rc.N"
[[ "$expected" =~ ^[a-f0-9]{40}$ ]] ||
  die "Expected release commit must be a full Git SHA"

# Always read origin, not locally cached refs. The annotated form has the
# refs/tags/TAG^{} peeled commit. Missing/unreadable tags fail closed.
remote_lines=$(GIT_TERMINAL_PROMPT=0 git ls-remote --exit-code origin \
  "refs/tags/$tag" "refs/tags/$tag^{}") ||
  die "Cannot read tag $tag from origin (missing, deleted or network failure)"

tag_ref="refs/tags/$tag"
peeled_ref="refs/tags/$tag^{}"
tag_sha=$(printf '%s\n' "$remote_lines" | awk -v ref="$tag_ref" '$2 == ref { print $1 }')
peeled_sha=$(printf '%s\n' "$remote_lines" | awk -v ref="$peeled_ref" '$2 == ref { print $1 }')
[[ "$tag_sha" =~ ^[a-f0-9]{40}$ ]] || die "Remote tag $tag not found"
# Lightweight tags point at the commit; annotated tags expose a peeled sha.
actual="${peeled_sha:-$tag_sha}"
[[ "$actual" =~ ^[a-f0-9]{40}$ ]] || die "Remote tag $tag has an invalid peeled object"
[[ "$actual" == "$expected" ]] ||
  die "Release tag $tag moved: remote commit does not match tested source SHA. Refusing draft creation."

printf '[RouteGate release] Remote tag %s still matches tested commit %s.\n' "$tag" "$expected"
