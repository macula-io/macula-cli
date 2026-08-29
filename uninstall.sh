#!/usr/bin/env bash
# Uninstalls macula-cli: removes the binary install.sh placed. Leaves the
# persisted identity alone by default -- it took real puzzle-grinding work
# to generate and reinstalling later should be able to reuse it; pass
# --purge to remove it too.
#
# Usage:
#   curl -fsSL https://raw.githubusercontent.com/macula-io/macula-cli/master/uninstall.sh | bash
#   curl -fsSL .../uninstall.sh | bash -s -- --purge
#
# Env overrides:
#   MACULA_CLI_INSTALL_DIR  where to look for the binary (default: $HOME/.local/bin)
set -euo pipefail

INSTALL_DIR="${MACULA_CLI_INSTALL_DIR:-$HOME/.local/bin}"
BIN_PATH="${INSTALL_DIR}/macula-cli"

# Must match Go's os.UserConfigDir() exactly, which macula-cli itself uses
# to persist its identity: $XDG_CONFIG_HOME (or ~/.config) on Linux,
# ~/Library/Application Support on macOS. NOT the same directory on both
# platforms -- a real bug when this was first written to always assume
# ~/.config, caught before it shipped.
case "$(uname -s)" in
  Darwin) CONFIG_BASE="$HOME/Library/Application Support" ;;
  *) CONFIG_BASE="${XDG_CONFIG_HOME:-$HOME/.config}" ;;
esac
CONFIG_DIR="${CONFIG_BASE}/macula-cli"

log() { printf '%s\n' "$*" >&2; }

purge=0
for arg in "$@"; do
  case "$arg" in
    --purge) purge=1 ;;
    *) log "uninstall.sh: unknown argument: $arg"; exit 2 ;;
  esac
done

if [ -e "$BIN_PATH" ]; then
  rm -f "$BIN_PATH"
  log "removed ${BIN_PATH}"
else
  log "no binary found at ${BIN_PATH} (already removed, or installed elsewhere -- set MACULA_CLI_INSTALL_DIR)"
fi

if [ "$purge" -eq 1 ]; then
  if [ -e "$CONFIG_DIR" ]; then
    rm -rf "$CONFIG_DIR"
    log "removed ${CONFIG_DIR} (--purge: persisted identity deleted too)"
  else
    log "no config directory found at ${CONFIG_DIR}"
  fi
else
  if [ -e "$CONFIG_DIR" ]; then
    log "left ${CONFIG_DIR} in place (persisted identity) — pass --purge to remove it too"
  fi
fi
