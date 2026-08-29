#!/usr/bin/env bash
# Installs macula-cli for Linux and macOS: downloads the release archive
# matching this machine's OS/arch from GitHub Releases, verifies it
# against the release's own checksums.txt, and installs the binary.
#
# Usage:
#   curl -fsSL https://raw.githubusercontent.com/macula-io/macula-cli/master/install.sh | bash
#
# Env overrides:
#   MACULA_CLI_VERSION      pin a version (e.g. "v0.1.0") instead of latest
#   MACULA_CLI_INSTALL_DIR  install directory (default: $HOME/.local/bin)
set -euo pipefail

REPO="macula-io/macula-cli"
INSTALL_DIR="${MACULA_CLI_INSTALL_DIR:-$HOME/.local/bin}"

log() { printf '%s\n' "$*" >&2; }
die() { log "error: $*"; exit 1; }

need() { command -v "$1" >/dev/null 2>&1 || die "'$1' is required but not found on PATH"; }
need curl
need tar

detect_os() {
  case "$(uname -s)" in
    Linux) echo "linux" ;;
    Darwin) echo "darwin" ;;
    *) die "unsupported OS: $(uname -s) — this script covers Linux and macOS only; see install.ps1 for Windows" ;;
  esac
}

detect_arch() {
  case "$(uname -m)" in
    x86_64|amd64) echo "amd64" ;;
    arm64|aarch64) echo "arm64" ;;
    *) die "unsupported architecture: $(uname -m)" ;;
  esac
}

os="$(detect_os)"
arch="$(detect_arch)"

version="${MACULA_CLI_VERSION:-}"
if [ -z "$version" ]; then
  log "resolving latest release..."
  # Capture the full response before grep/sed touch it -- piping curl
  # straight into `grep -m1` closes the pipe on the first match, curl
  # gets SIGPIPE on its next write, and `pipefail` aborts the script
  # even though the match was already found. See
  # feedback_pipeline_head_sigpipe_in_scripts for the same class of bug.
  latest_release="$(curl -fsSL "https://api.github.com/repos/${REPO}/releases/latest")"
  version="$(grep -m1 '"tag_name"' <<<"$latest_release" | sed -E 's/.*"tag_name": *"([^"]+)".*/\1/')"
  [ -n "$version" ] || die "could not resolve the latest release tag from the GitHub API"
fi

version_num="${version#v}"
archive="macula-cli_${version_num}_${os}_${arch}.tar.gz"
base_url="https://github.com/${REPO}/releases/download/${version}"

work_dir="$(mktemp -d)"
trap 'rm -rf "$work_dir"' EXIT

log "downloading ${archive} (${version})..."
curl -fsSL "${base_url}/${archive}" -o "${work_dir}/${archive}" \
  || die "download failed — does release ${version} have a ${os}/${arch} build?"

log "verifying checksum..."
curl -fsSL "${base_url}/checksums.txt" -o "${work_dir}/checksums.txt" \
  || die "could not download checksums.txt for ${version}"
expected="$(grep " ${archive}\$" "${work_dir}/checksums.txt" | awk '{print $1}')"
[ -n "$expected" ] || die "no checksum entry found for ${archive} in checksums.txt"
if command -v sha256sum >/dev/null 2>&1; then
  actual="$(sha256sum "${work_dir}/${archive}" | awk '{print $1}')"
elif command -v shasum >/dev/null 2>&1; then
  actual="$(shasum -a 256 "${work_dir}/${archive}" | awk '{print $1}')"
else
  die "neither sha256sum nor shasum found — cannot verify the download"
fi
[ "$expected" = "$actual" ] || die "checksum mismatch for ${archive}: expected ${expected}, got ${actual}"

log "extracting..."
tar -xzf "${work_dir}/${archive}" -C "${work_dir}" macula-cli

mkdir -p "$INSTALL_DIR"
install -m 0755 "${work_dir}/macula-cli" "${INSTALL_DIR}/macula-cli"

log "installed macula-cli ${version} to ${INSTALL_DIR}/macula-cli"

case ":$PATH:" in
  *":${INSTALL_DIR}:"*) ;;
  *)
    log ""
    log "${INSTALL_DIR} is not on your PATH. Add it, e.g.:"
    log "  echo 'export PATH=\"${INSTALL_DIR}:\$PATH\"' >> ~/.bashrc  # or ~/.zshrc"
    ;;
esac

"${INSTALL_DIR}/macula-cli" --version >&2 || true
