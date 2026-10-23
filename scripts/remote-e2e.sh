#!/usr/bin/env bash
set -euo pipefail

# End-to-end check against a real local Worker runtime (workerd via wrangler).
#
#   scripts/remote-e2e.sh                 # starts wrangler dev --local here
#   DUO_RELAY_BASE=https://... scripts/remote-e2e.sh   # checks an existing relay
#
# This is deliberately outside `make check`: it needs the Worker runtime, which
# is optional for contributors. `cd remote && node --test` is the hermetic gate.

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
PORT="${DUO_RELAY_PORT:-8787}"
BASE="${DUO_RELAY_BASE:-http://127.0.0.1:${PORT}}"
LOG="$(mktemp -t duo-remote-e2e.XXXXXX.log)"
PID=""

cleanup() {
  if [[ -n "$PID" ]]; then
    kill "$PID" 2>/dev/null || true
    wait "$PID" 2>/dev/null || true
  fi
  if [[ "${DUO_RELAY_KEEP_LOG:-0}" == "1" ]]; then
    echo "wrangler log: $LOG" >&2
  else
    rm -f "$LOG"
  fi
}
trap cleanup EXIT

if [[ -z "${DUO_RELAY_BASE:-}" ]]; then
  if ! command -v wrangler >/dev/null 2>&1; then
    echo "remote-e2e: wrangler is not installed; skipping (hermetic gate: cd remote && node --test)" >&2
    exit 0
  fi

  ( cd "$ROOT/remote" && wrangler dev --local --port "$PORT" >"$LOG" 2>&1 ) &
  PID=$!

  ready=0
  for _ in $(seq 1 120); do
    if curl -fsS -m 2 "${BASE}/healthz" >/dev/null 2>&1; then
      ready=1
      break
    fi
    if ! kill -0 "$PID" 2>/dev/null; then
      echo "remote-e2e: wrangler dev exited before becoming ready" >&2
      tail -20 "$LOG" >&2 || true
      exit 1
    fi
    sleep 1
  done
  if [[ "$ready" != "1" ]]; then
    echo "remote-e2e: wrangler dev did not become ready" >&2
    tail -20 "$LOG" >&2 || true
    exit 1
  fi
fi

DUO_RELAY_BASE="$BASE" node "$ROOT/remote/e2e/relay.mjs"
