#!/bin/sh
# install.sh — install the astrix toolchain (astrix + astrixd).
# Prefers prebuilt GitHub release assets; falls back to `go install` when the
# assets are unavailable (e.g. not published yet). POSIX sh only (dash/ash/
# busybox/bash compatible); no bashisms.

set -eu

PROJECT="astrix"
REPO="vivek5200/astrix"
MODULE="github.com/vivek5200/astrix"
BINARIES="astrix astrixd"
VERSION_FLAG="--astrix-version"

DRY_RUN=0
BIN_DIR=""
VERSION=""          # empty or "latest" -> newest published release
TMP_DIR=""

info() { printf '%s\n' "info: $*"; }
warn() { printf '%s\n' "warning: $*" >&2; }
err()  { printf '%s\n' "error: $*" >&2; }

usage() {
  cat <<EOF
astrix installer — installs ${BINARIES}

Usage: install.sh [options]

Options:
  --help                Show this help and exit.
  --dry-run             Resolve and print the install plan; change nothing.
  --bin-dir <dir>       Install binaries into <dir> (default: /usr/local/bin
                        if it exists and is writable, otherwise
                        \$HOME/.local/bin).
  ${VERSION_FLAG} <tag> Install a specific release tag, e.g. v0.2.0-alpha
                        (default: latest release). "--version" is an alias.

Release assets are fetched from https://github.com/${REPO}/releases.
If the download fails (e.g. assets are not published yet) and a Go toolchain
is available (go >= 1.26 recommended), the installer falls back to:

  GOBIN=<bin dir> CGO_ENABLED=0 go install ${MODULE}/cmd/<bin>@<tag>
EOF
}

# fetch <url> <dest-file> — download with curl or wget; non-zero on failure.
fetch() {
  if command -v curl >/dev/null 2>&1; then
    curl -fsSL -o "$2" "$1"
  elif command -v wget >/dev/null 2>&1; then
    wget -q -O "$2" "$1"
  else
    warn "neither curl nor wget found on PATH"
    return 1
  fi
}

# ------------------------------------------------------------- arguments ---

while [ $# -gt 0 ]; do
  case "$1" in
    -h|--help) usage; exit 0 ;;
    --dry-run) DRY_RUN=1 ;;
    --bin-dir)
      [ $# -ge 2 ] || { err "--bin-dir requires a directory argument"; exit 1; }
      BIN_DIR="$2"; shift ;;
    --bin-dir=*) BIN_DIR="${1#*=}" ;;
    --astrix-version|--version)
      [ $# -ge 2 ] || { err "$1 requires a version/tag argument"; exit 1; }
      VERSION="$2"; shift ;;
    --astrix-version=*|--version=*) VERSION="${1#*=}" ;;
    *)
      err "unknown option: $1"
      usage >&2
      exit 1
      ;;
  esac
  shift
done

# ------------------------------------------------------ platform detection ---

OS="$(uname -s)"
case "$OS" in
  Linux)  OS="linux" ;;
  Darwin) OS="darwin" ;;
  *) err "unsupported operating system: $OS (this installer supports Linux and macOS)"; exit 1 ;;
esac

ARCH="$(uname -m)"
case "$ARCH" in
  x86_64|amd64)  ARCH="amd64" ;;
  aarch64|arm64) ARCH="arm64" ;;
  *) err "unsupported CPU architecture: $ARCH (supported: amd64, arm64)"; exit 1 ;;
esac

# Bin dir: explicit --bin-dir wins; else a writable /usr/local/bin; else the
# user-local bin dir (created on install, never in a dry run).
if [ -z "$BIN_DIR" ] && [ -d /usr/local/bin ] && [ -w /usr/local/bin ]; then
  BIN_DIR="/usr/local/bin"
fi
if [ -z "$BIN_DIR" ]; then
  if [ -z "${HOME:-}" ]; then
    err "HOME is unset; pass --bin-dir or set HOME"
    exit 1
  fi
  BIN_DIR="${HOME}/.local/bin"
fi

# ------------------------------------------------------- version and URLs ---

if [ -z "$VERSION" ] || [ "$VERSION" = "latest" ]; then
  VERSION="latest"
  # "latest/download" redirects to the newest release's assets: no API calls.
  BASE_URL="https://github.com/${REPO}/releases/latest/download"
  GO_REF="latest"
else
  BASE_URL="https://github.com/${REPO}/releases/download/${VERSION}"
  GO_REF="$VERSION"
fi

ARCHIVE="${PROJECT}_${OS}_${ARCH}.tar.gz"
ARCHIVE_URL="${BASE_URL}/${ARCHIVE}"
CHECKSUMS_URL="${BASE_URL}/checksums.txt"

# -------------------------------------------------------------- dry run -----

if [ "$DRY_RUN" -eq 1 ]; then
  echo "dry-run: install plan for ${PROJECT} (nothing was downloaded or written)"
  echo "  OS:          ${OS}"
  echo "  ARCH:        ${ARCH}"
  echo "  Version:     ${VERSION}"
  echo "  Bin dir:     ${BIN_DIR}"
  echo "  Method:      release download (fallback: go install if download fails)"
  echo "  Archive:     ${ARCHIVE_URL}"
  echo "  Checksums:   ${CHECKSUMS_URL}"
  if command -v go >/dev/null 2>&1; then
    for bin in $BINARIES; do
      echo "  Go fallback: GOBIN=\"${BIN_DIR}\" CGO_ENABLED=0 go install ${MODULE}/cmd/${bin}@${GO_REF}"
    done
  else
    echo "  Go fallback: unavailable ('go' not on PATH)"
  fi
  exit 0
