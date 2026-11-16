#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
BIN_DIR="${HOME}/.local/bin"
mkdir -p "$BIN_DIR"

"$ROOT/scripts/install-pi-extension.sh"
"$ROOT/scripts/install-opencode-extension.sh"
cd "$ROOT"
# Every command under cmd/ ships, so adding a driver needs no edit here. Each
# binary is named after its own directory, which is exactly the name the registry
# looks for on PATH, so the build output and `duo plugins` cannot disagree about
# what a driver is called.
for pkg in $(go list ./cmd/...); do
  go build -o "$BIN_DIR/$(basename "$pkg")" "$pkg"
done
# Build before testing, and put the install directory on PATH for the test run:
# the session tests start a real session through the driver plugin executables,
# which the registry finds on PATH. On a machine that has never installed Duo,
# the test run would otherwise be the first thing to notice those executables do
# not exist yet, and that reports this script's ordering, not the code.
export PATH="$BIN_DIR:$PATH"
go test ./...

echo
echo "Installed Duo binaries to $BIN_DIR:"
echo "  duo        the coordinator, TUI and bridge"
echo "  xduo       the multi-instance manager"
echo "  duo-*      one launch shim and plugin executable per driver"
echo "Run 'duo plugins' to list the drivers that are installed, and"
echo "'duo plugins test --all' to check each one against the protocol."
echo "Ensure $BIN_DIR is in PATH, then run:"
echo "  cd /path/to/git/repo && duo"
echo "  xduo"
