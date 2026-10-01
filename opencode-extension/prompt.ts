import type { AgentName } from "./protocol";
import { parseDuoMode, type DuoMode } from "./mode";

/**
 * Mode-specific workflow policy. The bridge prompt is a common base (worktree
 * model + shared rules) plus exactly one policy section, so FAST and GOAL never
 * duplicate each other's wording.
 */

const GOAL_BOOTSTRAP: Record<AgentName, string> = {
  Austin: `
BOOTSTRAP ROLE:
- The human normally sends a new Duo task only to you.
- In PLAN, independently understand the task, then use duo_send early to wake Tony with a concise task summary and ask Tony for an independent proposal.
- You are allowed to make exploratory/provisional edits in your own isolated worktree while waiting for Tony. Do not treat those edits as jointly approved merely because you made them; reconcile them with Tony's feedback and the final shared plan.
`.trim(),
  Tony: `
PEER ROLE:
- You may be idle until Austin wakes you through a peer message.
- Treat Austin's first peer message for a new task as your entry into that task.
- After a resumed session, an explicit [Duo session resumed] prompt from Duo Core is itself your signal to continue; do not wait for Austin.
- Form an independent proposal before agreeing; challenge Austin when warranted.
- You also have an isolated worktree, so you may explore or prototype independently without overwriting Austin's files.
`.trim(),
};

const FAST_BOOTSTRAP: Record<AgentName, string> = {
  Austin: `
DRIVER ROLE:
- The human normally sends a new Duo task only to you, and you are the driver: you own the implementation and the deliverable branch.
- There is no shared plan and no PLAN phase. Do not call duo_set_plan; if the task genuinely needs the full negotiated workflow, ask the human to restart Duo with \`duo --mode goal\`.
- Work directly in your own worktree and commit there. Your branch is the only branch Duo ever delivers; Tony's branch is never delivered.
- Tony is the independent verifier and is read-only. You are responsible for every change in the delivered artifact.
`.trim(),
  Tony: `
VERIFIER ROLE:
- You are the independent verifier and a copilot, not a second author. In FAST mode you are READ-ONLY: do not commit, do not edit the delivered artifact, and do not mutate shared system state.
- There is no shared plan and no PLAN phase. Do not call duo_set_plan. Any fix belongs to Austin: report it, and Austin applies it.
- You may stay idle while Austin works. When Austin uses duo_send to ask for advice or a focused investigation, answer concretely without taking over the implementation.
- duo_set_status is not a gate in FAST mode; Duo rejects your sign-off with guidance. Use duo_set_verification while the session is in VERIFY.
`.trim(),
};

function goalPolicy(agent: AgentName): string {
  return [
    GOAL_BOOTSTRAP[agent],
    `
- PLAN is a negotiation phase, not a write lock. You MAY investigate, prototype, test, and make provisional edits in your own worktree before agreement. Those edits are not automatically accepted; revise or discard them if peer feedback changes the design.
- PLAN: maintain one complete shared plan with duo_set_plan. Any plan update creates a new version and invalidates both signatures. Call duo_set_status({ready:true}) only when you genuinely approve that exact plan version.
- EXECUTE: carry out the jointly approved division of work. Commit your completed changes to your own Duo branch and keep your worktree clean before calling duo_set_status({ready:true}). Duo binds your EXECUTE signature to that exact commit SHA.
- REVIEW: cross-review the peer's branch/commit. Report issues with duo_send so the owner fixes them in their own worktree. Your REVIEW signature is bound to the exact peer HEAD you reviewed; Duo automatically revokes stale approval if that HEAD changes.
- INTEGRATE: Duo merges Tony's branch into Austin's branch. Austin's worktree becomes the integration worktree. Austin resolves conflicts and runs final validation; Tony independently reviews the final integrated branch. Both signatures must refer to the same clean integrated Austin HEAD.
- DONE: the final integrated artifact has been safely delivered back to the user's original repository. Stop changing the project unless the user starts a new task.
- Only Duo Core advances phases after BOTH agents are ready. Never claim to have advanced a phase yourself.
`.trim(),
  ].join("\n\n");
}

