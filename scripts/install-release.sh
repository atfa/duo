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
install -m 0755 "$tmp/xduo" "$bin_dir/xduo"
install -m 0755 "$tmp/xduo-renderer" "$bin_dir/xduo-renderer"
# Everything else in the archive is a per-driver launch shim or plugin executable.
# Installing whatever the release actually contains is what keeps this script from
# becoming another list of drivers to remember. `duo.tar.gz` sits alongside these
# but does not match the `duo-*` prefix, and directories are skipped.
for binary in "$tmp"/duo-*; do
  [ -f "$binary" ] || continue
  install -m 0755 "$binary" "$bin_dir/$(basename "$binary")"
done
rm -rf "$extension_dir"
cp -R "$tmp/pi-extension" "$extension_dir"

if [ "$os" = "darwin" ]; then
	applications="${HOME}/Applications"
	mkdir -p "$applications"
	app_id="com.atfa.xduo"
	app_destination="$applications/xduo.app"
	installed_app_id() {
		/usr/libexec/PlistBuddy -c 'Print :CFBundleIdentifier' "$1/Contents/Info.plist" 2>/dev/null || true
	}
	if [ -e "$app_destination" ] && [ "$(installed_app_id "$app_destination")" != "$app_id" ]; then
		app_destination="$applications/xduo Duo.app"
		suffix=2
		while [ -e "$app_destination" ] && [ "$(installed_app_id "$app_destination")" != "$app_id" ]; do
			app_destination="$applications/xduo Duo $suffix.app"
			suffix=$((suffix + 1))
		done
	fi
	if [ -e "$app_destination" ]; then
		[ "$(installed_app_id "$app_destination")" = "$app_id" ] || {
			echo "error: refusing to replace unrelated app: $app_destination" >&2
			exit 1
		}
		rm -rf "$app_destination"
	fi
	cp -R "$tmp/xduo.app" "$app_destination"
	printf '%s' "$app_destination" >"$bin_dir/xduo.app-path"
	echo "Installed xduo app to $app_destination"
else
	data_home="${XDG_DATA_HOME:-${HOME}/.local/share}"
	applications="$data_home/applications"
	icons="$data_home/icons/hicolor/512x512/apps"
	mkdir -p "$applications" "$icons"
	install -m 0644 "$tmp/xduo.png" "$icons/xduo.png"
	launcher="$applications/xduo-launcher"
	{
		echo '#!/usr/bin/env bash'
		printf 'export PATH=%q:"$PATH"\n' "$bin_dir"
		printf 'exec %q "$@"\n' "$bin_dir/xduo"
	} >"$launcher"
	chmod 0755 "$launcher"
	cat >"$applications/xduo.desktop" <<DESKTOP
[Desktop Entry]
Type=Application
Name=xduo
Comment=Graphical window for a Duo coding session
Exec="$launcher"
Icon=xduo
Terminal=false
Categories=Development;IDE;
StartupNotify=true
StartupWMClass=xduo
DESKTOP
	echo "Installed xduo desktop entry and icon under $data_home"
fi

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
