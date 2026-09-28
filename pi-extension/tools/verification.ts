import { StringEnum, Type } from "@earendil-works/pi-ai";
import { isToolEnabledForMode, type DuoMode } from "../mode";
import type { AgentName, VerificationResult } from "../protocol";
import { DuoTransport } from "../transport";
import { failureResult, resultFromResponse } from "./common";

/**
 * FAST mode, Tony only. `duo_set_plan` is GOAL-only and is not registered
 * here; Duo Core gates the tool server-side as well, so this is about not
 * offering the model a tool it cannot use.
 */
export function registerVerificationTool(
  pi: any,
  transport: DuoTransport,
  mode: DuoMode,
  agent: AgentName,
) {
  if (!isToolEnabledForMode("verification", mode) || agent !== "Tony") return;

  pi.registerTool({
    name: "duo_set_verification",
    label: "Duo Set Verification",
    description:
      "FAST mode only. Report the result of independently verifying Austin's exact current commit: passed, or issue_found with a concrete note.",
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
    promptSnippet: "duo_set_verification: pass or reject Austin's exact commit in FAST mode",
    promptGuidelines: [
      "Only Tony calls duo_set_verification, and only while the session is in VERIFY.",
      "result=passed accepts the exact Austin commit recorded when verification was requested; any later commit invalidates it.",
      "result=issue_found requires a concrete note naming the file/behavior and the required fix; Duo returns the session to RUNNING.",
    ],
    async execute(_toolCallId: string, params: { result: string; note?: string }) {
      let result: VerificationResult;
      if (params.result === "passed" || params.result === "issue_found") {
        result = params.result;
      } else {
        return failureResult("Duo verification failed", "result must be passed or issue_found");
      }
      const note = params.note?.trim() ?? "";
      if (result === "issue_found" && note === "") {
        return failureResult("Duo verification failed", "issue_found requires a concrete note");
      }
      try {
        return resultFromResponse(
          await transport.request("set_verification", { verification: result, note }),
        );
      } catch (error) {
        return failureResult("Duo verification failed", error);
      }
    },
  });
}
