/**
 * Duo session mode. The Go runtime always exports `DUO_MODE`; this module is
 * deliberately dependency-free so the bridge policy is unit-testable without a
 * running Pi host.
 *
 * FAST is the default for new sessions and has no shared plan and no dual
 * sign-off. GOAL keeps the full PLAN -> EXECUTE -> REVIEW -> INTEGRATE -> DONE
 * workflow. An unknown or missing value falls back to GOAL: that is the legacy,
 * conservative behavior, and the Go runtime validates the mode before exporting
 * it anyway.
 */
export type DuoMode = "fast" | "goal";

/** Tools that only exist in one mode. */
export type DuoModeTool = "plan" | "verification" | "escalate";

export function parseDuoMode(value: string | undefined): DuoMode {
  return value?.trim().toLowerCase() === "fast" ? "fast" : "goal";
}

/**
 * `duo_set_plan` is a GOAL-mode tool; `duo_set_verification` and `duo_escalate`
 * are FAST-mode tools. Duo Core rejects the wrong tool server-side as well,
 * so this only keeps the model from being offered a tool it cannot use.
 */
export function isToolEnabledForMode(tool: DuoModeTool, mode: DuoMode): boolean {
  return tool === "plan" ? mode === "goal" : mode === "fast";
}
