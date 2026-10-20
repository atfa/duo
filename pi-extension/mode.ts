/**
 * Duo session mode. The Go runtime always exports `DUO_MODE`; this module is
 * deliberately dependency-free so the bridge policy is unit-testable without a
 * running Pi host.
 *
 * FAST is the default for new sessions: Austin drives, Tony verifies, and there
 * is no shared plan. GOAL is the full PLAN -> EXECUTE -> REVIEW -> INTEGRATE ->
 * DONE workflow with dual sign-off at every gate. STEP plans a durable queue
 * first, then runs each tagged [fast] or [goal] item under that policy and lands
 * it on the base branch: the queue and the final step always need both agents.
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
