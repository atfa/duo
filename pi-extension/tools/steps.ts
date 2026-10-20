import { Type } from "@earendil-works/pi-ai";
import { DuoTransport } from "../transport";
import { failureResult, resultFromResponse } from "./common";

/**
 * The step queue is the contract of STEP mode: one entry per small task, each
 * tagged [fast] or [goal] and landed on its own.
 *
 * The bridge sends the raw markdown checkbox list and Duo Core parses it, so the
 * checkbox grammar lives in exactly one place (the Go runtime) and the bridge
 * stays a dumb pipe. Duo Core, never an agent, advances the queue cursor.
 *
 * Registered in every mode so a runtime switch to STEP has the tool ready.
 */
export function registerStepsTool(pi: any, transport: DuoTransport) {
  pi.registerTool({
    name: "duo_set_steps",
    label: "Duo Set Steps",
    description:
      "Create or replace the STEP-mode step queue during PLAN. Send a markdown checkbox list, one step per line, tagging each item [fast] or [goal] (untagged defaults to FAST). A PLAN update resets both agents' approvals. Duo Core advances the queue after each step lands — agents never mark a step done by hand.",
    parameters: Type.Object({
      steps: Type.String({
        description:
          "The step queue as a markdown checkbox list, e.g. '- [ ] [fast] add ModeStep\\n- [ ] [goal] redesign the scheduler'. Send the whole list, not a patch. Duo owns the ticks, so send '- [ ]' for every step.",
      }),
      append: Type.Optional(
        Type.Boolean({
          description:
            "true = add these steps to the END of the existing queue instead of replacing it. This is the only way to change the queue after PLAN, and it cannot disturb steps already landed.",
        }),
      ),
    }),
    promptSnippet:
      "duo_set_steps: publish or extend the STEP-mode step queue (markdown checkbox list)",
    promptGuidelines: [
      "In PLAN use duo_set_steps to publish the whole queue; a queue update resets both approvals, so both agents must sign the new version.",
      "After PLAN the queue can only grow: set append=true to add steps at the end. Replacing the queue is rejected, so never resend the full list during EXECUTE.",
      "Each step must be a committable increment whose test command passes at its own HEAD.",
      "Do not tick boxes yourself: Duo Core marks a step landed when it lands on the base branch.",
      "A landed step is never removed or reordered, so a step that turns out to be too big is split by appending to the end.",
    ],
    async execute(
      _toolCallId: string,
      params: { steps: string; append?: boolean },
    ) {
      const steps = params.steps.trim();
      if (!steps)
        return failureResult("Duo set steps failed", "steps was empty");
      try {
        return resultFromResponse(
          await transport.request("set_steps", {
            steps,
            // The coordinator reads an "append" marker to allow growing the queue
            // after PLAN; without it a mid-loop edit is rejected outright.
            text: params.append === true ? "append" : "",
          }),
        );
      } catch (error) {
        return failureResult("Duo set steps failed", error);
      }
    },
  });
}
