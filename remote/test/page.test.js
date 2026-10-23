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
