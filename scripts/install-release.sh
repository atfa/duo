#!/usr/bin/env bash
set -euo pipefail

repo="atfa/duo"
os="$(uname -s)"
arch="$(uname -m)"

case "$os" in
  Darwin) os="darwin" ;;
  Linux) os="linux" ;;
  *) echo "Duo supports macOS and Linux, not $os" >&2; exit 1 ;;
esac

case "$arch" in
  x86_64|amd64) arch="amd64" ;;
  arm64|aarch64) arch="arm64" ;;
  *) echo "Duo supports amd64 and arm64, not $arch" >&2; exit 1 ;;
esac

command -v curl >/dev/null || { echo "curl is required" >&2; exit 1; }
command -v tar >/dev/null || { echo "tar is required" >&2; exit 1; }

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT
url="https://github.com/$repo/releases/latest/download/duo_${os}_${arch}.tar.gz"
curl -fL "$url" -o "$tmp/duo.tar.gz"
tar -xzf "$tmp/duo.tar.gz" -C "$tmp"

bin_dir="${HOME}/.local/bin"
extension_dir="${HOME}/.pi/agent/extensions/duo"
opencode_plugin_dir="${HOME}/.config/opencode/plugin"
mkdir -p "$bin_dir" "$(dirname "$extension_dir")" "$opencode_plugin_dir"
install -m 0755 "$tmp/duo" "$bin_dir/duo"
[ -f "$tmp/duo-pi" ] && install -m 0755 "$tmp/duo-pi" "$bin_dir/duo-pi"
[ -f "$tmp/duo-agy" ] && install -m 0755 "$tmp/duo-agy" "$bin_dir/duo-agy"
[ -f "$tmp/duo-opencode" ] && install -m 0755 "$tmp/duo-opencode" "$bin_dir/duo-opencode"
[ -f "$tmp/duo-plugin-pi" ] && install -m 0755 "$tmp/duo-plugin-pi" "$bin_dir/duo-plugin-pi"
[ -f "$tmp/duo-plugin-agy" ] && install -m 0755 "$tmp/duo-plugin-agy" "$bin_dir/duo-plugin-agy"
[ -f "$tmp/duo-plugin-opencode" ] && install -m 0755 "$tmp/duo-plugin-opencode" "$bin_dir/duo-plugin-opencode"
[ -f "$tmp/duo-plugin-example" ] && install -m 0755 "$tmp/duo-plugin-example" "$bin_dir/duo-plugin-example"
rm -rf "$extension_dir"
cp -R "$tmp/pi-extension" "$extension_dir"

# opencode bridge: a plugin entry point that re-exports the bridge directory.
rm -rf "$opencode_plugin_dir/duo"
mkdir -p "$opencode_plugin_dir/duo"
cp "$tmp"/opencode-extension/*.ts "$opencode_plugin_dir/duo/"
rm -rf "$opencode_plugin_dir/duo/package.json" "$opencode_plugin_dir/duo/tsconfig.json"
cat > "$opencode_plugin_dir/duo.ts" <<'ENTRY'
// Duo bridge for opencode. Installed by Duo's install script; see
// opencode-extension/ in the Duo repository. Remove this file to uninstall.
export { default } from "./duo/index";
ENTRY

echo "Installed Duo to $bin_dir/duo"
echo "Installed Pi bridge to $extension_dir"
echo "Installed opencode bridge to $opencode_plugin_dir/duo"
echo "Run: cd /path/to/git/repo && duo"
