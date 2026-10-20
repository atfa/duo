import { Type } from "@earendil-works/pi-ai";
import { DuoTransport } from "../transport";
import { failureResult, resultFromResponse } from "./common";

/**
 * Sign-off tool for every mode. It is registered unconditionally because Duo
 * can switch workflow without restarting Pi; Duo Core decides which gate the
 * signature belongs to and rejects calls the current mode does not accept.
 */
export function registerStatusTool(pi: any, transport: DuoTransport) {
  pi.registerTool({
    name: "duo_set_status",
    label: "Duo Set Status",
    description:
      "Set your own sign-off state for the current Duo gate. In FAST, Austin uses ready=true to request verification and Tony reports with duo_set_verification. In GOAL, both agents sign PLAN/EXECUTE/REVIEW/INTEGRATE. In STEP, both approve the queue and a [goal] step uses the same dual signatures; a [fast] step is verified by Tony. Duo Core enforces the actual gate.",
    parameters: Type.Object({
      ready: Type.Boolean({
        description:
          "true = sign the current gate or request verification (FAST Austin); false = revoke your signature or pending request because work or objections remain.",
      }),
      note: Type.Optional(
        Type.String({
          description: "Optional concise reason or completion note.",
        }),
      ),
    }),
    promptSnippet:
      "duo_set_status: sign or revoke your readiness for the current Duo gate",
    promptGuidelines: [
      "PLAN ready=true approves the exact current plan or step-queue version; an update resets both signatures.",
      "EXECUTE ready=true requires your own worktree to be clean; Duo records your current commit SHA as evidence.",
      "In FAST, only Austin calls it, and only to request verification once the work is committed. Tony uses duo_set_verification.",
      "In STEP, a [fast] step uses Tony's verification; a [goal] step uses the same dual sign-off as GOAL.",
      "REVIEW ready=true means you reviewed the peer's exact current commit and have no unresolved objections.",
      "INTEGRATE ready=true means the clean integrated Austin HEAD has been validated and is acceptable. The final step always needs both agents.",
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
