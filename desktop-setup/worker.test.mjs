import { test } from 'node:test';
import assert from 'node:assert/strict';
import worker from './worker.mjs';

test('setup guide serves its asset and does not proxy terminals', async () => {
  const env={ASSETS:{fetch:async request=>{
    assert.equal(new URL(request.url).pathname,'/');
    return new Response('setup guide', {headers:{'Content-Type':'text/html'}});
  }}};
  for(const path of ['/setup','/setup/']){
    const response=await worker.fetch(new Request('https://desktop.xduo.dev'+path),env);
    assert.equal(response.status,200);
    assert.equal(await response.text(),'setup guide');
    assert.match(response.headers.get('Content-Security-Policy'),/frame-ancestors 'none'/);
  }
  const response=await worker.fetch(new Request('https://desktop.xduo.dev/ws'),env);
  assert.equal(response.status,404);
});
