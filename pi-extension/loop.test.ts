import { expect, test } from "bun:test";
import { parseDuoMode } from "./mode";
import { buildDuoPrompt } from "./prompt";

test("LOOP prompts persist waits and avoid delivery gates for both agents", () => {
  expect(parseDuoMode("loop")).toBe("loop");
  for (const agent of ["Austin", "Tony"] as const) {
    const prompt = buildDuoPrompt(agent, "loop", "/repo", ".");
    expect(prompt).toContain("duo_wait");
    expect(prompt).toContain("duo_finish");
    expect(prompt).toContain("ack");
    expect(prompt).not.toContain("RUNNING -> VERIFY");
  }
});
