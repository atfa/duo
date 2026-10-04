#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
BIN_DIR="${HOME}/.local/bin"
mkdir -p "$BIN_DIR"

"$ROOT/scripts/install-pi-extension.sh"
"$ROOT/scripts/install-opencode-extension.sh"
cd "$ROOT"
go test ./...
# Every command under cmd/ ships, so adding a driver needs no edit here. Each
# binary is named after its own directory, which is exactly the name the registry
# looks for on PATH, so the build output and `duo plugins` cannot disagree about
# what a driver is called.
for pkg in $(go list ./cmd/...); do
  go build -o "$BIN_DIR/$(basename "$pkg")" "$pkg"
done

echo
echo "Installed Duo binaries to $BIN_DIR:"
echo "  duo        the coordinator, TUI and bridge"
echo "  duo-*      one launch shim and plugin executable per driver"
echo "Run 'duo plugins' to list the drivers that are installed, and"
echo "'duo plugin test --all' to check each one against the protocol."
echo "Ensure $BIN_DIR is in PATH, then run:"
echo "  cd /path/to/git/repo && duo"
