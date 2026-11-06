import { Type } from "@earendil-works/pi-ai";
import { DuoTransport } from "../transport";
import type { DuoModeSource } from "../prompt";
import { installLoopGuard } from "../loop-guard";
import { failureResult, resultFromResponse } from "./common";

function stopOnSuccess(response: Awaited<ReturnType<DuoTransport["request"]>>) {
  return { ...resultFromResponse(response), terminate: response.ok === true };
}

export function registerLoopTools(pi: any, transport: DuoTransport, mode: DuoModeSource = "loop") {
  installLoopGuard(pi, transport, mode);
  pi.registerTool({
    name: "duo_events", label: "Duo Events",
    description: "LOOP: read pending events or acknowledge one processed event from your inbox. Does not register a wait.",
    parameters: Type.Object({ ack: Type.Optional(Type.String({ description: "One processed event ID." })), through: Type.Optional(Type.String({ description: "Last processed event ID: acknowledge the entire processed inbox prefix in one call. Use either ack or through." })) }),
    async execute(_id: string, params: { ack?: string; through?: string }) {
      try { return resultFromResponse(await transport.request("loop_events", { eventId: params.ack?.trim(), through: params.through?.trim() })); }
      catch (error) { return failureResult("Duo events failed", error); }
    },
  });
  pi.registerTool({
    executionMode: "sequential",
    name: "duo_wait", label: "Duo Wait",
    description: "LOOP: register a wait, then end this turn without more tools. Waiting takes effect at turn end. Unread events prevent waiting; process them with duo_events first.",
    parameters: Type.Object({
      until: Type.Optional(Type.String({ description: "Future RFC3339 deadline; omit to wait for an event." })),
      reason: Type.Optional(Type.String({ description: "Waiting reason and concise working summary." })),
    }),
    async execute(_id: string, params: { until?: string; reason?: string }) {
      try { const response = await transport.request("loop_wait", { text: params.until?.trim(), note: params.reason?.trim() });
        return response.text?.startsWith("Wait intent registered.") ? stopOnSuccess(response) : resultFromResponse(response); }
      catch (error) { return failureResult("Duo wait failed", error); }
    },
  });
  pi.registerTool({
    executionMode: "sequential",
    name: "duo_ask", label: "Duo Ask",
    description: "LOOP owner Austin: ask Tony for advice. End your turn and wait for the matching reply before further work. Use duo_send for asynchronous notifications.",
    parameters: Type.Object({ question: Type.String() }),
    async execute(_id: string, params: { question: string }) {
      try { return stopOnSuccess(await transport.request("loop_ask", { text: params.question.trim() })); }
      catch (error) { return failureResult("Duo ask failed", error); }
    },
  });
  pi.registerTool({
    name: "duo_reply", label: "Duo Reply",
    description: "LOOP advisor Tony: answer the exact questionId from duo_events. Austin may accept or reject your advice.",
    parameters: Type.Object({ questionId: Type.String(), message: Type.String() }),
    async execute(_id: string, params: { questionId: string; message: string }) {
      try { return resultFromResponse(await transport.request("loop_reply", { questionId: params.questionId.trim(), text: params.message.trim() })); }
      catch (error) { return failureResult("Duo reply failed", error); }
    },
  });
  pi.registerTool({
    executionMode: "sequential",
    name: "duo_finish", label: "Duo Finish",
    description: "LOOP owner Austin: finish with a summary and end your turn. Commit code changes and leave the worktree clean first; Duo safely delivers them before completion. No Tony approval is required; pending consultations must be answered first.",
    parameters: Type.Object({ summary: Type.String() }),
    async execute(_id: string, params: { summary: string }) {
      try { return stopOnSuccess(await transport.request("loop_finish", { note: params.summary.trim() })); }
      catch (error) { return failureResult("Duo finish failed", error); }
    },
  });
}
