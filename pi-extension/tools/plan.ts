import { Type } from "@earendil-works/pi-ai";
import { isToolEnabledForMode, type DuoMode } from "../mode";
import { DuoTransport } from "../transport";
import { failureResult, resultFromResponse } from "./common";

export function registerPlanTool(pi: any, transport: DuoTransport, mode: DuoMode) {
  let registered = false;
  const register = () => {
    if (registered) return;
    registered = true;

    pi.registerTool({
      name: "duo_set_plan",
      label: "Duo Set Plan",
      description:
        "Create or replace the shared Duo work plan during PLAN. Every update creates a new version and invalidates both agents' signatures.",
      parameters: Type.Object({
        plan: Type.String({
          description:
            "The complete shared plan, including Austin/Tony responsibilities and intended review responsibilities.",
        }),
      }),
      promptSnippet: "duo_set_plan: publish a new version of the shared work plan",
      promptGuidelines: [
        "Use duo_set_plan only in PLAN and always send the complete current plan, not a patch.",
        "A plan update resets both approvals, so do not churn versions for trivial wording changes.",
      ],
      async execute(_toolCallId: string, params: { plan: string }) {
        try {
          return resultFromResponse(
            await transport.request("set_plan", { plan: params.plan.trim() }),
          );
        } catch (error) {
          return failureResult("Duo set plan failed", error);
        }
      },
    });
  };

  // GOAL mode only at startup: FAST has no shared plan.
  if (isToolEnabledForMode("plan", mode)) {
    register();
  }

  // If session escalates to Goal, enable duo_set_plan dynamically.
  transport.addHandler((msg) => {
    if (
      msg.type === "escalate" ||
      (msg.type === "duo_notice" && msg.text?.includes("[Duo Mode Escalation]"))
    ) {
      register();
    }
  });
}
