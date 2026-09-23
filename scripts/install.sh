#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
BIN_DIR="${HOME}/.local/bin"
mkdir -p "$BIN_DIR"

"$ROOT/scripts/install-pi-extension.sh"
cd "$ROOT"
go test ./...
go build -o "$BIN_DIR/duo" ./cmd/duo

echo
echo "Installed Duo binary: $BIN_DIR/duo"
echo "Ensure $BIN_DIR is in PATH, then run:"
echo "  cd /path/to/git/repo && duo"
