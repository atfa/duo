export default {
  async fetch(request, env) {
    const url = new URL(request.url);
    if (url.pathname !== '/setup' && url.pathname !== '/setup/') {
      return new Response('Not found', { status: 404 });
    }
    url.pathname = '/';
    const asset = await env.ASSETS.fetch(new Request(url, request));
    const headers = new Headers(asset.headers);
    headers.set('Cache-Control', 'public, max-age=300');
    headers.set('Content-Security-Policy', "default-src 'none'; style-src 'unsafe-inline'; base-uri 'none'; frame-ancestors 'none'");
    headers.set('Referrer-Policy', 'no-referrer');
    headers.set('X-Content-Type-Options', 'nosniff');
    return new Response(asset.body, { status: asset.status, headers });
  }
};
