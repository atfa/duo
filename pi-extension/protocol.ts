export type AgentName = "Austin" | "Tony";

export type DuoMessage = {
  version: 1;
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
