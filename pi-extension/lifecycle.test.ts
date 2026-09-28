import { expect, test } from "bun:test";
import { installLifecycle } from "./lifecycle";

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
