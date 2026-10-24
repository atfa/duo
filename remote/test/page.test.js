import test from "node:test";
import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import { fileURLToPath } from "node:url";

const root = fileURLToPath(new URL("../public/", import.meta.url));
const page = await readFile(`${root}index.html`, "utf8");
const app = await readFile(`${root}app.js`, "utf8");
const headers = await readFile(`${root}_headers`, "utf8");

test("the pairing page loads only same-origin assets", () => {
  assert.match(page, /<script type="module" src="\/app\.js">/);
  assert.match(page, /<link rel="stylesheet" href="\/style\.css">/);
  assert.doesNotMatch(page, /https?:\/\//, "the page must not reference an external origin");
  assert.doesNotMatch(page, /<script(?![^>]*src=)[^>]*>/, "no inline script");
});

test("the page reads the pairing key from the fragment, never the query", () => {
  assert.match(app, /parsePairingKey\(location\.hash\)/);
  assert.doesNotMatch(app, /location\.search/);
});

test("the page renders untrusted text as text, not HTML", () => {
  assert.doesNotMatch(app, /\.innerHTML\s*=/);
  assert.match(app, /textContent/);
});

test("the mobile viewport and noindex are declared for a private pairing page", () => {
  assert.match(page, /name="viewport"/);
  assert.match(page, /name="robots" content="noindex, nofollow"/);
});

test("the asset response carries a strict content security policy", () => {
  assert.match(headers, /Content-Security-Policy: default-src 'none'/);
  assert.match(headers, /connect-src 'self'/);
  assert.match(headers, /frame-ancestors 'none'/);
  assert.match(headers, /Referrer-Policy: no-referrer/);
});

// The page and its scripts are served under fixed names, so a phone that reloads
// after a deploy has to revalidate instead of running the previous app.js for
// minutes: that looks exactly like "the fix did not work". Only the favicon,
// whose bytes never carry behaviour, may sit in the cache.
test("the page and its scripts revalidate on every load", () => {
  const blocks = headers.split(/\n(?=\S)/).map((block) => block.trim()).filter(Boolean);
  const shared = blocks.find((block) => block.startsWith("/*"));
  assert.ok(shared, "the shared block is missing");
  assert.match(shared, /Cache-Control: no-cache/);
  assert.doesNotMatch(shared, /max-age/, "a max-age on the page keeps a stale app.js alive after a deploy");

  const favicon = blocks.find((block) => block.startsWith("/favicon.svg"));
  assert.ok(favicon, "the favicon needs its own cache rule");
  assert.match(favicon, /max-age=/);
});

// A phone that keeps the text after sending it sends the same prompt again as
// soon as the command is acknowledged, and each send carries a fresh id, so the
// host runs it twice. The composer is emptied once the command is built, which
// is also after the only early return that has to keep the text.
test("a sent task leaves the composer empty", () => {
  const build = app.indexOf("const frame = await buildCommand({");
  const clear = app.indexOf('if (kind === "task") els.task.value = "";');
  const queued = app.indexOf("pending = { id, frame };");
  assert.ok(build >= 0, "the command must still be built before it is sent");
  assert.ok(clear > build, "the composer must be cleared after a task command is built");
  assert.ok(queued > clear, "the composer must be cleared before the command is pending");
});
