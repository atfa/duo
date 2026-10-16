#!/usr/bin/env bash
set -euo pipefail

# Proves the two halves of the graphical frontend still meet.
#
#   scripts/gui-smoke.sh
#
# The deterministic half runs anywhere, with no display and no agent process: the
# fake frontend over the real Go host (internal/guiipc's tests drive the real Host
# over pipes with a scripted frontend — first frame a view with notices, then the
# replayed history, then live events; a submit_task and a restart_agent reach the
# session; quit and EOF end Serve), the renderer's own protocol tests, and the
# command-line behaviour of `duo gui`.
#
# The window half needs a display, so it is skipped with a printed reason when
# there is none: the real renderer is fed the same kind of frames and its stdout
# must stay protocol-only. It cannot press a key — a window's input is not
# scriptable here — which is why the command direction is covered by the tests
# above, where it is cheap and deterministic.
#
# It is deliberately not part of `make check`: it needs cargo, and CI has no Rust
# toolchain.

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT

say() { printf '\n== %s\n' "$*"; }
skip() { printf '\n-- %s\n' "$*"; }
die() {
	printf 'gui-smoke: %s\n' "$*" >&2
	exit 1
}

say "The fake frontend over the real Go host"
(cd "$root" && go test ./internal/guiipc/)

say "The renderer's protocol half"
(cd "$root/gpui" && cargo test)

say "duo gui: usage, discovery, and no session when the renderer is missing"
(cd "$root" && go build -o "$work/duo" ./cmd/duo)
mkdir -p "$work/home" "$work/repo"
(cd "$work/repo" &&
	git init -q &&
	: >a.txt &&
	git -c user.email=smoke@example.com -c user.name=smoke add -A &&
	git -c user.email=smoke@example.com -c user.name=smoke commit -qm init)

"$work/duo" --help | grep -q 'duo gui' || die "duo --help does not list the gui command"
HOME="$work/home" "$work/duo" gui --help >/dev/null || die "duo gui --help is not the usage path"

if (cd "$work/repo" && HOME="$work/home" PATH=/usr/bin:/bin "$work/duo" gui >"$work/guiout" 2>&1); then
	die "duo gui succeeded without a renderer"
fi
grep -q 'make gpui' "$work/guiout" || {
	cat "$work/guiout"
	die "the missing-renderer error does not say how to build one"
}
if [ -e "$work/home/.duo" ]; then
	die "duo gui created a session before it had a renderer"
fi

if [ -z "${WAYLAND_DISPLAY:-}${DISPLAY:-}" ]; then
	skip "no display: skipping the window feed (the renderer needs one to open a window)"
	say "OK: the headless half of the graphical frontend"
	exit 0
fi

say "Build the renderer"
(cd "$root" && make gpui >/dev/null)
renderer="$root/gpui/.target/release/xduo"
[ -x "$renderer" ] || die "make gpui did not produce $renderer"

# The fake host writes the frames docs/gui-protocol.md specifies, in the order the
# host sends them, then goes quiet: the renderer reads them and must stay up.
cat >"$work/host.py" <<'PY'
import json
import sys
import time


def emit(frame):
    sys.stdout.write(json.dumps(frame) + "\n")
    sys.stdout.flush()


emit({"type": "view", "view": {
    "version": "v0.12.2", "session": "smoke", "mode": "step", "phase": "EXECUTE",
    "language": "zh", "repository": "/repo", "base_branch": "main",
    "base_commit": "d434ddbfc6", "status": "phase EXECUTE", "plan_version": 1,
    "plan": "# plan", "ready": {"austin": True, "tony": False},
    "steps_total": 2, "step_cursor": 1, "delivery": "", "notices": ["no sqlite3"],
    "agents": {
        "austin": {"present": True, "connected": True, "process": "running",
                   "driver": "pi", "model": "m", "thinking": "high", "busy": True,
                   "activity": "Bash: make check"},
        "tony": {"present": True, "connected": False, "process": "running",
                 "driver": "pi", "model": "", "thinking": "", "busy": False,
                 "activity": ""}}}})
for i in range(6):
    emit({"type": "event", "event": {
        "time": "2026-10-16T00:00:0%dZ" % i,
        "kind": "peer", "agent": "austin" if i % 2 else "tony",
        "peer": "tony" if i % 2 else "austin", "text": "smoke line %d" % i,
        "label": "Austin \u2192 Tony" if i % 2 else "Tony \u2192 Austin"}})
    time.sleep(0.2)
emit({"type": "error", "text": "smoke: a command was rejected"})
time.sleep(2)
emit({"type": "closed", "text": "smoke host done"})
time.sleep(0.5)
PY

say "Feed the renderer real frames and watch both of its streams"
set +e
timeout 10 bash -c "python3 '$work/host.py' | '$renderer' > '$work/out' 2> '$work/err'"
status=$?
set -e

if [ "$status" != "124" ]; then
	printf -- '--- stderr ---\n%s\n' "$(cat "$work/err")"
	die "the renderer exited on its own (status $status); it must survive the feed"
fi
if [ -s "$work/out" ]; then
	printf -- '--- stdout ---\n%s\n' "$(cat "$work/out")"
	die "the renderer wrote to stdout; stdout carries protocol frames and nothing else"
fi
if grep -q 'unparsable frame' "$work/err"; then
	cat "$work/err"
	die "the renderer rejected frames that are valid"
fi
if [ -s "$work/err" ]; then
	printf -- '-- renderer stderr (not part of the protocol) --\n%s\n' "$(cat "$work/err")"
fi

say "OK: the two halves meet"
