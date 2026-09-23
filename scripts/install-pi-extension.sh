#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
DEST="${HOME}/.pi/agent/extensions/duo-v02"

mkdir -p "$(dirname "$DEST")"
rm -rf "$DEST"
cp -R "$ROOT/pi-extension" "$DEST"

echo "Installed Duo v0.2 Pi bridge to: $DEST"
echo "Start Duo Core first. It will print the exact Tony/Austin worktree commands."
