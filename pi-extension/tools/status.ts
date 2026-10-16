import { Type } from "@earendil-works/pi-ai";
import type { DuoMode } from "../mode";
import { DuoTransport } from "../transport";
import { failureResult, resultFromResponse } from "./common";

export function registerStatusTool(pi: any, transport: DuoTransport, mode: DuoMode = "goal") {
  const fast = mode === "fast";
  const step = mode === "step";

  pi.registerTool({
    name: "duo_set_status",
    label: "Duo Set Status",
    description: fast
      ? "FAST mode: Austin-only readiness. Austin uses ready=true to request verification; Tony cannot sign and must use duo_set_verification instead."
      : step
        ? "STEP mode: sign the gates that need dual sign-off — the step queue in PLAN, and the final step in INTEGRATE. Austin also uses ready=true to put the current step up for review. Duo Core advances the queue cursor itself once Tony's verification passes."
        : "Set your own sign-off state for the current Duo phase. Duo Core advances only when both Austin and Tony have valid, non-stale signatures.",
    parameters: Type.Object({
      ready: Type.Boolean({
        description: fast
          ? "FAST mode: Austin-only. ready=true requests independent verification of your committed, clean HEAD; ready=false withdraws a pending request. It is not a phase sign-off."
          : step
            ? "true = sign the current STEP gate (approve the queue in PLAN, put your committed HEAD up for review in EXECUTE, accept the final step in INTEGRATE); false = revoke."
            : "true = sign current phase; false = revoke your signature because work or objections remain.",
      }),
      note: Type.Optional(
        Type.String({ description: "Optional concise reason or completion note." }),
      ),
    }),
    promptSnippet: fast
      ? "duo_set_status: Austin requests FAST-mode verification"
      : step
        ? "duo_set_status: sign the STEP queue, put a step up for review, or accept the final step"
        : "duo_set_status: sign or revoke your readiness for the current phase",
    promptGuidelines: fast
      ? [
          "FAST is Austin-driven: only Austin calls duo_set_status ready=true, and only to request verification once the work is committed and the worktree is clean.",
          "Tony cannot sign in FAST mode; Duo rejects it and points to duo_set_verification.",
        ]
      : step
        ? [
            "PLAN ready=true approves the exact current queue version; a queue update resets both signatures.",
            "EXECUTE ready=true is Austin-only and requires a committed, clean worktree: Duo binds it to that HEAD and hands the step to Tony for verification.",
            "Tony verifies with duo_set_verification, not with ready=true, for every step except the last.",
            "INTEGRATE ready=true is required from both agents on the FINAL step only; it accepts the landed artifact and marks DONE.",
          ]
      : [
          "PLAN ready=true means you approve the exact current plan version; provisional edits do not count as plan approval.",
          "EXECUTE ready=true requires your own worktree to be clean; Duo records your current commit SHA as evidence.",
          "REVIEW ready=true means you reviewed the peer's exact current commit and have no unresolved objections.",
          "INTEGRATE ready=true means the clean integrated Austin HEAD has been validated/reviewed and is acceptable.",
          "Use ready=false if later information reopens your work or review.",
        ],
    async execute(
      _toolCallId: string,
      params: { ready: boolean; note?: string },
    ) {
      try {
        return resultFromResponse(
          await transport.request("set_status", {
            ready: params.ready,
            note: params.note?.trim(),
          }),
        );
      } catch (error) {
        return failureResult("Duo status update failed", error);
      }
    },
  });
}
