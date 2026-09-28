import { expect, test } from "bun:test";
import { installLifecycle } from "./lifecycle";
import { isToolEnabledForMode, parseDuoMode } from "./mode";
import { buildDuoPrompt, installDuoPrompt } from "./prompt";

test("compaction abort does not report an agent error", async () => {
  const handlers = new Map<string, (event: any) => Promise<void>>();
  const sent: any[] = [];
  installLifecycle(
    { on: (name: string, handler: (event: any) => Promise<void>) => handlers.set(name, handler) },
    { setHandler() {}, sendActivity() {}, send: (message: any) => sent.push(message) } as any,
    "Austin",
  );

  const messageEnd = handlers.get("message_end")!;
  await messageEnd({ message: { role: "assistant", stopReason: "error", errorMessage: "This operation was aborted" } });
  expect(sent).toEqual([]);

  await messageEnd({ message: { role: "assistant", stopReason: "error", errorMessage: "request failed" } });
  expect(sent).toHaveLength(1);
  expect(sent[0]).toMatchObject({ type: "agent_error", agent: "Austin", text: "request failed" });
});

test("an unknown DUO_MODE falls back to the conservative goal workflow", () => {
  expect(parseDuoMode(undefined)).toBe("goal");
  expect(parseDuoMode("")).toBe("goal");
  expect(parseDuoMode("goal")).toBe("goal");
  expect(parseDuoMode("turbo")).toBe("goal");
  expect(parseDuoMode("fast")).toBe("fast");
  expect(parseDuoMode(" FAST ")).toBe("fast");
});

test("mode gates the shared-plan and verification tools", () => {
  expect(isToolEnabledForMode("plan", "goal")).toBe(true);
  expect(isToolEnabledForMode("plan", "fast")).toBe(false);
  expect(isToolEnabledForMode("verification", "fast")).toBe(true);
  expect(isToolEnabledForMode("verification", "goal")).toBe(false);
});

test("FAST makes Tony a read-only verifier and never offers the shared plan", () => {
  const tony = buildDuoPrompt("Tony", "fast", "/repo", ".");
  expect(tony).toContain("RUNNING -> VERIFY -> DONE");
  expect(tony).toContain("READ-ONLY");
  expect(tony).toContain("duo_set_verification");
  expect(tony).not.toContain("maintain one complete shared plan");
  expect(tony).not.toContain("INTEGRATE");
});

test("FAST makes Austin the driver and the sole delivered branch", () => {
  const austin = buildDuoPrompt("Austin", "fast", "/repo", ".");
  expect(austin).toContain("DRIVER ROLE");
  expect(austin).toContain("only branch Duo ever delivers");
  expect(austin).toContain("duo_set_status");
  expect(austin).not.toContain("maintain one complete shared plan");
});

test("GOAL keeps the full negotiated workflow", () => {
  const tony = buildDuoPrompt("Tony", "goal", "/repo", ".");
  expect(tony).toContain("PLAN -> EXECUTE -> REVIEW -> INTEGRATE -> DONE");
  expect(tony).toContain("duo_set_plan");
  expect(tony).toContain("INTEGRATE");
  expect(tony).not.toContain("READ-ONLY");
  expect(tony).not.toContain("RUNNING -> VERIFY");
});

test("installDuoPrompt publishes the mode selected for this session", async () => {
  const handlers = new Map<string, (event: any) => Promise<void>>();
  const sections: Record<string, string> = {};
  installDuoPrompt(
    { on: (name: string, handler: (event: any) => Promise<void>) => handlers.set(name, handler) },
    "Tony",
    parseDuoMode("fast"),
  );

  await handlers.get("before_agent_start")!({ systemPromptOptions: { sections } });
  expect(sections["duo"]).toContain("READ-ONLY");
  expect(sections["duo"]).toContain("duo_set_verification");
  expect(sections["duo"]).not.toContain("maintain one complete shared plan");
});
