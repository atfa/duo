import { Type } from "@earendil-works/pi-ai";
import { DuoTransport } from "../transport";
import { failureResult, resultFromResponse } from "./common";

export function registerStatusTool(pi: any, transport: DuoTransport) {
  pi.registerTool({
    name: "duo_set_status",
    label: "Duo Set Status",
    description:
      "Set your own sign-off state for the current Duo phase. Duo Core advances only when both Austin and Tony have valid, non-stale signatures.",
    parameters: Type.Object({
      ready: Type.Boolean({
        description:
          "true = sign current phase; false = revoke your signature because work or objections remain.",
      }),
      note: Type.Optional(
        Type.String({ description: "Optional concise reason or completion note." }),
      ),
    }),
    promptSnippet: "duo_set_status: sign or revoke your readiness for the current phase",
    promptGuidelines: [
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
