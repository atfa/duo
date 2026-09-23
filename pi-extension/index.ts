import { installLifecycle } from "./lifecycle";
import { installDuoPrompt } from "./prompt";
import type { AgentName } from "./protocol";
import { DuoTransport } from "./transport";
import { registerPlanTool } from "./tools/plan";
import { registerProjectStatusTool } from "./tools/project-status";
import { registerSendTool } from "./tools/send";
import { registerStatusTool } from "./tools/status";

const HOST = process.env.DUO_HOST ?? "127.0.0.1";
const PORT = Number(process.env.DUO_PORT ?? "8765");
const AGENT = (process.env.DUO_AGENT ?? "Austin") as AgentName;

export default function (pi: any) {
  if (AGENT !== "Austin" && AGENT !== "Tony") {
    throw new Error(`DUO_AGENT must be Austin or Tony, got: ${AGENT}`);
  }

  const transport = new DuoTransport(AGENT, HOST, PORT);

  installDuoPrompt(pi, AGENT);
  installLifecycle(pi, transport, AGENT);

  registerSendTool(pi, transport);
  registerPlanTool(pi, transport);
  registerStatusTool(pi, transport);
  registerProjectStatusTool(pi, transport);

  pi.registerCommand("duo-test", {
    description: "Test connection to Duo Core",
    handler: async (_args: string, ctx: any) => {
      const ok = transport.send({
        version: 1,
        type: "test",
        agent: AGENT,
        text: "Hello from Pi Duo bridge",
        timestamp: Date.now(),
      });

      ctx.ui.notify(
        ok ? `Duo connected as ${AGENT}` : "Duo Core is not connected",
        ok ? "info" : "error",
      );
    },
  });

  transport.connect();
}
