import { tool } from "@opencode-ai/plugin";
import { isToolEnabledForMode, type DuoMode } from "./mode";
import type { AgentName, DuoMessage } from "./protocol";
import type { DuoTransport } from "./transport";

const z = tool.schema;

/**
 * Duo's six coordination tools, registered as opencode tools.
 *
 * Mode gating is applied here so the model is never offered a tool the core will
 * refuse. Duo Core enforces the same rules independently; this is presentation,
 * not the security boundary.
 */
export function buildDuoTools(
  transport: DuoTransport,
  agent: AgentName,
  mode: DuoMode,
): Record<string, any> {
  const tools: Record<string, any> = {};

  const ok = (response: DuoMessage): string => {
    const parts: string[] = [];
    if (response.text) parts.push(response.text);
    if (response.state) parts.push(response.state);
    return parts.join("\n\n") || (response.ok ? "OK" : "Duo operation failed");
  };

  const failed = async (prefix: string, error: unknown): Promise<string> => {
    const message = error instanceof Error ? error.message : String(error);
    return `${prefix}: ${message}`;
  };

  tools.duo_send = tool({
    description:
      "Send an important live coordination message to your peer agent while both agents continue working.",
    args: {
      message: z.string().describe(
        "Concise information useful to the peer: finding, question, warning, conflict, proposal, or interface change.",
      ),
    },
    async execute(args) {
      const text = args.message.trim();
      if (!text) return "Message was empty and was not sent.";
      try {
        return ok(await transport.request("peer_message", { text }));
      } catch (error) {
        return failed("Duo send failed", error);
      }
    },
  });

  tools.duo_status = tool({
    description:
      "Read authoritative Duo phase/plan/signatures plus both Git worktree branches, paths, HEADs, cleanliness and ahead counts.",
    args: {},
    async execute() {
      try {
        return ok(await transport.request("get_status"));
      } catch (error) {
        return failed("Duo status failed", error);
      }
    },
  });

  tools.duo_set_status = tool({
    description:
      mode === "goal"
        ? "Set your own sign-off state for the current Duo phase. Duo Core advances only when both Austin and Tony have valid, non-stale signatures."
        : "FAST mode: Austin-only readiness. Austin uses ready=true to request independent verification of the committed, clean HEAD; Tony cannot sign and must use duo_set_verification instead.",
    args: {
      ready: z.boolean().describe(
        mode === "goal"
          ? "true = sign current phase; false = revoke your signature because work or objections remain."
          : "FAST mode: Austin-only. ready=true requests independent verification of your committed, clean HEAD; ready=false withdraws a pending request. It is not a phase sign-off.",
      ),
      note: z.string().optional().describe("Optional concise reason or completion note."),
    },
    async execute(args) {
      try {
        return ok(
          await transport.request("set_status", { ready: args.ready, note: args.note?.trim() }),
        );
      } catch (error) {
        return failed("Duo status update failed", error);
      }
    },
  });

  if (isToolEnabledForMode("plan", mode)) {
    tools.duo_set_plan = tool({
      description:
        "Create or replace the shared Duo work plan during PLAN. Every update creates a new version and invalidates both agents' signatures.",
      args: {
        plan: z
          .string()
          .describe(
            "The complete shared plan, including Austin/Tony responsibilities and intended review responsibilities.",
          ),
      },
      async execute(args) {
        const plan = args.plan.trim();
        if (!plan) return "Plan was empty and was not sent.";
        try {
          return ok(await transport.request("set_plan", { plan }));
        } catch (error) {
          return failed("Duo set plan failed", error);
        }
      },
    });
  }

  if (isToolEnabledForMode("verification", mode) && agent === "Tony") {
    tools.duo_set_verification = tool({
      description:
        "FAST mode only. Report the result of independently verifying Austin's exact current commit: passed, or issue_found with a concrete note.",
      args: {
        result: z
          .enum(["passed", "issue_found"])
          .describe(
            "passed = the verified commit is acceptable; issue_found = a concrete defect or risk blocks it.",
          ),
        note: z
          .string()
          .optional()
          .describe(
            "Required with issue_found: the concrete issue and the fix Austin must make.",
          ),
      },
      async execute(args) {
        if (args.result !== "passed" && args.result !== "issue_found") {
          return "result must be passed or issue_found";
        }
        const note = args.note?.trim() ?? "";
        if (args.result === "issue_found" && !note) {
          return "issue_found requires a concrete note";
        }
        try {
          return ok(await transport.request("set_verification", { verification: args.result, note }));
        } catch (error) {
          return failed("Duo verification failed", error);
        }
      },
    });
  }

  if (isToolEnabledForMode("escalate", mode)) {
    tools.duo_escalate = tool({
      description:
        "FAST mode only. Dynamically upgrade the session from Fast mode to Goal mode. Transitions session to PLAN phase and initializes a shared co-design plan while preserving all commits already made.",
      args: {
        reason: z
          .string()
          .describe(
            "Required: explain why this task needs Goal-mode co-design or dual implementation (e.g. unexpected architecture complexity, high-risk refactoring).",
          ),
      },
      async execute(args) {
        const reason = args.reason.trim();
        if (!reason) return "reason is required";
        try {
          return ok(await transport.request("escalate", { note: reason }));
        } catch (error) {
          return failed("Duo escalate failed", error);
        }
      },
    });
  }

  return tools;
}
