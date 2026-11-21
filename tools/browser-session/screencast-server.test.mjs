import assert from 'node:assert/strict';
import { EventEmitter, once } from 'node:events';
import { mkdtemp, rm } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import test from 'node:test';
import { WebSocket } from 'ws';
import { createChromiumProvider } from './chromium-provider.mjs';
import { startScreencast } from './screencast-server.mjs';

test('CDP relays a JPEG and only the target page receives input', { timeout: 180_000 }, async () => {
  const profileDir = await mkdtemp(join(tmpdir(), 'luxmed-cdp-'));
  const provider = await createChromiumProvider({ profileDir });
  try {
    const [first, second] = await Promise.all([provider.open('12'), provider.open('34')]);
    for (const { page } of [first, second]) {
      await page.setContent('<input autofocus aria-label="fixture">');
    }
    const socket = new EventEmitter();
    socket.readyState = WebSocket.OPEN;
    socket.bufferedAmount = 0;
    socket.send = data => socket.emit('frame', data);
    const frame = once(socket, 'frame', { signal: AbortSignal.timeout(8000) });
    const stop = await startScreencast(first.page, socket);
    try {
      const [jpeg] = await frame;
      assert.deepEqual(jpeg.subarray(0, 2), Buffer.from([0xff, 0xd8]));
      socket.emit('message', Buffer.from(JSON.stringify({ kind: 'text', text: 'fixture α' })), false);
      await first.page.waitForFunction(() => document.querySelector('input').value === 'fixture α');
      assert.equal(await second.page.getByRole('textbox').inputValue(), '');
    } finally {
      await stop();
    }
  } finally {
    await provider.close();
    await rm(profileDir, { recursive: true, force: true });
  }
});
