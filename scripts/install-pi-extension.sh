#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
BASE="${HOME}/.pi/agent"
DEST="${BASE}/extensions/duo"

mkdir -p "${BASE}/extensions"

# Disable only Duo bridge layouts created by earlier Duo prototypes.
if [[ -d "${BASE}/extensions/duo-v02" ]]; then
  rm -rf "${BASE}/duo-v02.disabled"
  mv "${BASE}/extensions/duo-v02" "${BASE}/duo-v02.disabled"
  echo "Disabled previous Duo v0.2 bridge: ${BASE}/duo-v02.disabled"
fi
if [[ -f "${BASE}/extensions/duo-export.ts" ]]; then
  mv "${BASE}/extensions/duo-export.ts" "${BASE}/duo-export.disabled.ts"
  echo "Disabled legacy duo-export.ts: ${BASE}/duo-export.disabled.ts"
fi

rm -rf "$DEST"
cp -R "$ROOT/pi-extension" "$DEST"

echo "Installed Duo v0.3.1 Pi bridge to: $DEST"
echo "Restart any running Pi processes before starting Duo."
