import { StringEnum, Type } from "@earendil-works/pi-ai";
import type { AgentName, VerificationResult } from "../protocol";
import { DuoTransport } from "../transport";
import { failureResult, resultFromResponse } from "./common";

/**
 * Tony only. Reports independent verification in FAST mode and for [fast] steps
 * in STEP mode. Registered in every mode; Duo Core rejects it when the current
 * gate is a Goal cross-sign-off instead.
 */
export function registerVerificationTool(
  pi: any,
  transport: DuoTransport,
  agent: AgentName,
) {
  if (agent !== "Tony") return;

  pi.registerTool({
    name: "duo_set_verification",
    label: "Duo Set Verification",
    description:
      "Report the result of independently verifying an exact Austin commit. In FAST, passed lands the verified artifact and issue_found returns it to Austin. In STEP this applies to a [fast] step; a [goal] step uses duo_set_status cross-sign-off instead.",
    parameters: Type.Object({
      result: StringEnum(["passed", "issue_found"] as const, {
        description:
          "passed = the verified commit is acceptable; issue_found = a concrete defect or risk blocks it.",
      }),
      note: Type.Optional(
        Type.String({
          description:
            "Required with issue_found: the concrete issue and the fix Austin must make.",
        }),
      ),
    }),
    promptSnippet: "duo_set_verification: pass or reject Austin's exact commit",
    promptGuidelines: [
      "Only Tony calls duo_set_verification, and only for FAST mode or the current [fast] step.",
      "result=passed accepts the exact Austin commit recorded when verification was requested; any later commit invalidates it.",
      "result=issue_found requires a concrete note naming the file/behavior and the required fix; Duo returns the work to Austin.",
      "For a [goal] step, cross-review the peer and sign with duo_set_status instead.",
    ],
    async execute(
      _toolCallId: string,
      params: { result: string; note?: string },
    ) {
      let result: VerificationResult;
      if (params.result === "passed" || params.result === "issue_found") {
        result = params.result;
      } else {
        return failureResult(
          "Duo verification failed",
          "result must be passed or issue_found",
        );
      }
      const note = params.note?.trim() ?? "";
      if (result === "issue_found" && note === "") {
        return failureResult(
          "Duo verification failed",
          "issue_found requires a concrete note",
        );
      }
      try {
        return resultFromResponse(
          await transport.request("set_verification", {
            verification: result,
            note,
          }),
        );
      } catch (error) {
        return failureResult("Duo verification failed", error);
      }
    },
  });
}
