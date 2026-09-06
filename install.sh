#!/bin/sh
# aispace CLI installer.
#
#   curl -fsSL https://aispace.sh/install.sh | sh
#
# Environment:
#   AISPACE_VERSION   pin a release tag, e.g. "v1.2.3" or "1.2.3"
#                     (default: latest GitHub release)
#   AISPACE_INSTALL_DIR  install directory (default: /usr/local/bin, falling back
#                     to ~/.local/bin when not writable)
#   GITHUB_TOKEN      optional, raises GitHub API rate limits
#
# Downloads aispace_<os>_<arch>.tar.gz and checksums.txt from the GitHub release
# of aispace-sh/aispace-client, verifies the SHA-256, and installs the `aispace` binary.

set -eu

REPO="aispace-sh/aispace-client"
BIN="aispace"

log() { printf '%s\n' "$*" >&2; }
die() { log "error: $*"; exit 1; }

need() {
  command -v "$1" >/dev/null 2>&1 || die "required command not found: $1"
}

detect_os() {
  os=$(uname -s 2>/dev/null || echo unknown)
  case "$os" in
    Darwin) echo darwin ;;
    Linux) echo linux ;;
    *) die "unsupported OS: $os (supported: darwin, linux)" ;;
  esac
}

detect_arch() {
  arch=$(uname -m 2>/dev/null || echo unknown)
  case "$arch" in
    x86_64|amd64) echo amd64 ;;
    arm64|aarch64) echo arm64 ;;
    *) die "unsupported architecture: $arch (supported: amd64, arm64)" ;;
  esac
}

# fetch URL [output]  — downloads with curl or wget; prints to stdout without output arg.
fetch() {
  url=$1
  out=${2:-}
  auth=""
  if [ -n "${GITHUB_TOKEN:-}" ]; then
    case "$url" in
      https://api.github.com/*) auth="Authorization: Bearer $GITHUB_TOKEN" ;;
    esac
  fi
  if command -v curl >/dev/null 2>&1; then
    if [ -n "$out" ]; then
      if [ -n "$auth" ]; then curl -fsSL -H "$auth" -o "$out" "$url"; else curl -fsSL -o "$out" "$url"; fi
    else
      if [ -n "$auth" ]; then curl -fsSL -H "$auth" "$url"; else curl -fsSL "$url"; fi
    fi
  elif command -v wget >/dev/null 2>&1; then
    if [ -n "$out" ]; then
      if [ -n "$auth" ]; then wget -q --header="$auth" -O "$out" "$url"; else wget -q -O "$out" "$url"; fi
    else
      if [ -n "$auth" ]; then wget -q --header="$auth" -O - "$url"; else wget -q -O - "$url"; fi
    fi
  else
    die "need curl or wget"
  fi
}

# Resolve the release tag.
resolve_tag() {
  v=${AISPACE_VERSION:-}
  if [ -n "$v" ]; then
    case "$v" in
      v*) echo "$v" ;;
      *) echo "v$v" ;;
    esac
    return
  fi
  tag=$(fetch "https://api.github.com/repos/$REPO/releases/latest" | grep -o '"tag_name": *"v[^"]*"' | head -n1 | sed 's/.*"\(v[^"]*\)"/\1/')
  [ -n "$tag" ] || die "no v* release found for $REPO; set AISPACE_VERSION to pin one"
  echo "$tag"
}

sha256_of() {
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$1" | awk '{print $1}'
  elif command -v shasum >/dev/null 2>&1; then
    shasum -a 256 "$1" | awk '{print $1}'
  elif command -v openssl >/dev/null 2>&1; then
    openssl dgst -sha256 "$1" | awk '{print $NF}'
  else
    die "need sha256sum, shasum or openssl to verify the download"
  fi
}

pick_install_dir() {
  if [ -n "${AISPACE_INSTALL_DIR:-}" ]; then
    mkdir -p "$AISPACE_INSTALL_DIR" || die "cannot create $AISPACE_INSTALL_DIR"
    echo "$AISPACE_INSTALL_DIR"
    return
  fi
  if [ -d /usr/local/bin ] && [ -w /usr/local/bin ]; then
    echo /usr/local/bin
    return
  fi
  mkdir -p "$HOME/.local/bin" || die "cannot create $HOME/.local/bin"
  echo "$HOME/.local/bin"
}

main() {
  need uname
  need tar
  need grep
  need sed
  need awk

  os=$(detect_os)
  arch=$(detect_arch)
  tag=$(resolve_tag)
  version=${tag#v}
  asset="${BIN}_${os}_${arch}.tar.gz"
  base="https://github.com/$REPO/releases/download/v${version}"

  log "installing aispace v${version} (${os}/${arch})"

  tmp=$(mktemp -d 2>/dev/null || mktemp -d -t aispace)
  trap 'rm -rf "$tmp"' EXIT INT TERM

  fetch "$base/$asset" "$tmp/$asset" || die "download failed: $base/$asset"
  fetch "$base/checksums.txt" "$tmp/checksums.txt" || die "download failed: $base/checksums.txt"

  expected=$(grep " ${asset}\$" "$tmp/checksums.txt" | awk '{print $1}' | head -n1)
  [ -n "$expected" ] || die "no checksum for $asset in checksums.txt"
  actual=$(sha256_of "$tmp/$asset")
  if [ "$expected" != "$actual" ]; then
    die "checksum mismatch for $asset
  expected: $expected
  actual:   $actual"
  fi
  log "checksum verified"

  tar -xzf "$tmp/$asset" -C "$tmp" || die "cannot extract $asset"
  [ -f "$tmp/$BIN" ] || die "archive did not contain $BIN"
  chmod 0755 "$tmp/$BIN"

  dir=$(pick_install_dir)
  if [ -w "$dir" ]; then
    mv "$tmp/$BIN" "$dir/$BIN"
  else
    log "need sudo to write to $dir"
    sudo mv "$tmp/$BIN" "$dir/$BIN"
  fi
  log "installed $dir/$BIN"

  case ":${PATH}:" in
    *":$dir:"*) ;;
    *) log "note: $dir is not on your PATH; add it, e.g.:  export PATH=\"$dir:\$PATH\"" ;;
  esac

  log ""
  log "next: create a bot key in the aispace dashboard, then run"
  log "  aispace login --key ask_..."
}

main "$@"
