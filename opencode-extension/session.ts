import fs from "node:fs";

/**
 * Tracks which opencode session this plugin instance drives.
 *
 * opencode assigns session ids server-side, so the id cannot be chosen up front
 * the way pi's `--session-id` can. It becomes observable two ways:
 *
 *  - a fresh launch emits `session.created` for the root session (no parentID)
 *  - a resumed launch emits no `session.created`, but every later event carries
 *    the sessionID
 *
 * Child sessions created for side tasks (title generation, compaction) report a
 * parentID, so they are ignored: Duo must follow the root conversation.
 */
export class SessionTracker {
  private current: string | null = null;
  private readonly reported = new Set<string>();

  constructor(private readonly sessionFile: string) {}

  /** Feed an event's `properties`; returns true when the event is for our session. */
  observe(eventType: string, properties: any): boolean {
    if (eventType === "session.created") {
      const info = properties?.info;
      if (info?.id && !info?.parentID) this.learn(info.id);
    }
    const sessionID = properties?.sessionID ?? properties?.info?.id;
    if (sessionID) this.learn(sessionID);
    if (!this.current) return false;
    // Before the id is known, stay quiet rather than guessing.
    return sessionID === undefined || sessionID === this.current;
  }

  get sessionID(): string | null {
    return this.current;
  }

  /**
   * Persist the id where Duo Core can find it. Duo persists it with the session
   * snapshot and passes it back as `--session` on the next run, which is what
   * makes the agent's conversation survive a restart.
   */
  private learn(sessionID: string): void {
    if (typeof sessionID !== "string" || !sessionID.startsWith("ses_")) return;
    if (this.current === sessionID) return;
    this.current = sessionID;
    if (!this.sessionFile || this.reported.has(sessionID)) return;
    try {
      fs.writeFileSync(this.sessionFile, sessionID + "\n");
      this.reported.add(sessionID);
    } catch {
      // A missing session file only costs identity persistence, never the run.
    }
  }
}
