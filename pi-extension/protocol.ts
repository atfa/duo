export type AgentName = "Austin" | "Tony";

/**
 * Duo bridge wire-protocol version. Must match `protocol.Version` in the Go
 * runtime (internal/protocol). Independent of the Duo binary version.
 */
export const PROTOCOL_VERSION = 1;

export type DuoMessageType =
  | "hello" | "test" | "activity" | "assistant_message" | "agent_error"
  | "peer_message" | "steer" | "duo_notice" | "harness_prompt" | "human_prompt"
  | "resume_prompt" | "set_plan" | "set_status" | "get_status" | "response"
  | "set_verification" | "set_model" | "model_state" | "cycle_thinking"
  | "thinking_state";

/**
 * Structured verification result used by FAST mode. Austin is the driver; Tony
 * is the independent verifier and reports one of these for the exact Austin
 * commit under review. Must match `project.VerificationResult` in Go.
 */
export type VerificationResult = "passed" | "issue_found";

export type DuoMessage = {
  version: typeof PROTOCOL_VERSION;
  type: DuoMessageType;
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
  verification?: VerificationResult;

  /** Model switching: target/reported model and thinking level. */
  provider?: string;
  model?: string;
  thinking?: string;

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
