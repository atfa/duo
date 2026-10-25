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

// The page is one scrolling column with the composer pinned to the bottom, so a
// thumb reaches the message box without scrolling and the last card is never
// hidden behind it. The tabs swap panels in place: no route, no reload.
function panel(name) {
  const start = page.indexOf(`<section id="panel-${name}"`);
  assert.ok(start >= 0, `panel-${name} is missing`);
  const end = page.indexOf("</section>", start);
  assert.ok(end > start, `panel-${name} is not closed`);
  return page.slice(start, end);
}

test("the page is three tabs, one panel each", () => {
  assert.match(page, /<div class="tabs" role="tablist"/);
  for (const name of ["session", "activity", "settings"]) {
    assert.match(page, new RegExp(`<button[^>]*id="tab-${name}"[^>]*role="tab"[^>]*aria-controls="panel-${name}"`));
    assert.match(page, new RegExp(`<section[^>]*id="panel-${name}"[^>]*role="tabpanel"[^>]*aria-labelledby="tab-${name}"`));
    // A panel is reachable from the keyboard: the Activity panel has no focusable
    // child of its own, so a reader would otherwise land nowhere after the tab.
    assert.match(page, new RegExp(`<section[^>]*id="panel-${name}"[^>]*tabindex="0"`));
  }
  // Session is the default tab; the other two are hidden until they are picked.
  assert.match(page, /<button[^>]*id="tab-session"[^>]*aria-selected="true"/);
  assert.match(page, /<section[^>]*id="panel-activity"[^>]*hidden/);
  assert.match(page, /<section[^>]*id="panel-settings"[^>]*hidden/);
  assert.doesNotMatch(page, /<section[^>]*id="panel-session"[^>]*hidden/);
});

test("the composer sits outside the scrolling panels", () => {
  assert.match(page, /<footer class="dock">/);
  const main = page.indexOf("</main>");
  assert.ok(main > 0, "the panels are still inside main");
  assert.ok(page.indexOf('id="composer"') > main, "the composer is docked, not the last card");
  assert.ok(page.indexOf('id="sent"') < page.indexOf('id="composer"'), "the answer is next to the composer");
  assert.match(page, /<textarea id="task" rows="2"/);
});

// Everything that is touched once a session is switched or a model is picked
// costs a scroll on every visit if it lives in the main column.
test("the rare controls live in the Settings tab", () => {
  const settings = panel("settings");
  for (const id of ["modes", "newsess", "driver-austin", "driver-tony", "driver-note", "model-agent", "model-load", "model-status", "model-filter", "model-list", "room"]) {
    assert.match(settings, new RegExp(`id="${id}"`), `${id} belongs in Settings`);
  }
  assert.doesNotMatch(panel("session"), /id="modes"/, "the mode switch does not compete with the status view");
});

test("the session tab shows the step, the agents, the observer and the plan", () => {
  const session = panel("session");
  for (const id of ["step-title", "agents", "observer", "refresh", "plan"]) {
    assert.match(session, new RegExp(`id="${id}"`), `${id} belongs in Session`);
  }
  assert.match(panel("activity"), /id="activity"/);
});

// The header keeps a short counter and a progress bar, because the long step
// string wraps a four-up vitals strip and pushes the tabs down the screen.
test("the sticky header carries the short vitals and the step progress", () => {
  const header = page.slice(page.indexOf('<header class="topbar"'), page.indexOf("</header>"));
  for (const id of ["title", "status", "mode", "phase", "step", "expiry", "banner"]) {
    assert.match(header, new RegExp(`id="${id}"`), `${id} stays visible without a tab`);
  }
  // The bar shows the step counter a second time, so it needs a name of its own.
  assert.match(header, /<progress id="progress"[^>]*aria-label="[^"]+"[^>]*hidden/);
  assert.match(header, /aria-controls="panel-session"/, "the tabs are part of the sticky header");
  assert.doesNotMatch(header, /id="step-title"/, "the long step text is not in the header");
});

