#!/usr/bin/env bash
# Re-trigger release.yml for an existing tag by deleting + re-pushing it.
# Use after a GitHub Actions outage causes release.yml to miss the
# original tag-push event.
#
# Usage:
#   scripts/retrigger-release.sh v0.4.17
#
# Safe to run repeatedly — the workflow rebuilds from the same commit
# the tag points at, so the produced artefacts are byte-identical
# (apart from BUILD_DATE) and softprops/action-gh-release overwrites
# the existing files on the release.
set -euo pipefail
tag="${1:?usage: retrigger-release.sh <tag>}"
if ! git rev-parse "$tag" >/dev/null 2>&1; then
  echo "no local tag $tag" >&2
  exit 1
fi
echo "deleting $tag on origin..."
git push origin ":refs/tags/$tag"
sleep 3
echo "re-pushing $tag (will trigger release.yml)..."
git push origin "$tag"
echo
echo "watch with:"
echo "  gh run watch \$(gh run list --workflow=release.yml --limit 1 --json databaseId --jq '.[0].databaseId')"
