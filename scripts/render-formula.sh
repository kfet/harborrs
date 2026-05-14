#!/usr/bin/env bash
# Render packaging/homebrew/harborrs.rb.tmpl into a concrete Formula by
# substituting VERSION + per-platform SHA256s from dist/checksums.txt.
#
# Usage: scripts/render-formula.sh <VERSION> <CHECKSUMS_FILE> <OUT_FILE>
#
# VERSION is the bare semver (no leading "v").
set -euo pipefail

VERSION="${1:?version required}"
SUMS="${2:?checksums.txt required}"
OUT="${3:?output path required}"

repo_root=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)
tmpl="${repo_root}/packaging/homebrew/harborrs.rb.tmpl"
[ -f "$tmpl" ] || { echo "missing template: $tmpl" >&2; exit 1; }
[ -f "$SUMS" ] || { echo "missing checksums: $SUMS" >&2; exit 1; }

# Pull "<sha>  <file>" pairs and look up the sha for each known asset.
lookup() {
    local asset="harborrs-${VERSION}-$1.tar.gz"
    local sha
    sha=$(awk -v a="$asset" '$2==a {print $1}' "$SUMS")
    [ -n "$sha" ] || { echo "no checksum for $asset in $SUMS" >&2; exit 1; }
    printf '%s' "$sha"
}

SHA_DARWIN_ARM64=$(lookup darwin-arm64)
SHA_DARWIN_AMD64=$(lookup darwin-amd64)
SHA_LINUX_ARM64=$(lookup linux-arm64)
SHA_LINUX_AMD64=$(lookup linux-amd64)

sed \
    -e "s/__VERSION__/${VERSION}/g" \
    -e "s/__SHA_DARWIN_ARM64__/${SHA_DARWIN_ARM64}/g" \
    -e "s/__SHA_DARWIN_AMD64__/${SHA_DARWIN_AMD64}/g" \
    -e "s/__SHA_LINUX_ARM64__/${SHA_LINUX_ARM64}/g" \
    -e "s/__SHA_LINUX_AMD64__/${SHA_LINUX_AMD64}/g" \
    "$tmpl" > "$OUT"

echo "✓ rendered $OUT (harborrs $VERSION)"
