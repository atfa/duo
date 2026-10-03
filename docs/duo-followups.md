# Candidate follow-up: verify Goal resume startup liveness

This is an unconfirmed scenario, not a diagnosed bug. Existing tests prove that an
already-started idle Goal project can be nudged and that a fully signed-off
INTEGRATE phase stays quiet. They do not cover a resumed session before either agent
reports activity.

**Code facts:** `Coordinator.OnConnect` touches runtime activity and sends the
one-shot resume prompt, but does not set `project.State.Started`. Goal marks the
project started on a user task or `agent_start` activity. `Monitor.maybeWake` returns
while `Started` is false. Therefore connecting both bridge clients alone does not
open the harness gate; determine whether real resumed agents reliably report
`agent_start` after the wake before treating this as a user-visible failure.

**Reproduction to attempt:** resume a Goal session, let both bridge clients connect,
and capture `events.jsonl`/`duo.log` while neither agent reports activity. Establish
whether startup stalls and which event, if any, eventually allows the harness to
act. Do not assume the old agy-specific report applies to current driver paths.

**Acceptance if reproduced:** a focused test models the observed recovered state,
checks that `RecoveryGrace` suppresses nudges, then proves pending work receives one
nudge after grace. Human-attached and fully signed-off INTEGRATE sessions remain
quiet. Keep delivery-path behavior covered for any driver that cannot receive a
bridge wake. Do not increase grace or idle thresholds to mask a startup gate.
