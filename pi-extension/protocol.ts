export type AgentName = "Austin" | "Tony";

/**
 * Duo bridge wire-protocol version. Must match `protocol.Version` in the Go
 * runtime (internal/protocol). Independent of the Duo binary version.
 */
export const PROTOCOL_VERSION = 1;

export type DuoMessage = {
  version: typeof PROTOCOL_VERSION;
  type: string;
  requestId?: string;

  sessionId?: string;
  token?: string;

  agent?: string;
  from?: string;
  to?: string;

  text?: string;
  plan?: string;
  note?: string;
  ready?: boolean;

  activity?: string;
  timestamp?: number;

  ok?: boolean;
  state?: string;
};

export type PendingRequest = {
  resolve: (message: DuoMessage) => void;
  reject: (error: Error) => void;
  timer: ReturnType<typeof setTimeout>;
};
