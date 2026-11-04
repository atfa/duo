import { Type } from "@earendil-works/pi-ai";
import { DuoTransport } from "../transport";
import { failureResult, resultFromResponse } from "./common";

export function registerLoopTools(pi: any, transport: DuoTransport) {
  pi.registerTool({
    name: "duo_wait",
    label: "Duo Wait",
    description: "LOOP: read pending events or durably wait for a peer message or optional deadline. End your turn after waiting. Acknowledge an event only after processing it.",
    parameters: Type.Object({
      until: Type.Optional(Type.String({ description: "RFC3339 deadline; omit to wait for an event." })),
      reason: Type.Optional(Type.String({ description: "Waiting reason and concise working summary." })),
      ack: Type.Optional(Type.String({ description: "Previously processed event ID." })),
    }),
    async execute(_id: string, params: { until?: string; reason?: string; ack?: string }) {
      try {
        return resultFromResponse(await transport.request("loop_wait", {
          text: params.until?.trim(), note: params.reason?.trim(), eventId: params.ack?.trim(),
        }));
      } catch (error) { return failureResult("Duo wait failed", error); }
    },
  });
  pi.registerTool({
    name: "duo_finish",
    label: "Duo Finish",
    description: "LOOP: propose or confirm completion with the same agreed summary. No Git delivery occurs.",
    parameters: Type.Object({ summary: Type.String({ description: "Agreed termination reason and result." }) }),
    async execute(_id: string, params: { summary: string }) {
      try {
        return resultFromResponse(await transport.request("loop_finish", { note: params.summary.trim() }));
      } catch (error) { return failureResult("Duo finish failed", error); }
    },
  });
}
