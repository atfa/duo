#!/usr/bin/env bash
set -euo pipefail

# Run in a real desktop session after building xduo. Decorations, icon artwork,
# and light/dark palettes are visual checks; only observable window state is
# automated. Usage: scripts/xduo-window-smoke.sh [--interactive] [path-to-xduo]
root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
interactive=0
renderer=""
while (($#)); do
	case "$1" in
		--interactive) interactive=1 ;;
		-h|--help) printf 'usage: %s [--interactive] [path-to-xduo]\n' "$0"; exit 0 ;;
		-*) printf 'error: unknown option: %s\n' "$1" >&2; exit 2 ;;
		*) [[ -z "$renderer" ]] || { echo 'error: only one renderer path is accepted' >&2; exit 2; }; renderer="$1" ;;
	esac
	shift
done
renderer="${renderer:-$root/gpui/.target/release/xduo}"
if [[ ! -x "$renderer" ]]; then
	printf 'error: xduo is not executable: %s\nBuild it with `make gpui` or pass its path.\n' "$renderer" >&2
	exit 1
fi
if [[ "$interactive" -eq 0 && -t 0 ]]; then interactive=1; fi
if [[ "$interactive" -eq 1 && ! -t 0 ]]; then
	echo 'error: interactive visual review requires a terminal on stdin' >&2
	exit 2
fi
case "$(uname -s)" in
	Darwin) backend=macOS ;;
	*)
		if [[ -n "${WAYLAND_DISPLAY:-}" ]]; then backend=Wayland
		elif [[ -n "${DISPLAY:-}" ]]; then backend=X11
		else echo 'SKIP: no desktop display is available.'; exit 77
		fi
		;;
esac

work="$(mktemp -d)"
app_pid=""
host_pid=""
cleanup() {
	[[ -z "$app_pid" ]] || kill "$app_pid" 2>/dev/null || true
	[[ -z "$host_pid" ]] || kill "$host_pid" 2>/dev/null || true
	[[ -z "$app_pid" ]] || wait "$app_pid" 2>/dev/null || true
	[[ -z "$host_pid" ]] || wait "$host_pid" 2>/dev/null || true
	rm -rf "$work"
}
trap cleanup EXIT INT TERM
cat >"$work/host.py" <<'PY'
import json, sys, time
view = {"type":"view", "view": {
    "version":"v0.12.1", "session":"window-smoke", "mode":"step",
    "phase":"EXECUTE", "language":"en", "repository":"/smoke/repo",
    "base_branch":"main", "base_commit":"0123456789ab",
    "status":"xduo desktop window smoke", "plan_version":1,
    "plan":"Synthetic window smoke session", "ready":{"austin":True,"tony":True},
    "steps_total":1, "step_cursor":1, "delivery":"",
    "notices":["Synthetic view; no Duo session is running."],
    "agents": {
        "austin":{"present":True,"connected":True,"process":"running","driver":"smoke","model":"","thinking":"","busy":False,"activity":"Window smoke"},
        "tony":{"present":False,"connected":False,"process":"stopped","driver":"","model":"","thinking":"","busy":False,"activity":""}}}}
sys.stdout.write(json.dumps(view) + "\n")
sys.stdout.flush()
time.sleep(3600)
PY
mkfifo "$work/frames"
python3 "$work/host.py" >"$work/frames" 2>"$work/host.err" &
host_pid=$!
"$renderer" <"$work/frames" >"$work/stdout" 2>"$work/stderr" &
app_pid=$!
fail() { echo "FAIL: $*" >&2; [[ ! -s "$work/stderr" ]] || cat "$work/stderr" >&2; exit 1; }
printf '== Launch xduo on %s\n' "$backend"
sleep 2
kill -0 "$app_pid" 2>/dev/null || fail 'renderer exited before the window could be checked'
[[ ! -s "$work/stdout" ]] || fail 'renderer wrote to stdout (reserved for protocol messages)'
printf 'Renderer process %s is alive; visible-window verification follows.\n' "$app_pid"

if [[ "$interactive" -eq 1 ]]; then
	cat <<'CHECKLIST'
