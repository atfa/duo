# Restarting Duo between steps

`duo step-loop --mode step` runs a STEP session and resumes it when the session
requests a restart. The launcher must be outside that session: agents are child
processes of Duo, so they cannot relaunch the coordinator that stops them. The
repository includes `scripts/duo-step-loop.sh` as a convenience wrapper.

An agent may request a restart only in STEP mode during EXECUTE, between steps.
Duo refuses while either agent worktree has uncommitted changes, since stopping
would otherwise lose uncommitted work. The request stops Duo with exit code 77;
the launcher then resumes the saved session at the same queue position. Recovery
reconciles approvals against Git evidence and revokes stale signatures before the
agents start.

By default, the launcher resumes with the same binary. To install a new Duo first,
the request must ask for installation. Before installing, `duo step-loop` compares
the delivered source's session snapshot schema with the schema of the saved
session. A newer schema is accepted only when the delivered source registers the
migration that carries this session forward; an older one, or a newer one without
that step, refuses the install and leaves the session available to resume with a
compatible Duo. The installer also refuses a dirty source checkout, then
builds from the repository checkout where completed steps are delivered, not an
agent's worktree. If installation fails, the launcher stops without resuming.

If Duo refuses the restart because an agent worktree is dirty, the session is still
running; commit or discard those changes before requesting it again. If installation
or the schema check fails, the launcher stops and the saved session can be resumed
manually with a compatible Duo. See the [CLI overview](../README.md) for command
invocation details.
