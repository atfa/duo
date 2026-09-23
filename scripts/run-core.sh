#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
REPO="${1:-${DUO_REPO:-}}"

if [[ -z "${REPO}" ]]; then
  echo "Usage: $0 /path/to/target-git-repo" >&2
  echo "or set DUO_REPO=/path/to/target-git-repo" >&2
  exit 2
fi

cd "$ROOT"
export DUO_REPO="$REPO"
exec go run ./cmd/duo
