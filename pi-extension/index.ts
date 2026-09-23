import { installLifecycle } from "./lifecycle";
import { installDuoPrompt } from "./prompt";
import type { AgentName } from "./protocol";
import { DuoTransport } from "./transport";
import { registerPlanTool } from "./tools/plan";
import { registerProjectStatusTool } from "./tools/project-status";
import { registerSendTool } from "./tools/send";
import { registerStatusTool } from "./tools/status";

export default function (pi: any) {
  if (process.env.DUO_ACTIVE !== "1") return;

  const agent = process.env.DUO_AGENT;
  const host = process.env.DUO_HOST;
  const port = Number(process.env.DUO_PORT);
  const sessionId = process.env.DUO_SESSION;
  const token = process.env.DUO_TOKEN;

  if (!host || !Number.isInteger(port) || port <= 0 || !sessionId || !token) {
    throw new Error(
      "DUO_HOST, DUO_PORT, DUO_SESSION, and DUO_TOKEN are required when DUO_ACTIVE=1",
    );
  }
  const AGENT = agent as AgentName;
  if (AGENT !== "Austin" && AGENT !== "Tony") {
    throw new Error(`DUO_AGENT must be Austin or Tony, got: ${AGENT}`);
  }

  const transport = new DuoTransport(AGENT, host, port, sessionId, token);

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
