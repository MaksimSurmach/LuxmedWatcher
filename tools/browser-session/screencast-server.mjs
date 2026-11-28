import { WebSocket } from 'ws';
import { setTimeout as delay } from 'node:timers/promises';

export const viewport = { width: 1180, height: 728 };
const mouseButtons = ['left', 'middle', 'right'];
const buttonBits = [1, 4, 2];
const keyCodes = {
  Backspace: 8, Tab: 9, Enter: 13, Shift: 16, Control: 17, Alt: 18, Escape: 27,
  ' ': 32, PageUp: 33, PageDown: 34, End: 35, Home: 36, ArrowLeft: 37, ArrowUp: 38,
  ArrowRight: 39, ArrowDown: 40, Delete: 46, Meta: 91,
};
const modifierBits = { Alt: 1, Control: 2, Meta: 4, Shift: 8 };

export async function startScreencast(page, socket) {
  // The portal redirect can swap Chromium's document target after DOMContentLoaded.
  // Rebind to the same page so the portal keeps its login form.
  for (let attempt = 0; ; attempt++) {
    if (socket.readyState !== WebSocket.OPEN) throw new Error('Viewer disconnected');
    try {
      await page.bringToFront();
      await page.waitForLoadState('domcontentloaded', { timeout: 5000 });
      return await attachScreencast(page, socket);
    } catch (error) {
      if (attempt >= 5 || !error.message.includes('Not attached to an active page')) throw error;
      await delay(200 * (attempt + 1));
    }
  }
}

async function attachScreencast(page, socket) {
  const cdp = await page.context().newCDPSession(page);
  let modifiers = 0;
  let buttons = 0;
  let pending = 0;
  let queue = Promise.resolve();
  let closed = false;

  async function input(data, isBinary) {
    if (isBinary || data.length > 8192) return;
    const event = JSON.parse(data.toString('utf8'));
    if (event === null || typeof event !== 'object') return;
    if (event.kind === 'text' && typeof event.text === 'string' && event.text.length <= 2048) {
      return cdp.send('Input.insertText', { text: event.text });
    }
    if (event.kind === 'key') {
      const { phase, key, code, text } = event;
      if (!['up', 'down'].includes(phase) || typeof key !== 'string' || key.length > 32 ||
        typeof code !== 'string' || code.length > 32) return;
      const bit = modifierBits[key] ?? 0;
      modifiers = phase === 'down' ? modifiers | bit : modifiers & ~bit;
      const vk = keyCodes[key] ?? (/^[a-z0-9]$/i.test(key) ? key.toUpperCase().charCodeAt(0) : 0);
      const typed = phase === 'down' && typeof text === 'string' && text.length <= 4 && !(modifiers & 7) ? text : '';
      return cdp.send('Input.dispatchKeyEvent', {
        type: phase === 'up' ? 'keyUp' : typed ? 'keyDown' : 'rawKeyDown',
        modifiers, key, code, windowsVirtualKeyCode: vk, text: typed,
      });
    }
    const { x, y } = event;
    if (!Number.isFinite(x) || x < 0 || x >= viewport.width ||
      !Number.isFinite(y) || y < 0 || y >= viewport.height) return;
    if (event.kind === 'pointer' && ['move', 'down', 'up'].includes(event.phase) && [0, 1, 2].includes(event.button)) {
      const bit = buttonBits[event.button];
      if (event.phase === 'down') buttons |= bit;
      if (event.phase === 'up') buttons &= ~bit;
      const type = { move: 'mouseMoved', down: 'mousePressed', up: 'mouseReleased' }[event.phase];
      return cdp.send('Input.dispatchMouseEvent', {
        type, x, y, button: event.phase === 'move' ? 'none' : mouseButtons[event.button],
        buttons, modifiers, clickCount: event.phase === 'move' ? 0 : 1,
      });
    }
    if (event.kind === 'wheel' && Number.isFinite(event.dx) && Number.isFinite(event.dy)) {
      const bound = n => Math.max(-4096, Math.min(4096, n));
      return cdp.send('Input.dispatchMouseEvent', {
        type: 'mouseWheel', x, y, modifiers, deltaX: bound(event.dx), deltaY: bound(event.dy),
      });
    }
  }

  function onInput(data, isBinary) {
    if (closed) return;
    if (pending >= 64) {
      if (!isBinary && data.includes('"phase":"move"')) return;
      socket.close(1008);
      return;
    }
    pending += 1;
    queue = queue.then(() => {
      if (!closed) return input(data, isBinary);
    }).catch(() => {}).finally(() => { pending -= 1; });
  }

  cdp.on('Page.screencastFrame', ({ data, sessionId }) => {
    if (socket.readyState === WebSocket.OPEN && socket.bufferedAmount < 256 * 1024) {
      socket.send(Buffer.from(data, 'base64'), { binary: true });
    }
    cdp.send('Page.screencastFrameAck', { sessionId }).catch(() => {});
  });

  try {
    await cdp.send('Page.startScreencast', { format: 'jpeg', quality: 62, maxWidth: viewport.width, maxHeight: viewport.height });
  } catch (error) {
    await cdp.detach().catch(() => {});
    throw error;
  }
  socket.on('message', onInput);
  return async () => {
    closed = true;
    socket.off('message', onInput);
    await queue;
    await cdp.detach().catch(() => {});
  };
}
