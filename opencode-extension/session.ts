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
  // The root session is authoritative. opencode creates child sessions for side
  // tasks (title generation, compaction) whose events carry their own id, and
  // adopting one would report a background helper's output as the agent's.
  private authoritative = false;
  private readonly reported = new Set<string>();

  constructor(private readonly sessionFile: string) {}

  /**
   * Feed an event's `data` (opencode 2.0 events are flat: every `data` carries
   * its `sessionID`, and `session.created` additionally carries `parentID`).
   * Returns true when the event is for our session.
   */
  observe(eventType: string, data: any): boolean {
    if (eventType === "session.created") {
      if (data?.sessionID && !data?.parentID) this.learn(data.sessionID, true);
    }
    // Every event's `data.sessionID` is the session it belongs to; there is no
    // message-id confusion like the pre-2.0 `properties.info.id` fallback.
    const sessionID = data?.sessionID;
    if (typeof sessionID === "string" && sessionID) this.learn(sessionID, false);
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
  private learn(sessionID: string, authoritative: boolean): void {
    if (typeof sessionID !== "string" || !sessionID.startsWith("ses_")) return;
    if (authoritative) {
      // The root session always wins, even over an id learned earlier.
      this.authoritative = true;
    } else if (this.current !== null) {
      // Once a session is known, a stray id from another session must not
      // retarget the bridge.
      return;
    }
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
