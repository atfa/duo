#!/usr/bin/env bash
set -euo pipefail

# Deploy the relay, then prove the deployment serves this tree's page.
#
#   scripts/remote-deploy.sh                # deploy, then verify
#   scripts/remote-deploy.sh --check-only   # verify a deployment, deploy nothing
#
# The phone page is the Worker's static assets (remote/wrangler.jsonc serves
# remote/public/), so a change there reaches no phone until it is deployed. A
# deployment that lags the tree looks exactly like a fix that does not work,
# which is why the check is part of the deploy rather than a separate chore.
#
# DUO_RELAY_BASE overrides the relay (default https://remote.xduo.dev).

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
PUBLIC="$ROOT/remote/public"
BASE="${DUO_RELAY_BASE:-https://remote.xduo.dev}"
BASE="${BASE%/}"
# Any room path works: the Worker answers /p/<room> with the same index.html.
ROOM="${DUO_RELAY_ROOM:-duo-deploy-check}"

check_only=0
case "${1:-}" in
  --check-only) check_only=1 ;;
  "") ;;
  *)
    echo "usage: scripts/remote-deploy.sh [--check-only]" >&2
    exit 2
    ;;
esac

WORK="$(mktemp -d -t duo-remote-deploy.XXXXXX)"
trap 'rm -rf "$WORK"' EXIT

# normalize strips what a transport may legitimately change — CRLF and trailing
# whitespace — so the comparison fails on content, not on encoding.
normalize() {
  sed -e 's/\r$//' -e 's/[[:space:]]*$//' "$1" |
    awk '{ line[NR] = $0 } END { last = NR; while (last > 0 && line[last] == "") last--; for (i = 1; i <= last; i++) print line[i] }'
}

failures=0

# compare <local file> <url path> prints one line per asset and reports a
# mismatch with both sizes and the first differing byte.
compare() {
  local file="$1" path="$2" remote="$WORK/remote" local_norm="$WORK/local" remote_norm="$WORK/remote.norm"
  if ! curl -fsS --max-time 30 -o "$remote" "$BASE$path"; then
    echo "  FAIL  $path could not be fetched from $BASE" >&2
    failures=$((failures + 1))
    return
  fi
  normalize "$file" >"$local_norm"
  normalize "$remote" >"$remote_norm"
  if cmp -s "$local_norm" "$remote_norm"; then
    printf '  ok    %-14s %s  sha256 %s\n' "$path" "$(wc -c <"$remote" | tr -d ' ') bytes" "$(sha256sum <"$remote" | cut -d' ' -f1)"
    return
  fi
  failures=$((failures + 1))
  printf '  FAIL  %s differs from %s\n' "$path" "${file#"$ROOT"/}" >&2
  printf '        live %s bytes, local %s bytes\n' "$(wc -c <"$remote" | tr -d ' ')" "$(wc -c <"$file" | tr -d ' ')" >&2
  # LC_ALL=C keeps cmp's message parseable, and only the offset matters: the
  # temporary file names would only confuse the reader.
  printf '        first difference: %s\n' "$(LC_ALL=C cmp "$local_norm" "$remote_norm" 2>&1 | head -1 | sed 's/.*differ: //')" >&2
}

verify() {
  echo "verifying $BASE against $ROOT (HEAD $(git -C "$ROOT" rev-parse --short HEAD))"
  compare "$PUBLIC/index.html" "/p/$ROOM"
  compare "$PUBLIC/app.js" "/app.js"
  compare "$PUBLIC/console.js" "/console.js"
  compare "$PUBLIC/crypto.js" "/crypto.js"
  compare "$PUBLIC/style.css" "/style.css"
  if [[ "$failures" -gt 0 ]]; then
    echo "remote-deploy: $failures asset(s) on $BASE do not match this tree" >&2
    echo "remote-deploy: the page a phone loads is the deployed one, so this is what a user sees" >&2
    exit 1
  fi
  echo "remote-deploy: $BASE serves this tree"
}

if [[ "$check_only" == "1" ]]; then
  verify
  exit 0
fi

if [[ -n "$(git -C "$ROOT" status --porcelain)" ]]; then
  echo "remote-deploy: the worktree has uncommitted changes; commit them first" >&2
  git -C "$ROOT" status --short >&2
  exit 1
fi

# The deploy publishes the whole tree, so the page and the relay code are always
# one version. A failed deploy stops here: retrying blindly hides which half ran.
echo "deploying $(git -C "$ROOT" rev-parse --short HEAD) to $BASE"
( cd "$ROOT/remote" && npx wrangler deploy )

verify
