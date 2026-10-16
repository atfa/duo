#!/usr/bin/env bash
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
if [[ "$(uname -s)" != "Darwin" ]]; then
	echo "error: package-xduo-macos.sh must run on macOS" >&2
	exit 1
fi
if [[ $# -lt 1 || $# -gt 2 ]]; then
	echo "usage: $0 <output.app> [path-to-xduo]" >&2
	exit 2
fi

bundle="$1"
binary="${2:-$root/gpui/.target/release/xduo}"
if [[ -e "$bundle" ]]; then
	echo "error: output already exists: $bundle" >&2
	exit 1
fi
if [[ ! -x "$binary" ]]; then
	echo "error: xduo binary is not executable: $binary" >&2
	exit 1
fi

contents="$bundle/Contents"
mkdir -p "$contents/MacOS" "$contents/Resources"
install -m 0755 "$binary" "$contents/MacOS/xduo"
install -m 0644 "$root/resources/xduo.icns" "$contents/Resources/xduo.icns"
cat >"$contents/Info.plist" <<'PLIST'
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>CFBundleExecutable</key><string>xduo</string>
  <key>CFBundleIdentifier</key><string>com.atfa.xduo</string>
  <key>CFBundleIconFile</key><string>xduo</string>
  <key>CFBundleName</key><string>xduo</string>
  <key>CFBundlePackageType</key><string>APPL</string>
  <key>CFBundleShortVersionString</key><string>0.11.0</string>
  <key>CFBundleVersion</key><string>0.11.0</string>
  <key>NSHighResolutionCapable</key><true/>
</dict>
</plist>
PLIST

echo "Created $bundle"
echo "Launch a Duo session with: DUO_GPUI_BIN=\"$contents/MacOS/xduo\" duo gui"
