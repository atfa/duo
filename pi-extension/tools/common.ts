import type { DuoMessage } from "../protocol";

export function resultFromResponse(response: DuoMessage) {
  const parts: string[] = [];
  if (response.text) parts.push(response.text);
  if (response.state) parts.push(response.state);

  return {
    content: [
      {
        type: "text",
        text: parts.join("\n\n") || (response.ok ? "OK" : "Duo operation failed"),
      },
    ],
    details: {
      ok: response.ok ?? false,
      state: response.state,
    },
  };
}

export function failureResult(prefix: string, error: unknown) {
  const message = error instanceof Error ? error.message : String(error);
  return {
    content: [{ type: "text", text: `${prefix}: ${message}` }],
    details: { ok: false },
  };
}
