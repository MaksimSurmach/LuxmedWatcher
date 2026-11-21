import assert from 'node:assert/strict';
import { createServer } from 'node:http';
import { once } from 'node:events';
import { mkdtemp, rm } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import test from 'node:test';
import { createSteelProvider } from './steel-provider.mjs';

test('Steel leaves an existing session alone', async () => {
  let postCount = 0;
  const server = createServer((req, res) => {
    if (req.method === 'POST') postCount++;
    res.end(JSON.stringify({ sessions: [{ id: 'another-session', status: 'live' }] }));
  });
  const folder = await mkdtemp(join(tmpdir(), 'luxmed-steel-'));
  server.listen(0, '127.0.0.1');
  await once(server, 'listening');
  try {
    const provider = await createSteelProvider({
      baseURL: 'http://127.0.0.1:' + server.address().port, profileDir: folder,
    });
    await assert.rejects(provider.open('12'), error => error.code === 'BROWSER_BUSY');
    assert.equal(postCount, 0);
    await provider.close();
  } finally {
    server.close();
    await rm(folder, { recursive: true, force: true });
  }
});
