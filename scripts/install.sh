#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
BIN_DIR="${HOME}/.local/bin"
mkdir -p "$BIN_DIR"

"$ROOT/scripts/install-pi-extension.sh"
"$ROOT/scripts/install-opencode-extension.sh"
cd "$ROOT"
go test ./...
go build -o "$BIN_DIR/duo" ./cmd/duo
go build -o "$BIN_DIR/duo-pi" ./cmd/duo-pi
go build -o "$BIN_DIR/duo-agy" ./cmd/duo-agy
go build -o "$BIN_DIR/duo-opencode" ./cmd/duo-opencode

# Driver plugins. Duo falls back to running these same drivers in-process when no
# plugin is installed, but `duo plugins` should list what is actually available.
go build -o "$BIN_DIR/duo-plugin-pi" ./cmd/duo-plugin-pi
go build -o "$BIN_DIR/duo-plugin-opencode" ./cmd/duo-plugin-opencode
go build -o "$BIN_DIR/duo-plugin-example" ./cmd/duo-plugin-example

echo
echo "Installed Duo binaries to $BIN_DIR:"
echo "  duo                    the coordinator, TUI and bridge"
echo "  duo-pi duo-agy duo-opencode"
echo "                         launch shims kept for DUO_PI_COMMAND compatibility"
echo "  duo-plugin-pi          Pi Driver Plugin (Duo Driver Plugin Protocol v1)"
echo "  duo-plugin-opencode    opencode Driver Plugin"
echo "  duo-plugin-example     reference plugin, used by the contract tests"
echo "Ensure $BIN_DIR is in PATH, then run:"
echo "  cd /path/to/git/repo && duo"
