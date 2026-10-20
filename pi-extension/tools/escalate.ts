import { Type } from "@earendil-works/pi-ai";
import { DuoTransport } from "../transport";
import { failureResult, resultFromResponse } from "./common";

/**
 * Austin or Tony can switch to Goal mode whenever a task needs shared design,
 * dual-agent implementation, or full Goal rigor. Registered in every mode so a
 * runtime switch never leaves the tool out of the process.
 */
export function registerEscalateTool(pi: any, transport: DuoTransport) {
  pi.registerTool({
    name: "duo_escalate",
    label: "Duo Escalate",
    description:
      "Switch this session to GOAL mode at any time. Duo starts a new PLAN round, preserves commits already made, and initializes a shared co-design plan.",
    parameters: Type.Object({
      reason: Type.String({
        description:
          "Required: explain why this task needs Goal-mode co-design or dual implementation (e.g. unexpected architecture complexity, high-risk refactoring).",
      }),
    }),
    promptSnippet: "duo_escalate: switch to GOAL mode co-design",
    promptGuidelines: [
      "Use duo_escalate when FAST or STEP work turns out to need one shared plan or continuous joint implementation.",
      "The switch starts a new PLAN round with a shared plan seeded with your reason; pending signatures and the old step queue are reset.",
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
