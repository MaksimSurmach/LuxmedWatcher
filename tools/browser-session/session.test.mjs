import assert from 'node:assert/strict';
import { EventEmitter, once } from 'node:events';
import { mkdtemp, rm } from 'node:fs/promises';
import { createServer } from 'node:http';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import test from 'node:test';
import { checkPortal, observeSession, openProfile, waitForLogin } from './session.mjs';

test('only the current navigation can confirm an expired session', async () => {
  const origin = 'https://portal.example.test';
  const login = `${origin}/PatientPortal/NewPortal/Page/Account/Login`;
  const page = {
    url: () => login,
    goto: async target => ({ url: () => target, status: () => 401, ok: () => false }),
  };
  assert.equal(await checkPortal(page, origin), 'needs_login');
  page.goto = async () => { throw new Error('navigation timed out before receiving a response'); };
  assert.equal(await checkPortal(page, origin), 'unavailable');
  page.goto = async target => ({ url: () => target, status: () => 503, ok: () => false });
  assert.equal(await checkPortal(page, origin), 'unavailable');
  page.goto = async () => ({ url: () => login, status: () => 200, ok: () => true });
  assert.equal(await checkPortal(page, origin), 'needs_login');
});

test('the same Chrome profile can retain a portal session and report expiration', { timeout: 40_000 }, async (t) => {
  const directory = await mkdtemp(join(tmpdir(), 'luxmed-profile-test-'));
  t.after(() => rm(directory, { recursive: true, force: true }));

  let acceptSession = true;
  const base = '/PatientPortal/NewPortal/Page';
  const refresh = '/PatientPortal/NewPortal/Token/Refresh';
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
        response.end(`<input placeholder="Wpisz lub wybierz z listy"><script>fetch('${refresh}?opaque=fixture-private-query')</script>`);
      } else {
        response.writeHead(302, { Location: `${base}/Account/Login` }).end();
      }
    } else if (request.url.startsWith(`${refresh}?`)) {
      response.writeHead(200, {
        'Content-Type': 'application/json',
        'Set-Cookie': 'fixture_renewal=fixture-private-cookie; HttpOnly; Path=/',
        'Authorization-Token': 'fixture-private-token',
      }).end('{"marker":"fixture-private-body"}');
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
    const events = new EventEmitter();
    const observed = [];
    const report = observeSession(browser, {
      origin,
      write(entry) {
        observed.push(entry);
        events.emit(entry.event, entry);
      },
    });
    const beforeLogin = await checkPortal(page, origin);
    report(beforeLogin);
    assert.equal(beforeLogin, 'needs_login');

    const refreshResponse = once(events, 'luxmed_session_refresh_response', {
      signal: AbortSignal.timeout(15_000),
    });

    const ready = waitForLogin(page, origin);
    await page.getByRole('button', { name: 'Sign in' }).click();
    const afterLogin = await ready;
    report(afterLogin);
    assert.equal(afterLogin, 'ready');
    const [evidence] = await refreshResponse;
    assert.equal(evidence.status, 200);
    assert.equal(evidence.set_cookie_present, true);
    assert.equal(evidence.authorization_token_present, true);
    assert.deepEqual(observed.filter(entry => entry.event === 'luxmed_session_check').map(entry => entry.state), ['needs_login', 'ready']);
    assert.doesNotMatch(JSON.stringify(observed), /fixture-private|Token\/Refresh|127\.0\.0\.1/);
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

test('passive refresh telemetry ignores look-alike routes and cross-origin requests', () => {
  const context = new EventEmitter();
  const records = [];
  let readHeaders = 0;
  const origin = 'https://portal.example.test';
  observeSession(context, { origin, write: record => records.push(record) });

  const refresh = '/PatientPortal/NewPortal/Token/Refresh';
  for (const [url, frameUrl] of [
    [`https://other.example.test${refresh}`, origin],
    [`${origin}${refresh}/extra`, origin],
    [`${origin}${refresh}`, 'https://other.example.test'],
  ]) {
    context.emit('response', {
      url: () => url,
      request: () => ({ method: () => 'GET', frame: () => ({ url: () => frameUrl }) }),
      allHeaders: async () => { readHeaders++; return {}; },
    });
  }
  assert.equal(readHeaders, 0);
  assert.deepEqual(records, []);
});
