import test from "node:test";
import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import { fileURLToPath } from "node:url";

const root = fileURLToPath(new URL("../public/", import.meta.url));
const css = await readFile(`${root}style.css`, "utf8");

// rule returns the declaration block of a selector, so an assertion can say
// which rule it means instead of matching any line in the file.
function rule(selector) {
  const start = css.indexOf(`${selector} {`);
  assert.ok(start >= 0, `the stylesheet has no rule for ${selector}`);
  const end = css.indexOf("}", start);
  return css.slice(start, end + 1);
}

// The page is served with `style-src 'self'` and the relay must not make a
// request on the phone's behalf, so the stylesheet is the only styling source
// and it may not reach out for a font, an image or a second sheet.
test("the stylesheet loads nothing from outside itself", () => {
  assert.doesNotMatch(css, /@import/);
  assert.doesNotMatch(css, /url\(\s*['"]?https?:/i);
  assert.doesNotMatch(css, /https?:\/\//i);
});

// The script toggles the hidden attribute on the tab panels and the progress
// bar, and both of those selectors also carry a display of their own. Without a
// rule that outranks them a hidden panel would stay on screen.
test("hidden wins over the display a panel sets for itself", () => {
  assert.match(rule("[hidden]"), /display:\s*none\s*!important/);
  assert.match(rule(".panel"), /display:\s*grid/);
  assert.match(rule(".progress"), /display:\s*block/);
  // One exception, and it is the rule above: nothing else may shout, because a
  // stylesheet full of !important cannot be overridden by a later fix.
  const shouts = css.split("\n").filter((line) => line.includes("!important"));
  assert.equal(shouts.length, 1, `only [hidden] may use !important, found ${shouts.length}`);
  assert.match(shouts[0], /\[hidden\]/);
});

// The composer is the reason the page was redrawn: it has to be reachable
// without scrolling, above the home indicator, and never on top of the last card.
test("the composer is pinned to the bottom edge above the safe area", () => {
  const dock = rule(".dock");
  assert.match(dock, /position:\s*fixed/);
  assert.match(dock, /bottom:\s*0/);
  assert.match(dock, /env\(safe-area-inset-bottom\)/);
});

// The dock's ceiling and the space the cards reserve below themselves are one
// number. Two numbers drift apart the first time the status line wraps, and the
// drift is invisible until the last card is covered on a real phone.
test("the dock and the card reserve are the same number", () => {
  assert.match(rule(":root"), /--dock-space:\s*[\d.]+rem/);
  const space = "calc(var(--dock-space) + env(safe-area-inset-bottom))";
  assert.match(rule(".shell"), new RegExp(`padding-bottom:\\s*${escapeRe(space)}`));
  assert.match(rule(".dock"), new RegExp(`max-height:\\s*${escapeRe(space)}`));
});

// A status line that wraps is a status line that can push the textarea off the
// dock, so it is clamped, and an empty one costs no height at all.
test("the status line cannot grow the dock without bound", () => {
  const sent = rule(".sent");
  assert.match(sent, /-webkit-line-clamp:\s*2/);
  assert.match(sent, /overflow:\s*hidden/);
  // Empty it takes no space, but it may not leave the accessibility tree: this is
  // the only place the page reports that a command was accepted or refused, and a
  // live region that is display:none is not reliably announced.
  const empty = rule(".sent:empty");
  assert.doesNotMatch(empty, /display:\s*none/);
  assert.match(empty, /position:\s*absolute/);
  assert.match(empty, /clip-path:\s*inset\(50%\)/);
});

test("the header stays on screen while the cards scroll", () => {
  const topbar = rule(".topbar");
  assert.match(topbar, /position:\s*sticky/);
  assert.match(topbar, /top:\s*0/);
});

// The selected tab is the markup's aria-selected attribute: the script adds no
// class for it, so the stylesheet has to key on the attribute itself.
test("the tabs are a three-up row and the selected one is visibly different", () => {
  assert.match(rule(".tabs"), /display:\s*flex/);
  assert.match(rule(".tabs"), /flex-wrap:\s*nowrap/);
  assert.match(rule(".tab"), /flex:\s*1 1 0/);
  const selected = rule('.tab[aria-selected="true"]');
  assert.match(selected, /background:/);
  assert.match(selected, /color:/);
});

// A phone zooms the whole page when a field smaller than 16px takes focus, and
// the composer is the field that takes focus most often.
test("fields are 16px so a phone does not zoom on focus", () => {
  const fields = css.slice(css.indexOf("select,\ninput[type=\"search\"],\ntextarea {"));
  assert.match(fields.slice(0, fields.indexOf("}")), /font-size:\s*1rem/);
  assert.match(rule("body"), /font:\s*16px/);
});

// 2.75rem is 44px: the smallest target that stays comfortable under a thumb.
test("every control is at least 44px tall", () => {
  assert.match(rule(":root"), /--tap:\s*2\.75rem/);
  for (const selector of [".tab", "button", 'select,\ninput[type="search"],\ntextarea']) {
    assert.match(rule(selector), /min-height:\s*var\(--tap\)/, `${selector} must use the tap height`);
  }
  assert.match(rule(".composer textarea"), /field-sizing:\s*content/);
});

// A phone is the target: iOS before 18 only knows the prefixed blur, and there
// the sticky header would simply be flat, which is a silent difference nobody
// sees on a desktop. The same for the double-tap zoom delay on the fields that
// are actually touched.
test("the sticky surfaces carry the prefixed blur and the touch action", () => {
  assert.match(css, /-webkit-backdrop-filter:\s*blur\(/);
  assert.match(css, /[^-]backdrop-filter:\s*blur\(/);
  assert.match(rule("button"), /touch-action:\s*manipulation/);
  assert.match(rule(".fold summary"), /touch-action:\s*manipulation/);
  const fields = css.slice(css.indexOf('select,\ninput[type="search"],\ntextarea {'));
  assert.match(fields.slice(0, fields.indexOf("}")), /touch-action:\s*manipulation/);
});

test("focus is visible and motion is optional", () => {
  assert.match(css, /:focus-visible\s*{[^}]*outline:\s*2px solid/);
  const reduce = css.slice(css.indexOf("@media (prefers-reduced-motion: reduce)"));
  assert.ok(reduce.length > 0, "the reduced-motion block is missing");
  assert.match(reduce.slice(0, reduce.indexOf("}")), /\.dot-run\s*{\s*animation:\s*none/);
  assert.match(css, /@media \(prefers-color-scheme: light\)/);
});

// Every class the page's structure depends on has to exist here, or the page
// renders as an unstyled list and nobody notices until it is on a phone.
test("the stylesheet covers the page's structure", () => {
  for (const name of [".topbar", ".brandline", ".vitals", ".vital", ".tabs", ".tab", ".banner", ".shell", ".panel", ".card", ".step-title", ".fold", ".progress", ".facts", ".control", ".models", ".model", ".agents", ".dot", ".activity", ".observer", ".plan", ".dock", ".composer", ".sent", ".ghost", ".sr-only"]) {
    assert.ok(css.includes(`${name} {`) || css.includes(`${name},`) || css.includes(`${name}:`), `no styling for ${name}`);
  }
});

function escapeRe(value) {
  return value.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
}
