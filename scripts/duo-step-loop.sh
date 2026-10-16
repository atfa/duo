#!/usr/bin/env bash
set -euo pipefail

# Runs one Duo session, and every time Duo asks to continue on a new binary
# (exit code 77) installs the newly delivered Duo and resumes the session on it.
# This is what makes STEP mode a loop a human starts once:
#
#   cd /path/to/repo && scripts/duo-step-loop.sh --mode step
#
# The loop lives here, outside Duo, and not inside the session: every agent is a
# child of the Duo process being replaced, so an agent that asks for the upgrade
# is killed by it and cannot relaunch anything. See docs/self-upgrade.md.
#
# The work is in `duo step-loop` rather than in this file on purpose: the exit
# code, the recorded session id and the schema guard are per-session facts, and a
# shell script would have to parse them out of JSON.

exec duo step-loop "$@"
