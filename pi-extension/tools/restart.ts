import { Type } from "@earendil-works/pi-ai";
import { DuoTransport } from "../transport";
import { failureResult, resultFromResponse } from "./common";

/**
 * Asks Duo to stop so the launcher can resume the session on a newly installed
 * binary; the durable work continues at the same point.
 *
 * Keep this a deliberate, explicit action: it kills both agent processes. The
 * coordinator accepts it only in STEP mode during EXECUTE, i.e. between steps,
 * and the resume prompt re-injects the queue afterwards.
 */
export function registerRestartTool(pi: any, transport: DuoTransport) {
  pi.registerTool({
    name: "duo_restart_duo",
    label: "Duo Restart",
    description:
      "STEP mode only. Ask Duo to stop so the launcher can resume this session on a newly installed binary. Both agents are killed and restarted; the durable step queue continues at the same step. Use it between steps, as the last action of a round, not while a step is under review.",
    parameters: Type.Object({
      reason: Type.String({
        description:
          "Why the restart is wanted now, e.g. the step landed a change to Duo itself.",
      }),
      install: Type.Optional(
        Type.Boolean({
          description:
            "true = also build and install from the delivered branch before resuming. Omit or false to just restart on the binary already installed.",
        }),
      ),
    }),
    promptSnippet:
      "duo_restart_duo: restart Duo between steps, optionally installing the new binary",
    promptGuidelines: [
      "Only call this between steps (EXECUTE), after the current step has landed, never while a step is in REVIEW or being delivered.",
      "Report it once and then stop: Duo is stopping, so there is nothing further to do in this process.",
      "Set install=true only when the landed work should replace the running Duo itself.",
    ],
    async execute(
      _toolCallId: string,
      params: { reason: string; install?: boolean },
    ) {
      const reason = params.reason.trim();
      if (!reason)
        return failureResult("Duo restart failed", "a reason is required");
      try {
        return resultFromResponse(
          await transport.request("restart_duo", {
            note: reason,
            install: params.install === true,
          }),
        );
      } catch (error) {
        return failureResult("Duo restart failed", error);
      }
    },
  });
}
