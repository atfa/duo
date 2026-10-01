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

echo
echo "Installed Duo binaries to $BIN_DIR: duo, duo-pi, duo-agy, duo-opencode"
echo "Ensure $BIN_DIR is in PATH, then run:"
echo "  cd /path/to/git/repo && duo"
