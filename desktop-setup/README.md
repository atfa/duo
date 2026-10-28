# Xduo desktop setup guide

Static onboarding at `https://desktop.xduo.dev/setup`. The Worker route is limited
to `/setup*`; desktop terminal traffic continues to use the named Cloudflare tunnel.
No computer registration or shared relay is provided here. Each user configures
their own hostname and local Cloudflare credentials.

```sh
node --test desktop-setup/worker.test.mjs
wrangler deploy --config desktop-setup/wrangler.jsonc
```
