import { expect, test } from "bun:test";
import { buildDuoTools } from "./tools";
import { applyDuoContext, buildDuoPrompt } from "./prompt";

test("LOOP tools distinguish notifications, consultations and owner finish", async () => {
  const requests: unknown[] = [];
  const transport = { request: async (type: string, fields: unknown) => {
    requests.push({ type, fields }); return { ok: true, text: "OK" };
  } } as any;
  const tools = buildDuoTools(transport, "Tony");
  const tool = (name: string) => tools.find(t => t.name === name)!;
  await tool("duo_events").execute({ ack: "7" });
  await tool("duo_ask").execute({ question: "strategy?" });
  await tool("duo_reply").execute({ questionId: "8", message: "try B" });
  await tool("duo_finish").execute({ summary: "stopping" });
  expect(requests).toEqual([
    { type: "loop_events", fields: { eventId: "7", through: undefined } },
    { type: "loop_ask", fields: { text: "strategy?" } },
    { type: "loop_reply", fields: { questionId: "8", text: "try B" } },
    { type: "loop_finish", fields: { note: "stopping" } },
  ]);
  expect(tool("duo_finish").input.properties).not.toHaveProperty("approve");
  expect(buildDuoPrompt("Austin", "loop", "/repo", ".")).toContain("task owner");
  expect(buildDuoPrompt("Tony", "loop", "/repo", ".")).toContain("observing advisor");
});

test("opencode checks Core at execution time, even for calls from a previously built context", async () => {
  let blocked = false;
  let executed = 0;
  const base = { bash: { execute: async () => { executed++; return "ran"; } } };
  const input: any = { tools: base };
  applyDuoContext(input, "Austin", "loop", "en", "/repo", ".", async () => {
    if (blocked) throw new Error("waiting for reply");
  });
  blocked = true;
  await expect(input.tools.bash.execute()).rejects.toThrow("waiting for reply");
  expect(executed).toBe(0);
  blocked = false;
  expect(await input.tools.bash.execute()).toBe("ran");
  expect(executed).toBe(1);
  expect(base.bash.execute).not.toBe(input.tools.bash.execute);
});
