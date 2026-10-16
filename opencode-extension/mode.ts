/**
 * Duo session mode. The Go runtime always exports `DUO_MODE`; this module is
 * deliberately dependency-free so the bridge policy is unit-testable without a
 * running Pi host.
 *
 * FAST is the default for new sessions: Austin drives, Tony verifies, and there
 * is no shared plan. GOAL is the full PLAN -> EXECUTE -> REVIEW -> INTEGRATE ->
 * DONE workflow with dual sign-off at every gate. STEP runs that same phase
 * chain once per step of a durable queue, landing each step on the base branch:
 * the queue is dual-approved in PLAN, each step is gated by a verification bound
 * to one exact Austin HEAD, and the final step needs dual sign-off.
 *
 * An unknown or missing value falls back to GOAL: that is the legacy,
 * conservative behavior, and the Go runtime validates the mode before exporting
 * it anyway.
 */
export type DuoMode = "fast" | "goal" | "step";
export type DuoLanguage = "zh" | "en";

export function parseDuoLanguage(value: string | undefined): DuoLanguage {
  return value?.trim().toLowerCase() === "zh" ? "zh" : "en";
}

/** Tools that exist only in some modes. */
export type DuoModeTool = "plan" | "steps" | "verification" | "escalate" | "restart";

export function parseDuoMode(value: string | undefined): DuoMode {
  switch (value?.trim().toLowerCase()) {
    case "fast":
      return "fast";
    case "step":
      return "step";
    default:
      return "goal";
  }
}

/**
 * The one table for every mode-gated bridge tool, so adding a mode is one edit
 * here and cannot drift per tool. Duo Core rejects the wrong tool server-side as
 * well; this only keeps the model from being offered a tool it cannot use.
 *
 * - `duo_set_plan` is GOAL and STEP: STEP needs the prose design document as
 *   well as the step queue.
 * - `duo_set_steps` is STEP only: it is the step queue itself.
 * - `duo_set_verification` is FAST and STEP: in STEP it is the per-step gate.
 * - `duo_escalate` is FAST only: STEP is chosen at start, never escalated into.
 * - `duo_restart_duo` is STEP only: it stops Duo so the launcher can resume the
 *   durable queue on a newly installed binary, which only STEP can continue.
 */
export function isToolEnabledForMode(tool: DuoModeTool, mode: DuoMode): boolean {
  switch (tool) {
    case "plan":
      return mode === "goal" || mode === "step";
    case "steps":
      return mode === "step";
    case "verification":
      return mode === "fast" || mode === "step";
    case "escalate":
      return mode === "fast";
    case "restart":
      return mode === "step";
    default:
      // An unknown tool name is offered nowhere: Duo Core is the authority that
      // accepts or refuses it, and it refuses.
      return false;
  }
}
