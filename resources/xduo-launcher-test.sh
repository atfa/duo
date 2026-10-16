#!/usr/bin/env bash
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT
mkdir -p "$tmp/bin" "$tmp/home" "$tmp/repo"
git -C "$tmp/repo" init -q

cat >"$tmp/bin/duo" <<'HOST'
#!/usr/bin/env bash
if [[ "$1" == sessions && "$2" == --unfinished-ids ]]; then
	printf 'session-1\n'
	exit 0
fi
if [[ "$1" == gui ]]; then
	printf '%s\n' "$@" >"$GUI_CALLS"
	exit 0
fi
exit 9
HOST
cat >"$tmp/bin/xduo" <<'RENDERER'
#!/bin/sh
exit 0
RENDERER
chmod +x "$tmp/bin/duo" "$tmp/bin/xduo"

printf '%s\n1\n' "$tmp/repo" | env -u DUO_REPO \
	PATH="$tmp/bin:$PATH" HOME="$tmp/home" XDG_STATE_HOME="$tmp/state" \
	DUO_HOST_BIN="$tmp/bin/duo" DUO_GPUI_BIN="$tmp/bin/xduo" GUI_CALLS="$tmp/gui-calls" \
	bash "$root/resources/xduo-launcher.sh" --terminal-picker \
	>"$tmp/stdout" 2>"$tmp/stderr"

[[ ! -s "$tmp/stdout" ]]
[[ "$(cat "$tmp/gui-calls")" == $'gui\nresume\nsession-1' ]]
terminal_output="$(cat "$tmp/stderr")"
[[ "$terminal_output" == *'Unfinished sessions:'* ]]
[[ "$terminal_output" == *'1) Resume session-1'* ]]
[[ "$terminal_output" == *'Choose a session (0-1)'* ]]

cat >"$tmp/bin/x-terminal-emulator" <<'TERMINAL'
#!/usr/bin/env bash
script="$2"
shift 2
printf '1\n' | bash "$script" "$@"
TERMINAL
chmod +x "$tmp/bin/x-terminal-emulator"
rm -f "$tmp/gui-calls"
env PATH="$tmp/bin:$PATH" HOME="$tmp/home" XDG_STATE_HOME="$tmp/state" \
	DUO_HOST_BIN="$tmp/bin/duo" DUO_GPUI_BIN="$tmp/bin/xduo" DUO_REPO="$tmp/repo" GUI_CALLS="$tmp/gui-calls" \
	bash "$root/resources/xduo-launcher.sh" >"$tmp/fallback-stdout" 2>"$tmp/fallback-stderr"
[[ "$(cat "$tmp/gui-calls")" == $'gui\nresume\nsession-1' ]]
[[ "$(cat "$tmp/fallback-stderr")" == *'1) Resume session-1'* ]]
printf 'terminal launcher chooser checks passed\n'
