#!/bin/sh
# Install harb from a GitHub release.
#
# Usage:
#   curl -fsSL https://raw.githubusercontent.com/kfet/harb/main/install.sh | sh
#   curl -fsSL https://raw.githubusercontent.com/kfet/harb/main/install.sh | VERSION=v0.1.0 sh
#   curl -fsSL https://raw.githubusercontent.com/kfet/harb/main/install.sh | PREFIX=$HOME/.local sh
#
# Env:
#   VERSION   release tag to install (default: latest)
#   PREFIX    install prefix; binary lands in $PREFIX/bin (default: /usr/local, or $HOME/.local if not writable)
#   REPO      github owner/repo (default: kfet/harb)
#
# On macOS we recommend `brew install kfet/tap/harb` instead — it
# auto-updates with brew upgrade. This script is for Linux (Raspberry Pi etc.).
set -eu

REPO="${REPO:-kfet/harb}"
VERSION="${VERSION:-latest}"

die() { printf 'install: %s\n' "$*" >&2; exit 1; }
info() { printf '→ %s\n' "$*"; }

# --- detect OS/arch -----------------------------------------------------
uname_s="$(uname -s)"
uname_m="$(uname -m)"
case "${uname_s}" in
    Linux)   os=linux ;;
    Darwin)  os=darwin ;;
    *)       die "unsupported OS: ${uname_s}" ;;
esac
case "${uname_m}" in
    x86_64|amd64)   arch=amd64 ;;
    aarch64|arm64)  arch=arm64 ;;
    armv6l|armv7l) arch=armv6 ;;
    *)              die "unsupported arch: ${uname_m}" ;;
esac

# --- resolve version ----------------------------------------------------
if [ "${VERSION}" = "latest" ]; then
    info "resolving latest release of ${REPO}…"
    # Use the API redirect rather than parsing JSON.
    VERSION="$(curl -fsSL -o /dev/null -w '%{url_effective}' \
        "https://github.com/${REPO}/releases/latest" \
        | sed 's|.*/||')"
    [ -n "${VERSION}" ] || die "could not resolve latest version"
fi
ver_no_v="${VERSION#v}"
asset="harb-${ver_no_v}-${os}-${arch}.tar.gz"
base="https://github.com/${REPO}/releases/download/${VERSION}"

# --- prefix -------------------------------------------------------------
if [ -z "${PREFIX:-}" ]; then
    if [ -w /usr/local/bin ] 2>/dev/null || [ "$(id -u)" = 0 ]; then
        PREFIX=/usr/local
    else
        PREFIX="${HOME}/.local"
    fi
fi
bindir="${PREFIX}/bin"
mkdir -p "${bindir}"

# --- download + verify --------------------------------------------------
tmp="$(mktemp -d)"
trap 'rm -rf "${tmp}"' EXIT

info "downloading ${asset}"
curl -fSL --progress-bar -o "${tmp}/${asset}" "${base}/${asset}" \
    || die "download failed: ${base}/${asset}"

info "fetching checksums.txt"
curl -fsSL -o "${tmp}/checksums.txt" "${base}/checksums.txt" \
    || die "could not fetch checksums.txt"

info "verifying sha256"
expected="$(awk -v n="${asset}" '$2 == n {print $1}' "${tmp}/checksums.txt")"
[ -n "${expected}" ] || die "no checksum entry for ${asset}"
if command -v sha256sum >/dev/null 2>&1; then
    actual="$(sha256sum "${tmp}/${asset}" | awk '{print $1}')"
elif command -v shasum >/dev/null 2>&1; then
    actual="$(shasum -a 256 "${tmp}/${asset}" | awk '{print $1}')"
else
    die "need sha256sum or shasum on PATH"
fi
[ "${expected}" = "${actual}" ] || die "checksum mismatch: expected ${expected}, got ${actual}"

# --- extract + install --------------------------------------------------
info "extracting"
tar -C "${tmp}" -xzf "${tmp}/${asset}"
src="${tmp}/harb-${ver_no_v}-${os}-${arch}/harb"
[ -x "${src}" ] || die "binary not found in archive"

info "installing to ${bindir}/harb"
install -m 0755 "${src}" "${bindir}/harb.new"
mv -f "${bindir}/harb.new" "${bindir}/harb"

# --- post-install -------------------------------------------------------
printf '\n✓ harb %s installed to %s/harb\n' "${VERSION}" "${bindir}"
case ":${PATH}:" in
    *":${bindir}:"*) ;;
    *) printf '  note: %s is not in your PATH yet. add it to your shell rc:\n        export PATH="%s:$PATH"\n' "${bindir}" "${bindir}" ;;
esac
printf '\nnext: harb init    # bootstrap config + password\n'
printf '      harb serve   # start the server\n'
