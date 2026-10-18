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

# The graphical frontend is a separate Rust crate, so it is a separate build. It
# belongs here rather than behind a second command because one install should
# produce the whole product: `duo gui` finds xduo next to the duo binary.
#
# A machine with no cargo still gets a working terminal Duo, so the renderer is
# skipped with a notice instead of failing the install. `make gpui` is the same
# build for anyone who wants it explicitly.
install_xduo() {
	local built="$ROOT/gpui/.target/release/xduo"
	if command -v cargo >/dev/null 2>&1; then
		# The explicit test is load-bearing: a function called as an `if` condition
		# runs with `set -e` off, so a failed build would otherwise fall through.
		if ! (cd "$ROOT/gpui" && cargo build --release); then
			echo "error: the xduo renderer failed to build; the desktop entry was not installed" >&2
			return 1
		fi
	elif [[ -x "$built" ]]; then
		echo "cargo not found; installing the xduo renderer built earlier"
	else
		echo "cargo not found and no built renderer: xduo is skipped. Install Rust and re-run for the GUI." >&2
		return 1
	fi
	if ! install -m 0755 "$built" "$BIN_DIR/xduo"; then
		echo "error: could not install $built to $BIN_DIR/xduo" >&2
		return 1
	fi
	echo "Installed xduo renderer to $BIN_DIR/xduo"
}

if install_xduo; then
	# The desktop entries resolve duo and xduo from PATH, and this shell's PATH is
	# not guaranteed to contain the directory install just wrote to.
	export PATH="$BIN_DIR:$PATH"
	case "$(uname -s)" in
	Linux)
		"$ROOT/scripts/install-xduo-desktop.sh"
		;;
	Darwin)
		mkdir -p "${HOME}/Applications"
		rm -rf "${HOME}/Applications/xduo.app"
		"$ROOT/scripts/package-xduo-macos.sh" "${HOME}/Applications/xduo.app" "$BIN_DIR/xduo" "$BIN_DIR/duo" "$BIN_DIR"
		;;
	esac
fi

echo
echo "Installed Duo binaries to $BIN_DIR:"
echo "  duo        the coordinator, TUI and bridge"
echo "  duo-*      one launch shim and plugin executable per driver"
echo "  xduo       the graphical renderer for 'duo gui'"
echo "Run 'duo plugins' to list the drivers that are installed, and"
echo "'duo plugins test --all' to check each one against the protocol."
echo "Ensure $BIN_DIR is in PATH, then run:"
echo "  cd /path/to/git/repo && duo"
