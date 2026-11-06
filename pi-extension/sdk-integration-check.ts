// Optional integration check against an installed Pi SDK:
// DUO_PI_SDK=/path/to/@earendil-works/pi-coding-agent bun pi-extension/sdk-integration-check.ts
import { mkdtemp, rm, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join, resolve } from "node:path";

const sdk = process.env.DUO_PI_SDK;
if (!sdk) { console.log("SKIP: set DUO_PI_SDK to the installed pi-coding-agent package"); process.exit(0); }
const loader = await import(join(sdk, "dist/core/extensions/loader.js"));
const { ExtensionRunner } = await import(join(sdk, "dist/core/extensions/runner.js"));
const { wrapRegisteredTool } = await import(join(sdk, "dist/core/extensions/wrapper.js"));
const { runAgentLoop } = await import(join(sdk, "node_modules/@earendil-works/pi-agent-core/dist/agent-loop.js"));
const dir = await mkdtemp(join(tmpdir(), "duo-pi-sdk-"));
try {
  const entry = join(dir, "extension.ts");
  await writeFile(entry, `import { registerLoopTools } from ${JSON.stringify(resolve("pi-extension/tools/loop.ts"))};\nexport default (pi) => registerLoopTools(pi, globalThis.duoTestTransport);\n`);
  let guardCalls = 0;
  (globalThis as any).duoTestTransport = { request: async (type: string) => {
    if (type === "loop_guard") { guardCalls++; return { ok: false, text: "consultation pending" }; }
    if (type === "loop_reply") return { ok: false, text: "not advisor" };
    return { ok: true, text: "Wait intent registered. End this turn" };
  } };
  const loaded = await loader.loadExtensions([entry], process.cwd());
  if (loaded.errors.length) throw new Error(JSON.stringify(loaded.errors));
  const extension = loaded.extensions[0];
  const runner = new ExtensionRunner([extension], loaded.runtime, process.cwd(), {}, {});
  const blocked = await runner.emitToolCall({ type: "tool_call", toolName: "edit", input: {} });
  if (guardCalls !== 1 || !blocked?.block || !blocked.terminate) throw Error("real Pi runner did not block guarded action");
  const reply = await extension.tools.get("duo_reply").definition.execute("r", { questionId: "1", message: "x" });
  const failure = await runner.emitToolResult({ type: "tool_result", toolName: "duo_reply", content: reply.content, details: reply.details, isError: false });
  if (!failure?.isError) throw Error("real Pi runner did not mark failure");
  const wait = wrapRegisteredTool(extension.tools.get("duo_wait"), runner);
  let calls = 0;
  const message = { role: "assistant", content: [{ type: "toolCall", id: "w", name: "duo_wait", arguments: {} }], api: "openai-completions", provider: "test", model: "test", usage: { input: 0, output: 0, cacheRead: 0, cacheWrite: 0, totalTokens: 0, cost: { input: 0, output: 0, cacheRead: 0, cacheWrite: 0, total: 0 } }, stopReason: "toolUse", timestamp: Date.now() };
  const stream = async () => { if (++calls > 1) throw Error("unexpected additional model request"); return { async *[Symbol.asyncIterator]() {}, result: async () => message }; };
  const events: any[] = [];
  await runAgentLoop([{ role: "user", content: [{ type: "text", text: "test" }], timestamp: Date.now() }], { messages: [], tools: [wait] }, { model: { provider: "test" }, convertToLlm: async (messages: any) => messages }, async (event: any) => { events.push(event); }, undefined, stream);
  if (calls !== 1 || !events.some(e => e.type === "agent_end")) throw Error("wait did not settle");
  let edited = false;
  const ask = wrapRegisteredTool(extension.tools.get("duo_ask"), runner);
  const edit = { name: "edit", label: "edit", description: "edit", parameters: { type: "object", properties: {} }, execute: async () => { edited = true; return { content: [] }; } };
  const batch = { ...message, content: [
    { type: "toolCall", id: "a", name: "duo_ask", arguments: { question: "advice?" } },
    { type: "toolCall", id: "e", name: "edit", arguments: {} },
  ] };
  calls = 0;
  const batchStream = async () => { if (++calls > 1) throw Error("blocked batch continued"); return { async *[Symbol.asyncIterator]() {}, result: async () => batch }; };
  await runAgentLoop([{ role: "user", content: [{ type: "text", text: "test" }], timestamp: Date.now() }], { messages: [], tools: [ask, edit] }, {
    model: { provider: "test" }, convertToLlm: async (messages: any) => messages,
    beforeToolCall: async ({ toolCall, args }: any) => runner.emitToolCall({ type: "tool_call", toolName: toolCall.name, toolCallId: toolCall.id, input: args }),
  }, async () => {}, undefined, batchStream);
  if (edited || calls !== 1) throw Error("ask and edit batch escaped the guard");
  console.log("PASS: installed Pi loader, guard, error flag, wait termination, and ask/edit batch");
} finally { delete (globalThis as any).duoTestTransport; await rm(dir, { recursive: true, force: true }); }
