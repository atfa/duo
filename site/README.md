# xduo.dev

The Duo product site: two static pages (English at `/`, Simplified Chinese at
`/zh/`), shared CSS, and a small progressive-enhancement script. No build step,
no framework, no external requests.

## Layout

```text
site/
  wrangler.jsonc        Cloudflare Workers static assets config
  public/               served root
    index.html          English page
    zh/index.html       Chinese page
    404.html            not-found page
    robots.txt
    sitemap.xml
    _headers            security + cache headers
    assets/             css, js, brand images
      shots/            real TUI screenshots, exported by the capture test
```

## Preview locally

```bash
# works from the repository root and from site/
python3 -m http.server -d "$(git rev-parse --show-toplevel)/site/public" 8080
# http://localhost:8080/      (English)
# http://localhost:8080/zh/   (Chinese)
```

`-d` is resolved against the current directory, so a bare `-d site/public`
only works from the repository root; run inside `site/` it points at the
missing `site/site/public` and every URL answers 404.

`wrangler dev` from `site/` also works and applies `_headers`.

## Deploy

The site uses Cloudflare Workers static assets, so one deploy also attaches the
custom domain when the `xduo.dev` zone is active in the logged-in account.

```bash
cd site
wrangler login          # once
wrangler deploy --dry-run
wrangler deploy
```

First deploy creates the `xduo-dev` Worker and, with the `routes` entry in
`wrangler.jsonc`, attaches `xduo.dev`. The zone must be active in the logged-in
account: if it is not, Wrangler reports an error while attaching the custom
domain. Activate the zone, then redeploy. `www` is not configured.

A deploy publishes `public/` as it stands, so a committed change reaches nobody
until someone runs `wrangler deploy`. After deploying, check that the live page
carries the current release and section list:

```bash
curl -fsS https://xduo.dev/ | grep -o 'v0\.[0-9]\+\.[0-9]\+' | sort -u
curl -fsS https://xduo.dev/ | grep -c 'id="remote"'
```

Local Wrangler state lives in `site/.wrangler/` and is git-ignored.

## Conventions

- Palette comes from `internal/gotui/theme.go`; keep the site in sync with it.
- Both pages share section order and IDs: `#why #workflows #how #screens #features #remote #start #faq`.
- `assets/shots/*.svg` are not drawn by hand. `internal/gotui/capture_test.go`
  renders the real TUI and exports them with `DUO_GOTUI_SHOT_DIR` (see
  [docs/gotui.md](../docs/gotui.md#screenshots-for-the-site)); the same test
  produces the text frames in `docs/gotui/screens/`.
- Keep copy factual: v0.14.3, MIT, drivers pi/agy/opencode/codex, eight Duo tools,
  MCP subset, gotui, remote control (`/qrcode`, pause/continue, mode/driver/model,
  ciphertext-only relay). Do not claim the removed desktop GUI.
- No inline scripts or styles: `_headers` sets a strict CSP (`script-src 'self'`,
  `style-src 'self'`). Changing that requires updating the policy too.
- `site.js` is loaded in `<head>` without `defer`: it swaps `html.no-js` for
  `html.js` before first paint. Without JavaScript all content remains visible.
