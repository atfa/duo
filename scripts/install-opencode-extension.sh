#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
SRC="$ROOT/opencode-extension"
PLUGIN_DIR="${HOME}/.config/opencode/plugin"
DEST="$PLUGIN_DIR/duo"

if [[ ! -f "$SRC/index.ts" ]]; then
  echo "error: $SRC/index.ts not found" >&2
  exit 1
fi

# opencode loads .ts directly, so only the runtime sources are installed. The
# dev-only package.json/tsconfig.json/node_modules stay in the repository and are
# used for typechecking, not at runtime.
#
# No single-file entry shim: opencode discovers the duo directory itself, and a
# duo.ts re-export registers the same plugin id twice, which every TUI boot
# reports as "Plugin failed: duo". The rm below cleans the shim older installs
# left behind. Uninstalling is therefore: rm -r "$DEST" (and, for Pi, the
# matching extensions/duo directory).
mkdir -p "$PLUGIN_DIR"
rm -rf "$DEST" "$PLUGIN_DIR/duo.ts"
mkdir -p "$DEST"
cp "$SRC"/*.ts "$DEST/"

echo "Installed Duo opencode bridge to: $DEST"
echo "Restart any running opencode processes before starting Duo."
