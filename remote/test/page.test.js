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
  // Rules merge by path, so the shared `no-cache` would win over the max-age
  // unless the rule unsets it first. Without that line the long cache is inert
  // and this assertion keeps the file honest about what it does.
  assert.match(favicon, /!\s*Cache-Control/);
  assert.match(favicon, /Cache-Control: public, max-age=/);
});

// A phone that keeps the text after sending it sends the same prompt again as
// soon as the command is acknowledged, and each send carries a fresh id, so the
// host runs it twice. The composer is emptied once the command is built, which
// is also after the only early return that has to keep the text.
test("a sent task leaves the composer empty", () => {
  const build = app.indexOf("const frame = await buildCommand({");
  const clear = app.indexOf('if (kind === "task") els.task.value = "";');
  // The command in flight keeps the text it was sent with, so a command that did
  // not run can give it back instead of asking for a prompt that is gone.
  const queued = app.indexOf("pending = { id, frame, session: model.session, at: Date.now(), text:");
  assert.ok(build >= 0, "the command must still be built before it is sent");
  assert.ok(clear > build, "the composer must be cleared after a task command is built");
  assert.ok(queued > clear, "the composer must be cleared before the command is pending");
});

// The phone can change the workflow mode from the session card. One select is
// the whole control, and the page asks before a switch that would discard the
// round in progress.
test("the session card can switch mode", () => {
  assert.match(page, /<select id="modes"/);
  for (const mode of ["fast", "step", "goal"]) {
    assert.match(page, new RegExp(`<option value="${mode}"`));
  }
  assert.match(app, /command\("switch_mode"/);
  assert.match(app, /needsModeConfirm\(model\)/);
  assert.match(app, /window\.confirm\(/);
});

// Each agent's driver is chosen from what the computer can actually run, so the
// page offers a select per agent instead of asking for a name from memory.
test("the drivers card offers one select per agent", () => {
  for (const agent of ["austin", "tony"]) {
    assert.match(page, new RegExp(`<select id="driver-${agent}"`));
    assert.match(page, new RegExp(`<label for="driver-${agent}"`));
  }
  assert.match(app, /command\("set_driver"/);
  assert.match(app, /driverChangeAllowed\(model\)/);
  assert.match(app, /driver-note/);
});

// The model picker offers the driver's own list, one agent at a time, with a
// filter and a tap to switch. Loading it is a request of its own, because it
// starts the driver's CLI.
test("the models card can load and pick a model", () => {
  assert.match(page, /<select id="model-agent"/);
  assert.match(page, /<button id="model-load"/);
  assert.match(page, /<input id="model-filter" type="search"/);
  assert.match(page, /<ul id="model-list"/);
  assert.match(app, /command\("catalog"/);
  assert.match(app, /command\("set_model"/);
  assert.match(app, /mergeCatalog\(catalog, plain\.catalog\)/);
  assert.match(app, /filterModels\(/);
});

// One rule decides when the command in flight is over, and both triggers use it:
// every state the host publishes, and the bounded wait that keeps a lost answer
// from disabling the page for good.
test("the pending command is settled by the same rule on a state and on the timer", () => {
  const state = app.indexOf('if (frame.kind === "state") {');
  const resolved = app.indexOf("resolvePending();", state);
  const timer = app.indexOf("pendingTimer = setTimeout(", state);
  const fromTimer = app.indexOf("resolvePending();", timer);
  assert.ok(state >= 0 && resolved > state, "a state must resolve the command in flight");
  assert.ok(timer > resolved, "sending a command must arm the bounded wait");
  assert.ok(fromTimer > timer, "the bounded wait must resolve the command in flight");
  // The bounded wait is per command kind: a driver change may take a minute, a
  // task is answered at once.
  assert.match(app, /}, pendingTimeout\(kind\)\);/);
});
