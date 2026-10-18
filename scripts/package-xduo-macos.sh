#!/usr/bin/env bash
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
if [[ "$(uname -s)" != "Darwin" ]]; then
	echo "error: package-xduo-macos.sh must run on macOS" >&2
	exit 1
fi
if [[ $# -lt 1 || $# -gt 4 ]]; then
	echo "usage: $0 <output.app> [path-to-xduo-renderer] [path-to-duo] [path-to-duo-binaries]" >&2
	exit 2
fi

bundle="$1"
binary="${2:-$root/gpui/.target/release/xduo-renderer}"
duo_binary="${3:-}"
duo_bin_dir="${4:-}"
if [[ -z "$duo_binary" && -x "$root/duo" ]]; then
	duo_binary="$root/duo"
fi
if [[ -z "$duo_binary" ]]; then
	duo_binary="$(command -v duo || true)"
fi
if [[ -z "$duo_bin_dir" && -n "$duo_binary" ]]; then
	duo_bin_dir="$(dirname "$duo_binary")"
fi
if [[ -e "$bundle" ]]; then
	echo "error: output already exists: $bundle" >&2
	exit 1
fi
if [[ ! -x "$binary" ]]; then
	echo "error: xduo binary is not executable: $binary" >&2
	exit 1
fi
if [[ ! -x "$duo_binary" ]]; then
	echo "error: duo binary is not executable: $duo_binary" >&2
	exit 1
fi

contents="$bundle/Contents"
version="${RELEASE_VERSION:-$(git -C "$root" describe --tags --always 2>/dev/null || printf '0.0.0')}"
mkdir -p "$contents/MacOS" "$contents/Resources/bin"
install -m 0755 "$binary" "$contents/MacOS/xduo-renderer"
install -m 0755 "$duo_binary" "$contents/Resources/duo"
install -m 0644 "$root/scripts/xduo-launcher-path.sh" "$contents/Resources/xduo-launcher-path.sh"
install -m 0644 "$root/resources/xduo.icns" "$contents/Resources/xduo.icns"
for helper in "$duo_bin_dir"/duo-*; do
	[[ -f "$helper" && -x "$helper" ]] || continue
	install -m 0755 "$helper" "$contents/Resources/bin/$(basename "$helper")"
done
cat >"$contents/MacOS/xduo" <<'LAUNCHER'
#!/bin/sh
resources="$(cd "$(dirname "$0")/../Resources" && pwd)"
. "$resources/xduo-launcher-path.sh"
show_error() {
	/usr/bin/osascript \
		-e 'on run argv' \
		-e 'display dialog (item 1 of argv) buttons {"OK"} default button "OK" with icon caution' \
	-e 'end run' "$1" >/dev/null 2>&1 || true
}
export PATH="$(duo_xduo_launcher_path "$resources" "$resources/bin" "$PATH")"
export DUO_GPUI_BIN="$(cd "$(dirname "$0")" && pwd)/xduo-renderer"
log_dir="$HOME/Library/Logs/Duo"
umask 077
if ! mkdir -p "$log_dir"; then
	show_error "Could not create the Duo log directory: $log_dir"
	exit 1
fi
log_file="$log_dir/xduo.log"
if "$resources/duo" gui --welcome "$@" >"$log_file" 2>&1; then
	exit 0
else
	status=$?
fi
show_error "Duo could not start (exit $status). See the log at $log_file."
exit "$status"
LAUNCHER
chmod 0755 "$contents/MacOS/xduo"
cat >"$contents/Info.plist" <<PLIST
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>CFBundleExecutable</key><string>xduo</string>
  <key>CFBundleIdentifier</key><string>com.atfa.xduo</string>
  <key>CFBundleIconFile</key><string>xduo</string>
  <key>CFBundleName</key><string>xduo</string>
  <key>CFBundlePackageType</key><string>APPL</string>
  <key>CFBundleShortVersionString</key><string>${version#v}</string>
  <key>CFBundleVersion</key><string>${version#v}</string>
  <key>NSHighResolutionCapable</key><true/>
</dict>
</plist>
PLIST

echo "Created $bundle"
echo "Open the app and choose a Git repository to start a Duo session."
