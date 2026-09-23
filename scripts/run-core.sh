#!/usr/bin/env bash
set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
REPO="${1:-${DUO_REPO:-$PWD}}"
cd "$ROOT"
exec go run ./cmd/duo "$REPO"
