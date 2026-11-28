import assert from 'node:assert/strict';
import { createServer } from 'node:http';
import { once } from 'node:events';
import { test } from 'node:test';
import { fetchCatalog } from './portal-api.mjs';

test('catalog only treats a redirect to the portal login page as authentication failure', async (t) => {
  const server = createServer((req, res) => {
    if (req.url === '/login-redirect') {
      res.writeHead(302, { Location: '/PatientPortal/NewPortal/Page/Account/Login?return=1' }).end();
    } else if (req.url === '/maintenance-redirect') {
      res.writeHead(302, { Location: '/maintenance' }).end();
    } else if (req.url === '/unauthorized') {
      res.writeHead(401).end();
    } else if (req.url === '/throttled') {
      res.writeHead(429, { 'Retry-After': '120' }).end();
    } else if (req.url === '/catalog') {
      res.writeHead(200, { 'Content-Type': 'application/json' }).end('[]');
    } else {
      res.writeHead(200, { 'Content-Type': 'text/html' }).end('<html></html>');
    }
  }).listen(0, '127.0.0.1');
  t.after(() => server.close());
  await once(server, 'listening');
  const origin = `http://127.0.0.1:${server.address().port}`;
  globalThis.location = { origin };
  t.after(() => { delete globalThis.location; });
  const page = { evaluate: (fn, args) => fn({ ...args, target: origin + args.target }) };

  assert.deepEqual(await fetchCatalog(page, '/login-redirect'), { status: 401 });
  assert.deepEqual(await fetchCatalog(page, '/maintenance-redirect'), { status: 502 });
  assert.deepEqual(await fetchCatalog(page, '/unauthorized'), { status: 401 });
  assert.deepEqual(await fetchCatalog(page, '/throttled'), { status: 429, retryAfter: '120' });
  assert.deepEqual(await fetchCatalog(page, '/catalog'), { status: 200, body: '[]' });
});
