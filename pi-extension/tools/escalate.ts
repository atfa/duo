import { Type } from "@earendil-works/pi-ai";
import { isToolEnabledForMode, type DuoMode } from "../mode";
import { DuoTransport } from "../transport";
import { failureResult, resultFromResponse } from "./common";

/**
 * FAST mode only. Austin or Tony can call duo_escalate when a task requires
 * co-design, dual-agent implementation, or full Goal mode rigor.
 */
export function registerEscalateTool(pi: any, transport: DuoTransport, mode: DuoMode) {
  if (!isToolEnabledForMode("escalate", mode)) return;

  pi.registerTool({
    name: "duo_escalate",
    label: "Duo Escalate",
    description:
      "FAST mode only. Dynamically upgrade the session from Fast mode to Goal mode. Transitions session to PLAN phase and initializes a shared co-design plan while preserving all commits already made.",
    parameters: Type.Object({
      reason: Type.String({
        description:
          "Required: explain why this task needs Goal-mode co-design or dual implementation (e.g. unexpected architecture complexity, high-risk refactoring).",
      }),
    }),
    promptSnippet: "duo_escalate: dynamically upgrade from FAST mode to GOAL mode co-design",
    promptGuidelines: [
      "Use duo_escalate in FAST mode when a task turns out to need full Goal-mode collaboration.",
      "Escalation moves the session to PLAN phase with a shared plan v1 seeded with your reason.",
      "All code commits in worktrees are preserved.",
    ],
    async execute(_toolCallId: string, params: { reason: string }) {
      const reason = params.reason?.trim() ?? "";
      if (reason === "") {
        return failureResult("Duo escalate failed", "reason is required");
      }
      try {
        return resultFromResponse(
          await transport.request("escalate", { note: reason }),
        );
      } catch (error) {
        return failureResult("Duo escalate failed", error);
      }
    },
  });
}
