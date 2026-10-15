import { Type } from "@earendil-works/pi-ai";
import { isToolEnabledForMode, type DuoMode } from "../mode";
import { DuoTransport } from "../transport";
import { failureResult, resultFromResponse } from "./common";

/**
 * STEP mode only. The step queue is the contract of the mode: one entry per
 * small task, each of which is reviewed and landed on its own.
 *
 * The bridge sends the raw markdown checkbox list and Duo Core parses it, so the
 * checkbox grammar lives in exactly one place (the Go runtime) and the bridge
 * stays a dumb pipe. Duo Core, never an agent, advances the queue cursor.
 */
export function registerStepsTool(pi: any, transport: DuoTransport, mode: DuoMode) {
  if (!isToolEnabledForMode("steps", mode)) return;

  pi.registerTool({
    name: "duo_set_steps",
    label: "Duo Set Steps",
    description:
      "Create or replace the STEP-mode step queue during PLAN. Send a markdown checkbox list, one step per line; every update creates a new version and invalidates both agents' signatures. Duo Core advances the queue after each step lands — agents never mark a step done by hand.",
    parameters: Type.Object({
      steps: Type.String({
        description:
          "The complete ordered queue as a markdown checkbox list, e.g. '- [ ] add ModeStep\\n- [ ] land per-step delivery'. Send the whole list, not a patch.",
      }),
    }),
    promptSnippet: "duo_set_steps: publish the STEP-mode step queue (markdown checkbox list)",
    promptGuidelines: [
      "Use duo_set_steps only in PLAN; a queue update creates a new version and resets both approvals.",
      "Each step must be a committable increment whose test command passes at its own HEAD.",
      "Do not tick boxes yourself: Duo Core marks a step landed when it lands on the base branch.",
      "A step added mid-loop is appended at the end; a landed step is never removed or reordered.",
    ],
    async execute(_toolCallId: string, params: { steps: string }) {
      const steps = params.steps.trim();
      if (!steps) return failureResult("Duo set steps failed", "steps was empty");
      try {
        return resultFromResponse(await transport.request("set_steps", { steps }));
      } catch (error) {
        return failureResult("Duo set steps failed", error);
      }
    },
  });
}