Inspect the live window, then press Enter:
  [ ] Visible, titled xduo, and resizable.
  [ ] Decorations and minimize/maximize/restore/close controls work and look right.
  [ ] Taskbar/Dock displays the xduo icon (installed launcher or app bundle).
  [ ] Switching OS appearance between light and dark updates xduo.
CHECKLIST
	IFS= read -r _ || true
fi

case "$backend" in
	X11)
		if ! command -v wmctrl >/dev/null || ! command -v xprop >/dev/null; then
			echo 'SKIP: X11 control needs wmctrl and xprop; use --interactive for visual/manual review.'
			[[ "$interactive" -eq 1 ]] || exit 77
			echo 'Close xduo to finish.'
			while kill -0 "$app_pid" 2>/dev/null; do sleep 1; done
			exit 0
		fi
		window_id=''
		for _ in $(seq 1 30); do
			window_id="$(wmctrl -lp | awk -v pid="$app_pid" '$3 == pid {print $1; exit}')"
			[[ -z "$window_id" ]] || break
			sleep 1
		done
		[[ -n "$window_id" ]] || fail 'xduo window was not found by title and renderer PID'
		props="$(xprop -id "$window_id" WM_CLASS _NET_WM_NAME 2>&1)" || fail 'cannot inspect X11 window properties'
		printf '%s\n' "$props"
		printf '%s' "$props" | grep -qi xduo || fail 'window properties do not identify xduo'
		wmctrl -ir "$window_id" -b add,maximized_vert,maximized_horz || fail 'maximize request failed'
		sleep 1
		state="$(xprop -id "$window_id" _NET_WM_STATE 2>/dev/null || true)"
		[[ "$state" == *MAXIMIZED_VERT* && "$state" == *MAXIMIZED_HORZ* ]] || fail 'maximize state was not reported'
		echo 'PASS: maximize state reported.'
		wmctrl -ir "$window_id" -b remove,maximized_vert,maximized_horz || fail 'restore request failed'
		wmctrl -ir "$window_id" -b add,hidden || fail 'minimize request failed'
		sleep 1
		state="$(xprop -id "$window_id" _NET_WM_STATE 2>/dev/null || true)"
		if [[ "$state" == *HIDDEN* ]]; then echo 'PASS: minimized state reported.'
		else echo 'SKIP: window manager does not expose minimized state through EWMH.'; fi
		wmctrl -ia "$window_id" || fail 'restore after minimize failed'
		wmctrl -ic "$window_id" || fail 'close request failed'
		for _ in $(seq 1 10); do kill -0 "$app_pid" 2>/dev/null || break; sleep 1; done
		kill -0 "$app_pid" 2>/dev/null && fail 'xduo did not exit after close'
		echo 'PASS: closing the window ended xduo.'
		;;
	macOS)
		if osascript -e 'tell application "System Events" to get name of every window of process "xduo"' >"$work/windows" 2>"$work/access.err"; then
			grep -qi xduo "$work/windows" || fail 'Accessibility did not report an xduo window'
			printf 'PASS: macOS Accessibility reports window: %s\n' "$(cat "$work/windows")"
		else
			echo 'SKIP: macOS window automation unavailable; grant Accessibility access to terminal or review manually.'
			cat "$work/access.err" >&2
			[[ "$interactive" -eq 1 ]] || exit 77
		fi
		if [[ "$interactive" -eq 1 ]]; then
			echo 'Close xduo to finish.'
			while kill -0 "$app_pid" 2>/dev/null; do sleep 1; done
		else
			echo 'SKIP: minimize/zoom automation and visual appearance require an interactive review.'
			exit 77
		fi
		;;
	Wayland)
		echo 'SKIP: Wayland has no compositor-independent window-control/introspection API; process liveness is not proof the window opened.'
		[[ "$interactive" -eq 1 ]] || exit 77
		echo 'Confirm the checklist, close xduo, then press Enter.'
		IFS= read -r _ || true
		kill -0 "$app_pid" 2>/dev/null && fail 'xduo still runs after manual close'
		echo 'PASS: renderer exited after manual window close.'
		;;
esac
printf 'Visual-only: inspect decoration rendering, icon artwork, and light/dark palettes on screen.\n'
