#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
SRC="$ROOT/opencode-extension"
PLUGIN_DIR="${HOME}/.config/opencode/plugin"
DEST="$PLUGIN_DIR/duo"
ENTRY="$PLUGIN_DIR/duo.ts"

if [[ ! -f "$SRC/index.ts" ]]; then
  echo "error: $SRC/index.ts not found" >&2
  exit 1
fi

# opencode loads .ts directly, so only the runtime sources are installed. The
# dev-only package.json/tsconfig.json/node_modules stay in the repository and are
# used for typechecking, not at runtime.
mkdir -p "$PLUGIN_DIR"
rm -rf "$DEST" "$ENTRY"
mkdir -p "$DEST"
cp "$SRC"/*.ts "$DEST/"

# opencode auto-discovers any *.ts file in the plugin directory, so the entry
# point only has to re-export the bridge directory's default export.
cat > "$ENTRY" <<'TS'
// Duo bridge for opencode. Installed by Duo's install script; see
// opencode-extension/ in the Duo repository. Remove this file to uninstall.
export { default } from "./duo/index";
TS

echo "Installed Duo opencode bridge to: $DEST"
echo "Restart any running opencode processes before starting Duo."
