import type { DuoModeSource } from "./prompt";
import type { DuoTransport } from "./transport";

// Query Core immediately before execution, including after adapter/model restart.
// Failed bridge requests block work rather than silently bypassing consultation.
export function installLoopGuard(pi: any, transport: DuoTransport, mode: DuoModeSource) {
  // Pi derives the protocol error flag from tool_result, not the returned object.
  pi.on("tool_result", async (event: any) => {
    if (event.toolName.startsWith("duo_") && event.details?.ok === false) return { isError: true };
  });
  pi.on("tool_call", async (event: any) => {
    if ((typeof mode === "function" ? mode() : mode) !== "loop") return;
    if (["duo_events", "duo_status", "duo_wait", "duo_send", "duo_ask", "duo_reply", "duo_finish"].includes(event.toolName)) return;
    try {
      const response = await transport.request("loop_guard");
      if (!response.ok) return { block: true, reason: response.text || "LOOP work is blocked", terminate: true };
    } catch (error) { return { block: true, reason: String(error), terminate: true }; }
  });
}
