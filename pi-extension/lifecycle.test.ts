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
  // The verdict is relayed by Duo Core, so Tony must not duplicate it with duo_send.
  expect(tony).toContain("do not restate the same verdict with duo_send");
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

// The Duo work preview reads what an agent is doing from these activity
// details, so the payload shape is part of the bridge contract.
test("tool and stream activity carry the preview detail", async () => {
  const handlers = new Map<string, (event: any) => Promise<void>>();
  const sent: any[] = [];
  installLifecycle(
    { on: (name: string, handler: (event: any) => Promise<void>) => handlers.set(name, handler) },
    {
      setHandler() {},
      sendActivity: (activity: string, extra: any = {}) => sent.push({ activity, ...extra }),
      send: (message: any) => sent.push(message),
    } as any,
    "Austin",
  );

  await handlers.get("tool_execution_start")!({ toolName: "bash", args: { command: "go test ./...\n" } });
  expect(sent[0]).toMatchObject({ activity: "tool_start", tool: "bash", detail: "go test ./..." });

  await handlers.get("tool_execution_end")!({ toolName: "bash", isError: true, result: "exit status 1" });
  expect(sent[1]).toMatchObject({ activity: "tool_error", tool: "bash", detail: "exit status 1" });

  // A successful end must not replace the arguments captured at tool_start.
  await handlers.get("tool_execution_end")!({ toolName: "bash", isError: false, result: "ok" });
  expect(sent[2]).toMatchObject({ activity: "tool_end", tool: "bash", detail: "" });

  await handlers.get("message_update")!({
    message: { role: "assistant", content: [{ type: "text", text: "first line\nsecond line" }] },
  });
  expect(sent[3]).toMatchObject({ activity: "stream", detail: "first line second line" });
});
