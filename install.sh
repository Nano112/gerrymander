#!/bin/sh
# gerrymander installer: fetches the latest release binary for this platform
# into /usr/local/bin (or ~/.local/bin without sudo). Safe to re-run.
#   curl -fsSL https://raw.githubusercontent.com/Nano112/gerrymander/main/install.sh | sh
set -eu

REPO="Nano112/gerrymander"
OS=$(uname -s | tr '[:upper:]' '[:lower:]')
ARCH=$(uname -m)
case "$ARCH" in
  x86_64) ARCH=amd64 ;;
  aarch64 | arm64) ARCH=arm64 ;;
  *) echo "unsupported architecture: $ARCH" >&2; exit 1 ;;
esac
case "$OS" in
  darwin | linux) ;;
  *) echo "unsupported OS: $OS (Windows: grab the zip from the releases page)" >&2; exit 1 ;;
esac

TAG=$(curl -fsSL "https://api.github.com/repos/$REPO/releases/latest" |
  sed -n 's/.*"tag_name": *"\([^"]*\)".*/\1/p' | head -1)
[ -n "$TAG" ] || { echo "could not determine the latest release" >&2; exit 1; }
VERSION=${TAG#v}

URL="https://github.com/$REPO/releases/download/$TAG/gerry_${VERSION}_${OS}_${ARCH}.tar.gz"
TMP=$(mktemp -d)
trap 'rm -rf "$TMP"' EXIT

echo "downloading gerry $TAG ($OS/$ARCH)…"
curl -fsSL "$URL" | tar xz -C "$TMP"
BIN=$(find "$TMP" -type f -name 'gerry*' | head -1)
chmod +x "$BIN"

DEST=/usr/local/bin
if [ ! -w "$DEST" ]; then
  if command -v sudo >/dev/null 2>&1; then
    echo "installing to $DEST (sudo)…"
    sudo install "$BIN" "$DEST/gerry"
  else
    DEST="$HOME/.local/bin"
    mkdir -p "$DEST"
    install "$BIN" "$DEST/gerry"
    echo "installed to $DEST — make sure it's on your PATH"
  fi
else
  install "$BIN" "$DEST/gerry"
fi

echo "gerry $("$DEST/gerry" version 2>/dev/null | cut -d' ' -f2) installed."

# Full bootstrap (daemon + DNS + trust) unless the caller opted out. sudo
# prompts read /dev/tty, so this works under `curl | sh`; without a tty
# (CI, containers) we skip and say what to run.
if [ "${GERRY_INSTALL_ONLY:-}" = "1" ]; then
  echo "GERRY_INSTALL_ONLY=1 — skipping bootstrap. Run: gerry bootstrap"
elif [ -e /dev/tty ] && ( : < /dev/tty ) 2>/dev/null; then
  echo
  "$DEST/gerry" bootstrap < /dev/tty
else
  echo "no tty — skipping bootstrap. Run: gerry bootstrap"
fi
