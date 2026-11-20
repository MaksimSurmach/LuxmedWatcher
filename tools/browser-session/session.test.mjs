import assert from 'node:assert/strict';
import { once } from 'node:events';
import { mkdtemp, rm } from 'node:fs/promises';
import { createServer } from 'node:http';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import test from 'node:test';
import { checkPortal, openProfile, waitForLogin } from './session.mjs';

test('the same Chrome profile can retain a portal session and report expiration', { timeout: 40_000 }, async (t) => {
  const directory = await mkdtemp(join(tmpdir(), 'luxmed-profile-test-'));
  t.after(() => rm(directory, { recursive: true, force: true }));

  let acceptSession = true;
  const base = '/PatientPortal/NewPortal/Page';
  const server = createServer((request, response) => {
    if (request.url === `${base}/Account/Login`) {
      response.setHeader('Content-Type', 'text/html');
      response.end('<form method="post" action="/fixture/sign-in"><button>Sign in</button></form>');
    } else if (request.url === '/fixture/sign-in' && request.method === 'POST') {
      response.writeHead(303, {
        Location: `${base}/Dashboard`,
        'Set-Cookie': 'fixture_session=ok; HttpOnly; Path=/; SameSite=Lax; Max-Age=600',
      }).end();
    } else if (request.url === `${base}/Dashboard`) {
      response.end('Dashboard');
    } else if (request.url === `${base}/Reservation`) {
      if (acceptSession && request.headers.cookie?.includes('fixture_session=ok')) {
        response.setHeader('Content-Type', 'text/html');
        response.end('<input placeholder="Wpisz lub wybierz z listy">');
      } else {
        response.writeHead(302, { Location: `${base}/Account/Login` }).end();
      }
    } else {
      response.writeHead(404).end();
    }
  });
  server.listen(0, '127.0.0.1');
  await once(server, 'listening');
  t.after(() => server.close());
  const origin = `http://127.0.0.1:${server.address().port}`;

  let browser = await openProfile(directory, { headless: true });
  try {
    const page = browser.pages()[0];
    assert.equal(await checkPortal(page, origin), 'needs_login');
    const ready = waitForLogin(page, origin);
    await page.getByRole('button', { name: 'Sign in' }).click();
    assert.equal(await ready, 'ready');
  } finally {
    await browser.close();
  }

  browser = await openProfile(directory, { headless: true });
  try {
    const page = browser.pages()[0];
    assert.equal(await checkPortal(page, origin), 'ready');
    acceptSession = false;
    assert.equal(await checkPortal(page, origin), 'needs_login');
  } finally {
    await browser.close();
  }
});
