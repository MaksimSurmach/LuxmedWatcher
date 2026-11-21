import assert from 'node:assert/strict';
import { once } from 'node:events';
import { copyFile, mkdtemp, rm, stat, writeFile } from 'node:fs/promises';
import { createServer } from 'node:http';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import test from 'node:test';
import { createChromiumProvider } from './chromium-provider.mjs';

async function fixtureState(page, value) {
  return page.evaluate(async (value) => {
    if (value) {
      document.cookie = `fixture=${value}; Path=/`;
      localStorage.setItem('fixture', value);
    }
    const db = await new Promise((resolve, reject) => {
      const request = indexedDB.open('fixture', 1);
      request.onupgradeneeded = () => request.result.createObjectStore('fixture');
      request.onsuccess = () => resolve(request.result);
      request.onerror = () => reject(request.error);
    });
    const indexed = await new Promise((resolve, reject) => {
      const transaction = db.transaction('fixture', value ? 'readwrite' : 'readonly');
      const store = transaction.objectStore('fixture');
      if (value) store.put(value, 'state');
      const request = store.get('state');
      transaction.oncomplete = () => resolve(request.result);
      transaction.onerror = () => reject(transaction.error);
    });
    db.close();
    return [document.cookie, localStorage.getItem('fixture'), indexed];
  }, value);
}

test('Chromium accounts share a browser and restore only their own private state', { timeout: 220_000 }, async (t) => {
  const profileDir = await mkdtemp(join(tmpdir(), 'luxmed-shared-chromium-'));
  const server = createServer((_request, response) => response.writeHead(200, { 'Content-Type': 'text/html' }).end('<!doctype html>'));
  let provider = await createChromiumProvider({ profileDir });
  t.after(async () => {
    await provider.close();
    server.close();
    await rm(profileDir, { recursive: true, force: true });
  });
  server.listen(0, '127.0.0.1');
  await once(server, 'listening');
  const origin = `http://127.0.0.1:${server.address().port}`;

  for (const badId of [undefined, 1, '0', '01', '../1', '1\n']) {
    await assert.rejects(provider.open(badId), /Invalid browser account ID/);
  }
  const [first, again, second] = await Promise.all([provider.open('1'), provider.open('1'), provider.open('2')]);
  assert.equal(first, again);
  const browser = first.page.context().browser();
  assert.equal(second.page.context().browser(), browser);
  assert.notEqual(first.page.context(), second.page.context());
  await Promise.all([first.page.goto(origin), second.page.goto(origin)]);
  assert.deepEqual(await fixtureState(first.page, 'one'), ['fixture=one', 'one', 'one']);
  assert.deepEqual(await fixtureState(second.page), ['', null, undefined]);
  assert.deepEqual(await fixtureState(second.page, 'two'), ['fixture=two', 'two', 'two']);
  await Promise.all([first.save(), first.save(), second.save()]);
  assert.equal((await stat(join(profileDir, '1'))).mode & 0o777, 0o700);
  assert.equal((await stat(join(profileDir, '1', 'storage-state.json'))).mode & 0o777, 0o600);

  await browser.close();
  const [restarted, recovered] = await Promise.all([provider.open('1'), provider.open('2')]);
  assert.notEqual(restarted.page.context().browser(), browser);
  assert.equal(restarted.page.context().browser(), recovered.page.context().browser());
  await Promise.all([restarted.page.goto(origin), recovered.page.goto(origin)]);
  assert.deepEqual(await fixtureState(restarted.page), ['fixture=one', 'one', 'one']);
  assert.deepEqual(await fixtureState(recovered.page), ['fixture=two', 'two', 'two']);

  await provider.close();
  provider = await createChromiumProvider({ profileDir });
  const reopened = await provider.open('1');
  await reopened.page.goto(origin);
  assert.deepEqual(await fixtureState(reopened.page), ['fixture=one', 'one', 'one']);
  const firstStatePath = join(profileDir, '1', 'storage-state.json');
  const secondStatePath = join(profileDir, '2', 'storage-state.json');
  await copyFile(firstStatePath, secondStatePath);
  await assert.rejects(provider.open('2'), /Cannot read browser profile state/);
  await writeFile(secondStatePath, JSON.stringify({ accountId: '2', storageState: firstStatePath }));
  await assert.rejects(provider.open('2'), /Cannot read browser profile state/);
});
