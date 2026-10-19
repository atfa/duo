import { isToolEnabledForMode, type DuoMode } from "./mode";
import type { AgentName, DuoMessage } from "./protocol";
import type { DuoTransport } from "./transport";

/**
 * A tool definition in opencode 2.0's `tool.transform` editor format: a plain
 * JSON-schema `input` plus an `execute` that must resolve to `{ content }`
 * (a structured result would require a declared output schema).
 */
export interface DuoToolDefinition {
  name: string;
  description: string;
  input: Record<string, any>;
  execute: (input: any) => Promise<{ content: string }> | { content: string };
}

const str = (description?: string) =>
  description ? { type: "string", description } : { type: "string" };

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
): DuoToolDefinition[] {
  const tools: DuoToolDefinition[] = [];

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

  /** Wraps a result string in the `{ content }` shape opencode expects. */
  const withContent = (run: (args: any) => Promise<string>) =>
    (async (args: any) => ({ content: await run(args) }));

  const add = (
    name: string,
    description: string,
    input: Record<string, any>,
    run: (args: any) => Promise<string>,
  ) => {
    tools.push({ name, description, input, execute: withContent(run) });
  };

  add(
    "duo_send",
    "Send an important live coordination message to your peer agent while both agents continue working.",
    {
      type: "object",
      properties: {
        message: str(
          "Concise information useful to the peer: finding, question, warning, conflict, proposal, or interface change.",
        ),
      },
      required: ["message"],
    },
    async (args) => {
      const text = args.message.trim();
      if (!text) return "Message was empty and was not sent.";
      try {
        return ok(await transport.request("peer_message", { text }));
      } catch (error) {
        return failed("Duo send failed", error);
      }
    },
  );

  add(
    "duo_status",
    "Read authoritative Duo phase/plan/signatures plus both Git worktree branches, paths, HEADs, cleanliness and ahead counts.",
    { type: "object", properties: {} },
    async () => {
      try {
        return ok(await transport.request("get_status"));
      } catch (error) {
        return failed("Duo status failed", error);
      }
    },
  );

  add(
    "duo_set_status",
    mode === "fast"
      ? "FAST mode: Austin-only readiness. Austin uses ready=true to request independent verification of the committed, clean HEAD; Tony cannot sign and must use duo_set_verification instead."
      : mode === "step"
        ? "STEP mode: sign the phase that needs dual sign-off — the step queue in PLAN, and the final step in INTEGRATE. Duo Core advances a step after Tony's verification passes on its exact HEAD; the queue cursor is Duo's, never yours."
        : "Set your own sign-off state for the current Duo phase. Duo Core advances only when both Austin and Tony have valid, non-stale signatures.",
    {
      type: "object",
      properties: {
        ready: {
          type: "boolean",
          description:
            mode === "fast"
              ? "FAST mode: Austin-only. ready=true requests independent verification of your committed, clean HEAD; ready=false withdraws a pending request. It is not a phase sign-off."
              : mode === "step"
                ? "true = sign the current STEP gate (approve the queue in PLAN, request review of your committed HEAD in EXECUTE, accept the final step in INTEGRATE); false = revoke."
                : "true = sign current phase; false = revoke your signature because work or objections remain.",
        },
        note: str("Optional concise reason or completion note."),
      },
      required: ["ready"],
    },
    async (args) => {
      try {
        return ok(
          await transport.request("set_status", { ready: args.ready, note: args.note?.trim() }),
        );
      } catch (error) {
        return failed("Duo status update failed", error);
      }
    },
  );

  if (isToolEnabledForMode("plan", mode)) {
    add(
      "duo_set_plan",
      "Create or replace the shared Duo work plan during PLAN. Every update creates a new version and invalidates both agents' signatures.",
      {
        type: "object",
        properties: {
          plan: str(
            "The complete shared plan, including Austin/Tony responsibilities and intended review responsibilities.",
          ),
        },
        required: ["plan"],
      },
      async (args) => {
        const plan = args.plan.trim();
        if (!plan) return "Plan was empty and was not sent.";
        try {
          return ok(await transport.request("set_plan", { plan }));
        } catch (error) {
          return failed("Duo set plan failed", error);
        }
      },
    );
  }

  if (isToolEnabledForMode("steps", mode)) {
    add(
      "duo_set_steps",
      "STEP mode. Publish the step queue in PLAN, or extend it later with append=true. Send a markdown checkbox list, one step per line, using '- [ ]' for every step: Duo owns the ticks. A PLAN update resets both agents' signatures; after PLAN the queue can only grow.",
      {
        type: "object",
        properties: {
          steps: str(
            "The queue as a markdown checkbox list, e.g. '- [ ] add ModeStep\\n- [ ] land per-step delivery'. Send the whole list, not a patch.",
          ),
          append: {
            type: "boolean",
            description:
              "true = add these steps to the END of the existing queue instead of replacing it. This is the only way to change the queue after PLAN, and it cannot disturb steps already landed.",
          },
        },
        required: ["steps"],
      },
      async (args) => {
        const steps = args.steps.trim();
        if (!steps) return "Steps were empty and were not sent.";
        try {
          return ok(
            await transport.request("set_steps", {
              steps,
              // The coordinator reads an "append" marker to allow growing the
              // queue after PLAN; without it a mid-loop edit is rejected outright.
              text: args.append === true ? "append" : "",
            }),
          );
        } catch (error) {
          return failed("Duo set steps failed", error);
        }
      },
    );
  }

  if (isToolEnabledForMode("verification", mode) && agent === "Tony") {
    add(
      "duo_set_verification",
      mode === "step"
        ? "STEP mode. Report the result of independently verifying the current step's exact Austin commit: passed lands the step on the base branch and advances the queue; issue_found returns the step to EXECUTE."
        : "FAST mode only. Report the result of independently verifying Austin's exact current commit: passed, or issue_found with a concrete note.",
      {
        type: "object",
        properties: {
          result: {
            type: "string",
            enum: ["passed", "issue_found"],
            description:
              "passed = the verified commit is acceptable; issue_found = a concrete defect or risk blocks it.",
          },
          note: str(
            "Required with issue_found: the concrete issue and the fix Austin must make.",
          ),
        },
        required: ["result"],
      },
      async (args) => {
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
    );
  }

  if (isToolEnabledForMode("escalate", mode)) {
    add(
      "duo_escalate",
      "FAST mode only. Dynamically upgrade the session from Fast mode to Goal mode. Transitions session to PLAN phase and initializes a shared co-design plan while preserving all commits already made.",
      {
        type: "object",
        properties: {
          reason: str(
            "Required: explain why this task needs Goal-mode co-design or dual implementation (e.g. unexpected architecture complexity, high-risk refactoring).",
          ),
        },
        required: ["reason"],
      },
      async (args) => {
        const reason = args.reason.trim();
        if (!reason) return "reason is required";
        try {
          return ok(await transport.request("escalate", { note: reason }));
        } catch (error) {
          return failed("Duo escalate failed", error);
        }
      },
    );
  }

  if (isToolEnabledForMode("restart", mode)) {
    add(
      "duo_restart_duo",
      "STEP mode only. Ask Duo to stop so the launcher can resume this session on a newly installed binary. Both agents are killed and restarted; the durable step queue continues at the same step. Use it between steps, as the last action of a round, not while a step is under review.",
      {
        type: "object",
        properties: {
          reason: str(
            "Why the restart is wanted now, e.g. the step landed a change to Duo itself.",
          ),
          install: {
            type: "boolean",
            description:
              "true = also build and install from the delivered branch before resuming. Omit or false to restart on the binary already installed.",
          },
        },
        required: ["reason"],
      },
      async (args) => {
        const reason = args.reason.trim();
        if (!reason) return "a reason is required";
        try {
          return ok(
            await transport.request("restart_duo", { note: reason, install: args.install === true }),
          );
        } catch (error) {
          return failed("Duo restart failed", error);
        }
      },
    );
  }

  return tools;
}
