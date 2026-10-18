#!/usr/bin/env bash
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
data_home="${XDG_DATA_HOME:-${HOME}/.local/share}"
applications="${data_home}/applications"
icons="${data_home}/icons/hicolor/512x512/apps"

command -v duo >/dev/null 2>&1 || {
	echo "error: duo must be on PATH before installing the xduo desktop entry" >&2
	exit 1
}
duo_bin="$(command -v duo)"
if command -v xduo-renderer >/dev/null 2>&1; then
	renderer_bin="$(command -v xduo-renderer)"
elif [[ -x "$root/gpui/.target/release/xduo-renderer" ]]; then
	renderer_bin="$root/gpui/.target/release/xduo-renderer"
else
	echo "error: build the renderer with 'make gpui' or put xduo-renderer on PATH before installing the desktop entry" >&2
	exit 1
fi

mkdir -p "$applications" "$icons"
install -m 0644 "$root/resources/xduo.desktop" "$applications/xduo.desktop"
install -m 0644 "$root/resources/xduo.png" "$icons/xduo.png"
launcher_bin="$(command -v xduo)"
[[ -x "$launcher_bin" ]] || { echo 'error: xduo launcher must be installed on PATH' >&2; exit 1; }
launcher="$applications/xduo-launcher"
{
	echo '#!/usr/bin/env bash'
	printf 'export PATH=%q:"$PATH"\n' "$(dirname "$duo_bin")"
	printf 'export DUO_GPUI_BIN=%q\n' "$renderer_bin"
	printf 'exec %q "$@"\n' "$launcher_bin"
} >"$launcher"
chmod 0755 "$launcher"
# Desktop sessions often start with a shorter PATH than an interactive shell,
# so point the entry at the absolute wrapper path.
launcher_desktop="$(printf '%s' "$launcher" | sed 's/\\/\\\\/g; s/"/\\"/g')"
sed "s|^Exec=xduo$|Exec=\"$launcher_desktop\"|" \
	"$root/resources/xduo.desktop" >"$applications/xduo.desktop"

echo "Installed xduo desktop entry and icon under $data_home"
echo "The desktop entry launches the xduo app."