// The CSP is `style-src 'self'`: an inline style attribute or a <style> block
// would be dropped on a phone, so the page must keep its styling in style.css.
test("the page carries no inline style", () => {
  assert.doesNotMatch(page, /\sstyle="/);
  assert.doesNotMatch(page, /<style/);
});

// One selector drives the tabs: aria-selected for the button, hidden for the
// panel, so the CSS owns the look and the JS owns nothing visual.
test("one selector drives the tabs and their panels", () => {
  assert.match(app, /function selectTab\(name, focus\)/);
  assert.match(app, /setAttribute\("aria-selected"/);
  assert.match(app, /button\.tabIndex = on \? 0 : -1/);
  assert.match(app, /panel\.hidden = !on/);
  // A panel picked while the page is scrolled down would otherwise open on its
  // middle: the switch starts the new panel at the top.
  assert.match(app, /window\.scrollTo\(0, 0\)/);
  assert.match(app, /event\.key === "ArrowRight"/);
  assert.match(app, /event\.key === "ArrowLeft"/);
  assert.match(app, /event\.key === "Home"/);
  assert.match(app, /event\.key === "End"/);
  assert.match(app, /^wireTabs\(\);$/m);
  // The fragment is the pairing secret: the page never writes it.
  assert.doesNotMatch(app, /location\.hash\s*=/);
});

// The header has one line per vital, so render() splits the step and feeds the
// progress bar with properties, never with an inline style the CSP would drop.
test("render splits the step and drives the progress bar", () => {
  assert.match(app, /els\.step\.textContent = step \? `\$\{step\.index}\/\$\{step\.total}` : "—"/);
  assert.match(app, /els\.stepTitle\.textContent = step \?/);
  assert.match(app, /els\.progress\.hidden = !step/);
  assert.match(app, /els\.progress\.max = step\.total \|\| 1/);
  assert.match(app, /els\.progress\.value = step\.index \|\| 0/);
});

// The host appends oldest first; the feed reads newest first so the entry that
// just happened is at the top instead of below the fold of a stray scroll.
test("the activity feed renders newest first", () => {
  assert.match(app, /model\.activity\.slice\(\)\.reverse\(\)/);
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

// The phone can change the workflow mode from the Settings tab. One select is
// the whole control, and the page asks before a switch that would discard the
// round in progress.
test("the settings tab can switch mode", () => {
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

// The work-state button follows the host's pause state, so a phone never offers
// an action the session cannot take: running pauses, pausing cancels, paused
// resumes. It sends a bare command — the pause verbs carry no text.
test("the work-state button follows the pause state and sends bare commands", async () => {
  const { COMMAND_KINDS, COMMAND_TEXT_KINDS, buildCommand } = await import("../public/console.js");
  const { deriveKeys } = await import("../public/crypto.js");
  const { parsePairingKey } = await import("../public/crypto.js");
  const { VECTOR } = await import("./vectors.js");
  const { encKey, macKey } = await deriveKeys(parsePairingKey(VECTOR.key));
  for (const kind of ["pause", "unpause", "continue", "resume_work"]) {
    assert.ok(COMMAND_KINDS.has(kind), `${kind} must be a known command kind`);
    assert.ok(!COMMAND_TEXT_KINDS.has(kind), `${kind} must carry no text`);
  }
  assert.match(app, /running: \["pause", "Pause work"\]/);
  assert.match(app, /pausing: \["unpause", "Cancel pause"\]/);
  assert.match(app, /paused: \["continue", "Resume work"\]/);
  assert.match(app, /PAUSE_ACTIONS\[model\?\.pause\]/);
  assert.match(app, /els\.pausectl\.dataset\.kind/);
  // The keys are the values viewModel actually produces: the host's pause
  // projection is flattened to its state string before the page sees it.
  const { viewModel } = await import("../public/console.js");
  for (const state of ["running", "pausing", "paused"]) {
    assert.equal(viewModel({ pause: { state } }).pause, state, `viewModel must flatten pause state ${state}`);
  }
  assert.match(app, /command\(kind, ""\)/);
  // The command must still be signed like any other: buildCommand covers the
  // fields the relay could change, so the button goes through the same path.
  const frame = await buildCommand({
    room: VECTOR.room,
    encKey,
    macKey,
    kind: "pause",
    text: "",
    nonce: 7,
    id: "test-id",
    randomBytes: () => new Uint8Array(12).fill(3),
  });
  assert.equal(frame.t, "cmd");
  assert.equal(frame.id, "test-id");
});
