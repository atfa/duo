// Registrar is the only place the Worker can count anything: isolates are
// stateless, so per-IP and global registration limits have to live in a
// Durable Object. It stores a hash of the caller's IP, never the address.

import { REGISTRATIONS_GLOBAL_PER_HOUR, REGISTRATIONS_PER_HOUR } from "./protocol.js";

export class Registrar {
  constructor(state) {
    this.state = state;
    this.sql = state.storage.sql;
    this.nowMs = () => Date.now();
    this.sql.exec(`CREATE TABLE IF NOT EXISTS hits (
      scope TEXT NOT NULL,
      bucket INTEGER NOT NULL,
      count INTEGER NOT NULL,
      PRIMARY KEY (scope, bucket)
    )`);
  }

  first(query, ...params) {
    const rows = this.sql.exec(query, ...params).toArray();
    return rows.length > 0 ? rows[0] : null;
  }

  async scopeFor(ip) {
    const data = new TextEncoder().encode(`duo-remote-registrar-v1|${ip}`);
    const digest = await crypto.subtle.digest("SHA-256", data);
    return Array.from(new Uint8Array(digest), (b) => b.toString(16).padStart(2, "0")).join("").slice(0, 32);
  }

  bump(scope, bucket) {
    const current = Number(this.first("SELECT count FROM hits WHERE scope = ? AND bucket = ?", scope, bucket)?.count ?? 0);
    this.sql.exec(
      "INSERT INTO hits (scope, bucket, count) VALUES (?, ?, ?) ON CONFLICT(scope, bucket) DO UPDATE SET count = excluded.count",
      scope,
      bucket,
      current + 1,
    );
    return current + 1;
  }

  async fetch(request) {
    let url;
    try {
      url = new URL(request.url);
    } catch {
      return json({ error: "bad_request" }, 400);
    }
    if (url.pathname !== "/check" || request.method !== "POST") return json({ error: "method" }, 405);

    let body;
    try {
      body = await request.json();
    } catch {
      return json({ error: "bad_body" }, 400);
    }
    const ip = typeof body?.ip === "string" && body.ip !== "" ? body.ip : "unknown";
    const scope = await this.scopeFor(ip);
    const now = Math.floor(this.nowMs() / 1000);
    const bucket = Math.floor(now / 3600);

    if (this.bump("_global", bucket) > REGISTRATIONS_GLOBAL_PER_HOUR) return json({ ok: false }, 429);
    if (this.bump(scope, bucket) > REGISTRATIONS_PER_HOUR) return json({ ok: false }, 429);

    this.sql.exec("DELETE FROM hits WHERE bucket < ?", bucket - 1);
    this.state.storage.setAlarm((bucket + 2) * 3600 * 1000);
    return json({ ok: true });
  }

  async alarm() {
    const bucket = Math.floor(Math.floor(this.nowMs() / 1000) / 3600);
    this.sql.exec("DELETE FROM hits WHERE bucket < ?", bucket - 1);
  }
}

function json(value, status = 200) {
  return new Response(JSON.stringify(value), {
    status,
    headers: { "Content-Type": "application/json", "Cache-Control": "no-store" },
  });
}
