import assert from 'node:assert/strict';
import { once } from 'node:events';
import { mkdtemp, rm } from 'node:fs/promises';
import { createServer } from 'node:http';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import test from 'node:test';
import { requestPortalResource } from './direct-session.mjs';
import { readProfile, writeProfile } from './profile-store.mjs';

const refreshPath = '/PatientPortal/NewPortal/Token/Refresh';
const termsPath = '/PatientPortal/NewPortal/terms/index';
const jwt = exp => '.' + Buffer.from(JSON.stringify({ exp })).toString('base64url') + '.';
const cookie = (name, value) => ({
  name, value, domain: '127.0.0.1', path: '/', expires: -1,
  httpOnly: true, secure: false, sameSite: 'Lax',
});

async function fixture(t, handler) {
  const dir = await mkdtemp(join(tmpdir(), 'luxmed-direct-'));
  const server = createServer(handler);
  t.after(async () => {
    server.close();
    await rm(dir, { recursive: true, force: true });
  });
  server.listen(0, '127.0.0.1');
  await once(server, 'listening');
  return { dir, origin: 'http://127.0.0.1:' + server.address().port };
}

test('two direct renewals persist the rotated cookies before fetching slots', async t => {
  const start = 1_800_000_000;
  t.mock.timers.enable({ apis: ['Date'], now: start * 1000 });
  const calls = [];
  let rotation = 0;
  const { dir, origin } = await fixture(t, (req, res) => {
    calls.push(req.url);
    if (req.url === refreshPath) {
      assert.ok(req.headers.cookie.includes('RefreshToken=fixture-' + rotation));
      rotation++;
      res.writeHead(200, { 'Set-Cookie': [
        'Authorization-Token=' + jwt(start + rotation * 600) + '; Path=/; HttpOnly',
        'RefreshToken=fixture-' + rotation + '; Path=/; HttpOnly',
      ] }).end();
    } else {
      assert.ok(req.headers.cookie.includes('Authorization-Token=' + jwt(start + rotation * 600)));
      res.writeHead(200, { 'Content-Type': 'application/json' }).end('[]');
    }
  });
  const origins = [{ origin, localStorage: [{ name: 'fixture', value: 'preserved' }] }];
  await writeProfile(dir, '1', {
    cookies: [cookie('Authorization-Token', jwt(start)), cookie('RefreshToken', 'fixture-0')], origins,
  });
  const events = [];
  const options = { origin, report: record => events.push(record) };

  for (const round of [1, 2]) {
    const result = await requestPortalResource(dir, '1', termsPath, options);
    assert.equal(result.status, 200);
    assert.equal(result.body.toString(), '[]');
    const state = await readProfile(dir, '1');
    assert.equal(state.cookies.find(c => c.name === 'RefreshToken').value, 'fixture-' + round);
    assert.deepEqual(state.origins, origins);
    t.mock.timers.tick(600_000);
  }
  assert.deepEqual(calls, [refreshPath, termsPath, refreshPath, termsPath]);
  assert.deepEqual(events.map(e => e.event), [
    'luxmed_http_refresh', 'luxmed_http_search', 'luxmed_http_refresh', 'luxmed_http_search',
  ]);
  assert.equal(events[2].expires_at, new Date((start + 1200) * 1000).toISOString());
  assert.doesNotMatch(JSON.stringify(events), /fixture-|Authorization-Token/);
});

test('a rejected refresh does not write cookies or attempt the slot request', async t => {
  const calls = [];
  const { dir, origin } = await fixture(t, (req, res) => {
    calls.push(req.url);
    if (req.url === termsPath) {
      res.writeHead(401).end();
    } else {
      res.writeHead(429, { 'Retry-After': '60', 'Set-Cookie': 'RefreshToken=; Max-Age=0; Path=/' }).end();
    }
  });
  const initial = { cookies: [
    cookie('Authorization-Token', jwt(Math.floor(Date.now() / 1000) + 600)), cookie('RefreshToken', 'fixture'),
  ], origins: [] };
  await writeProfile(dir, '1', initial);
  const result = await requestPortalResource(dir, '1', termsPath, { origin, report() {} });
  assert.deepEqual(result, { status: 429, retryAfter: '60' });
  assert.deepEqual(await readProfile(dir, '1'), initial);
  assert.deepEqual(calls, [termsPath, refreshPath]);
  assert.deepEqual(await requestPortalResource(dir, '2', termsPath, { origin }), { status: 401 });
  assert.deepEqual(calls, [termsPath, refreshPath]);
});

test('account checks use the authenticated cities endpoint and expose only the status', async t => {
  const { dir, origin } = await fixture(t, (req,res) => {
    assert.equal(req.url, '/PatientPortal/NewPortal/Dictionary/cities');
    assert.ok(req.headers.cookie.includes('Authorization-Token='));
    res.writeHead(200, { 'Content-Type': 'application/json' }).end('[{"fixture":"city"}]');
  });
  await writeProfile(dir, '1', {
    cookies: [cookie('Authorization-Token', jwt(Math.floor(Date.now() / 1000) + 600))],
    origins: [],
  });
  assert.deepEqual(await requestPortalResource(dir, '1', '/session', { origin }), { status: 200 });
});