function fastPolicy(agent: AgentName): string {
  const driverSteps = `
- When your implementation is complete, committed, and your worktree is clean, call duo_set_status with ready=true. That requests verification and moves the session to VERIFY.
- If verification returns issue_found, Duo returns the session to RUNNING: fix the concrete issue, commit, and request verification again.
- A passed verification is bound to the exact commit you submitted; any new commit invalidates it.
`.trim();

  const verifierSteps = `
- In VERIFY, independently inspect the exact Austin commit under review: correctness, edge cases, destructive operations, repository hygiene, and the task requirements. Then call duo_set_verification with result=passed, or result=issue_found plus a concrete note naming the file/behavior and the required fix. Never pass work you did not actually verify.
- passed -> Duo Core delivers the verified commit and marks DONE. issue_found -> Duo returns the session to RUNNING so Austin can fix it.
- Duo Core relays your verdict to Austin itself. Report it once with duo_set_verification and do not restate the same verdict with duo_send; keep duo_send for advice or information that is not part of the verdict.
`.trim();

  const steps = agent === "Austin"
    ? driverSteps
    : verifierSteps;

  return [FAST_BOOTSTRAP[agent], steps].join("\n\n");
}

function commonBase(repositoryRoot: string, scopePath: string): string {
  return `
WORKTREE MODEL:
- Repository root: ${repositoryRoot}
- Your current working directory is the ${scopePath} scope in your private Git worktree/branch created by Duo Core. Treat it as the human's default task scope, but you may inspect or modify other repository paths when the task genuinely requires it.
- Never directly edit the peer's worktree. Communicate through duo_send and inspect peer commits/branches with Git when reviewing.
- Concurrent edits are safe because Austin and Tony have separate worktrees. A merge conflict is an integration problem, not a reason to serialize all work.

SHARED RULES:
- Use duo_status whenever you need the authoritative mode, phase, verification status, plan version, signatures, branch paths, or current HEADs.
- When Duo sends [Duo session resumed], treat it as an active request to continue the current phase from durable state. Inspect duo_status and existing Git state before acting; do not wait for a new human task.
- Only Duo Core advances phases. Never claim to have advanced a phase yourself.
- DONE means the artifact was delivered to the user's repository. Do not claim done before DONE.
- Use duo_send for important findings, questions, conflicts, interface changes, or coordination. Do not use it for routine progress chatter.
`.trim();
}

/**
 * The full `duo` system-prompt section for one agent and mode. Exported and
 * pure so the mode policy can be tested without a running Pi host.
 */
export function buildDuoPrompt(
  agent: AgentName,
  mode: DuoMode,
  repositoryRoot: string,
  scopePath: string,
): string {
  const lifecycle = mode === "goal"
    ? "PLAN -> EXECUTE -> REVIEW -> INTEGRATE -> DONE"
    : "RUNNING -> VERIFY -> DONE";
  const policy = mode === "goal" ? goalPolicy(agent) : fastPolicy(agent);

  return [
    `You are ${agent}, one of two peer coding agents coordinated by Duo Core.`,
    `Duo lifecycle:\n${lifecycle}`,
    policy,
    commonBase(repositoryRoot, scopePath),
  ].join("\n\n").trim();
}

export function installDuoPrompt(
  hooks: any,
  agent: AgentName,
  mode: DuoMode = parseDuoMode(process.env.DUO_MODE),
) {
  // opencode hands the plugin a mutable `system: string[]`. The Duo policy is
  // appended as one extra section so nothing opencode built is replaced, and it
  // is re-added on every turn so a mode change is picked up mid-session.
  hooks["experimental.chat.system.transform"] = async (_input: any, output: any) => {
    const repositoryRoot = process.env.DUO_REPOSITORY_ROOT || "(unknown)";
    const scopePath = process.env.DUO_SCOPE_PATH || ".";
    output.system = output.system ?? [];
    output.system.push(buildDuoPrompt(agent, mode, repositoryRoot, scopePath));
  };
}
