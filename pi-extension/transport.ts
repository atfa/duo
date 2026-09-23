import net from "node:net";
import type { AgentName, DuoMessage, PendingRequest } from "./protocol";

export class DuoTransport {
  private socket: net.Socket | null = null;
  private connected = false;
  private shuttingDown = false;
  private receiveBuffer = "";
  private reconnectTimer: ReturnType<typeof setTimeout> | null = null;
  private requestSeq = 0;
  private pending = new Map<string, PendingRequest>();
  private handler: ((message: DuoMessage) => void) | null = null;

  constructor(
    private readonly agent: AgentName,
    private readonly host: string,
    private readonly port: number,
    private readonly sessionId: string,
    private readonly token: string,
  ) {}

  setHandler(handler: (message: DuoMessage) => void) {
    this.handler = handler;
  }

  connect() {
    if (this.shuttingDown || this.socket) return;

    const socket = net.createConnection({ host: this.host, port: this.port });
    this.socket = socket;
    socket.setEncoding("utf8");

    socket.on("connect", () => {
      this.connected = true;
      this.send({
        version: 1,
        type: "hello",
        agent: this.agent,
        sessionId: this.sessionId,
        token: this.token,
        timestamp: Date.now(),
      });
    });

    socket.on("data", (chunk: string) => {
      this.receiveBuffer += chunk;
      this.drainBuffer();
    });

    socket.on("error", () => {
      // close handles reconnect
    });

    socket.on("close", () => {
      if (this.socket === socket) this.socket = null;
      this.connected = false;
      this.rejectAllPending("Duo Core connection closed");
      if (!this.shuttingDown) this.scheduleReconnect();
    });
  }

  close() {
    this.shuttingDown = true;
    if (this.reconnectTimer) {
      clearTimeout(this.reconnectTimer);
      this.reconnectTimer = null;
    }
    this.rejectAllPending("Pi session shutting down");
    if (this.socket) {
      this.socket.destroy();
      this.socket = null;
    }
  }

  send(message: DuoMessage): boolean {
    if (!this.socket || !this.connected || this.socket.destroyed) return false;
    this.socket.write(JSON.stringify(message) + "\n");
    return true;
  }

  sendActivity(activity: string) {
    this.send({
      version: 1,
      type: "activity",
      agent: this.agent,
      activity,
      timestamp: Date.now(),
    });
  }

  request(
    type: string,
    payload: Partial<DuoMessage> = {},
    timeoutMs = 5000,
  ): Promise<DuoMessage> {
    const requestId = `${this.agent}-${Date.now()}-${++this.requestSeq}`;

    return new Promise((resolve, reject) => {
      const timer = setTimeout(() => {
        this.pending.delete(requestId);
        reject(new Error(`Duo request timed out: ${type}`));
      }, timeoutMs);

      this.pending.set(requestId, { resolve, reject, timer });

      const ok = this.send({
        version: 1,
        type,
        requestId,
        agent: this.agent,
        timestamp: Date.now(),
        ...payload,
      });

      if (!ok) {
        clearTimeout(timer);
        this.pending.delete(requestId);
        reject(new Error("Duo Core is not connected"));
      }
    });
  }

  private scheduleReconnect() {
    if (this.shuttingDown || this.reconnectTimer) return;
    this.reconnectTimer = setTimeout(() => {
      this.reconnectTimer = null;
      this.connect();
    }, 1000);
  }

  private drainBuffer() {
    while (true) {
      const newline = this.receiveBuffer.indexOf("\n");
      if (newline < 0) return;

      const line = this.receiveBuffer.slice(0, newline).trim();
      this.receiveBuffer = this.receiveBuffer.slice(newline + 1);
      if (!line) continue;

      try {
        const message = JSON.parse(line) as DuoMessage;
        if (message.type === "response" && message.requestId) {
          const pending = this.pending.get(message.requestId);
          if (!pending) continue;
          clearTimeout(pending.timer);
          this.pending.delete(message.requestId);
          pending.resolve(message);
          continue;
        }
        this.handler?.(message);
      } catch (error) {
        console.error("[duo] invalid message from server:", error);
      }
    }
  }

  private rejectAllPending(reason: string) {
    for (const [id, item] of this.pending) {
      clearTimeout(item.timer);
      item.reject(new Error(reason));
      this.pending.delete(id);
    }
  }
}