fi

# ------------------------------------------------------------- install ------

cleanup() {
  if [ -n "$TMP_DIR" ]; then
    rm -rf "$TMP_DIR"
  fi
}
trap cleanup 0

TMP_DIR="$(mktemp -d)"

# install_from_release downloads archive + checksums, verifies integrity and
# copies the binaries into BIN_DIR. Returns non-zero when the release assets
# cannot be used (caller falls back to `go install`). A checksum MISMATCH is
# a hard failure: exit rather than install bad bytes.
install_from_release() {
  info "downloading ${ARCHIVE_URL}"
  if ! fetch "$ARCHIVE_URL" "${TMP_DIR}/${ARCHIVE}"; then
    warn "could not download release archive ${ARCHIVE} (assets may not be published yet)"
    return 1
  fi

  if fetch "$CHECKSUMS_URL" "${TMP_DIR}/checksums.txt"; then
    expected="$(awk -v f="$ARCHIVE" '$2 == f { print $1; exit }' "${TMP_DIR}/checksums.txt")"
    if [ -z "$expected" ]; then
      warn "checksums.txt has no entry for ${ARCHIVE}; skipping verification"
    else
      actual=""
      case "$OS" in
        linux)  actual="$(sha256sum "${TMP_DIR}/${ARCHIVE}" 2>/dev/null | awk '{print $1}')" ;;
        darwin) actual="$(shasum -a 256 "${TMP_DIR}/${ARCHIVE}" 2>/dev/null | awk '{print $1}')" ;;
      esac
      if [ -z "$actual" ] && command -v openssl >/dev/null 2>&1; then
        actual="$(openssl dgst -sha256 "${TMP_DIR}/${ARCHIVE}" 2>/dev/null | awk '{print $NF}')"
      fi
      if [ -z "$actual" ]; then
        warn "no sha256 tool (sha256sum/shasum/openssl) available; skipping verification"
      elif [ "$actual" != "$expected" ]; then
        err "checksum mismatch for ${ARCHIVE}"
        err "  expected: ${expected}"
        err "  actual:   ${actual}"
        exit 1
      else
        info "checksum verified (${expected})"
      fi
    fi
  else
    warn "checksums.txt unavailable; skipping checksum verification"
  fi

  if ! tar -xzf "${TMP_DIR}/${ARCHIVE}" -C "$TMP_DIR"; then
    warn "could not extract ${ARCHIVE}"
    return 1
  fi

  # Binaries may sit at the archive root or one directory deep. Locate all of
  # them before copying anything, so a partial install never happens.
  for bin in $BINARIES; do
    if [ -z "$(find "$TMP_DIR" -type f -name "$bin" -print 2>/dev/null | head -n 1)" ]; then
      warn "binary '${bin}' not found in release archive"
      return 1
    fi
  done

  mkdir -p "$BIN_DIR"
  for bin in $BINARIES; do
    src="$(find "$TMP_DIR" -type f -name "$bin" -print 2>/dev/null | head -n 1)"
    cp "$src" "${BIN_DIR}/${bin}"
    chmod +x "${BIN_DIR}/${bin}"
    info "installed ${BIN_DIR}/${bin}"
  done
  return 0
}

# install_from_go builds each binary with `go install` straight into BIN_DIR.
# The first attempt is CGO_ENABLED=0 (most portable); if that fails — e.g.
# older release tags that still require cgo for tree-sitter — retry once with
# cgo enabled when a C compiler is available.
install_from_go() {
  if ! command -v go >/dev/null 2>&1; then
    err "release download failed and the Go toolchain ('go') is not on PATH"
    err "install Go and re-run, or fetch the binaries manually from:"
    err "  https://github.com/${REPO}/releases"
    return 1
  fi
  info "installing via 'go install' (${MODULE}/cmd/... @ ${GO_REF})"
  mkdir -p "$BIN_DIR"
  for bin in $BINARIES; do
    info "GOBIN=\"${BIN_DIR}\" CGO_ENABLED=0 go install ${MODULE}/cmd/${bin}@${GO_REF}"
    if GOBIN="$BIN_DIR" CGO_ENABLED=0 go install "${MODULE}/cmd/${bin}@${GO_REF}"; then
      info "installed ${BIN_DIR}/${bin}"
      continue
    fi
    if command -v gcc >/dev/null 2>&1 || command -v cc >/dev/null 2>&1; then
      warn "CGO_ENABLED=0 build failed for ${bin}; retrying with cgo enabled"
      if ! GOBIN="$BIN_DIR" CGO_ENABLED=1 go install "${MODULE}/cmd/${bin}@${GO_REF}"; then
        err "'go install' failed for ${bin}"
        return 1
      fi
      info "installed ${BIN_DIR}/${bin}"
    else
      err "'go install' failed for ${bin} and no C compiler (gcc/cc) is"
      err "available for a cgo-enabled retry"
      return 1
    fi
  done
  return 0
}

if install_from_release; then
  info "install method: release download"
else
  warn "release download unavailable; falling back to 'go install'"
  if ! install_from_go; then
    err "all installation methods failed"
    exit 1
  fi
  info "install method: go install"
fi

# PATH hint: warn when the chosen bin dir is not already on PATH.
case ":${PATH}:" in
  *":${BIN_DIR}:"*) ;;
  *)
    warn "${BIN_DIR} is not on your PATH"
    warn "add it to your PATH, e.g.:  export PATH=\"${BIN_DIR}:\$PATH\""
    ;;
esac

info "done: ${PROJECT} toolchain (${BINARIES}) installed into ${BIN_DIR}"
