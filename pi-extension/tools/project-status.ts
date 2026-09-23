import { Type } from "@earendil-works/pi-ai";
import { DuoTransport } from "../transport";
import { failureResult, resultFromResponse } from "./common";

export function registerProjectStatusTool(pi: any, transport: DuoTransport) {
  pi.registerTool({
    name: "duo_status",
    label: "Duo Status",
    description:
      "Read authoritative Duo phase/plan/signatures plus both Git worktree branches, paths, HEADs, cleanliness and ahead counts.",
    parameters: Type.Object({}),
    promptSnippet: "duo_status: read current Duo project and workspace state",
    async execute() {
      try {
        return resultFromResponse(await transport.request("get_status"));
      } catch (error) {
        return failureResult("Duo status failed", error);
      }
    },
  });
}
