import { Type } from "@earendil-works/pi-ai";
import { DuoTransport } from "../transport";
import { failureResult, resultFromResponse } from "./common";

export function registerSendTool(pi: any, transport: DuoTransport) {
  pi.registerTool({
    name: "duo_send",
    label: "Duo Send",
    description:
      "Send an important live coordination message to your peer agent while both agents continue working.",
    parameters: Type.Object({
      message: Type.String({
        description:
          "Concise information useful to the peer: finding, question, warning, conflict, proposal, or interface change.",
      }),
    }),
    promptSnippet: "duo_send: send an important live message to your peer agent",
    promptGuidelines: [
      "Use duo_send for meaningful peer coordination, not routine progress updates.",
      "After duo_send, continue your own work unless you genuinely need the peer's answer first.",
    ],
    async execute(_toolCallId: string, params: { message: string }) {
      const text = params.message.trim();
      if (!text) {
        return {
          content: [{ type: "text", text: "Message was empty and was not sent." }],
          details: { ok: false },
        };
      }

      try {
        return resultFromResponse(await transport.request("peer_message", { text }));
      } catch (error) {
        return failureResult("Duo send failed", error);
      }
    },
  });
}
