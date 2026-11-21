import { chromium } from 'playwright';
import { readProfile, writeProfile } from './profile-store.mjs';
import { viewport } from './screencast-server.mjs';

const freeStatuses = new Set(['idle', 'released']);
const sessionID = /^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$/;

function busy() {
  return Object.assign(new Error('Steel is busy'), { code: 'BROWSER_BUSY' });
}

function trustedURL(value, protocols) {
  try {
    const url = new URL(value);
    if (protocols.includes(url.protocol) && url.hostname && !url.username && !url.password && !url.hash) return url;
  } catch {}
  throw new Error('Invalid Steel endpoint');
}

export async function createSteelProvider({ baseURL, profileDir = '/profiles', cdpURL } = {}) {
  const api = trustedURL(baseURL, ['http:', 'https:']);
  if (api.pathname !== '/' || api.search) throw new Error('Steel base URL must be an origin');
  const override = cdpURL && trustedURL(cdpURL, ['ws:', 'wss:']);
  let held;
  let stopping = false;

  async function request(method, path, allowMissing = false) {
    const response = await fetch(new URL(path, api), {
      method, redirect: 'error', signal: AbortSignal.timeout(12_000),
      ...(method === 'POST' ? { body: '{}', headers: { 'Content-Type': 'application/json' } } : {}),
    }).catch(() => { throw new Error('Steel API unavailable'); });
    if (response.status === 409) throw busy();
    if (allowMissing && response.status === 404) return response;
    if (!response.ok) throw new Error('Steel API unavailable');
    return response;
  }

  async function listSessions() {
    const data = await (await request('GET', '/v1/sessions')).json().catch(() => null);
    const sessions = Array.isArray(data) ? data : data?.sessions;
    if (!Array.isArray(sessions) || sessions.some(s => typeof s?.status !== 'string' || !sessionID.test(s.id ?? ''))) {
      throw new Error('Cannot verify Steel session ownership');
    }
    return sessions;
  }

  function websocketEndpoint(returned) {
    if (override) return override.href;
    const url = trustedURL(returned, ['ws:', 'wss:']);
    const expected = api.protocol === 'https:' ? 'wss:' : 'ws:';
    // Self-hosted Steel can advertise its loopback address behind a private proxy.
    if (['localhost', '127.0.0.1', '[::1]'].includes(url.hostname)) {
      url.host = api.host;
      url.protocol = expected;
    }
    if (url.host !== api.host || url.protocol !== expected) throw new Error('Steel advertised an untrusted CDP endpoint');
    return url.href;
  }

  async function release(id) {
    if (!id) return;
    // shortcut: self-hosted Steel has no cross-client creation lease, use a dedicated instance for import and release.
    const sessions = await listSessions();
    if (!sessions.some(s => s.id === id && !freeStatuses.has(s.status))) return;
    await (await request('POST', '/v1/sessions/' + id + '/release', true)).body?.cancel();
  }

  async function prepare(id, slot) {
    let session;
    let browser;
    let context;
    let saving = Promise.resolve();
    async function dispose() {
      try {
        await context?.close().catch(() => {});
        await browser?.close().catch(() => {});
        await release(session);
      } finally {
        if (held === slot) held = undefined;
      }
    }

    try {
      const storageState = await readProfile(profileDir, id);
      if ((await listSessions()).some(s => !freeStatuses.has(s.status))) throw busy();
      if (stopping) throw new Error('Steel provider is closed');
      const created = await (await request('POST', '/v1/sessions')).json().catch(() => null);
      if (!created || !sessionID.test(created.id ?? '')) throw new Error('Invalid Steel session response');
      session = created.id;
      const endpoint = websocketEndpoint(created.websocketUrl);
      try {
        browser = await chromium.connectOverCDP(endpoint, { timeout: 9_000 });
      } catch (error) {
        // Steel can forward a private DNS name as Chrome's Host header, which Chrome rejects.
        if (api.protocol !== 'http:' || override ||
          !error.message.includes('Host header is specified and is not an IP address or localhost')) throw error;
        browser = await chromium.connectOverCDP(endpoint, { timeout: 9_000, headers: { Host: 'localhost' } });
      }
      context = await browser.newContext({ storageState, viewport, acceptDownloads: false });
      const page = await context.newPage();
      return {
        page,
        save() {
          if (slot.closing) throw new Error('Steel profile is closing');
          const saved = saving.then(async () => writeProfile(profileDir, id, await context.storageState({ indexedDB: true })));
          saving = saved.catch(() => {});
          return saved;
        },
        close() {
          slot.closing ??= saving.then(dispose);
          return slot.closing;
        },
      };
    } catch (error) {
      await dispose().catch(() => {});
      if (error?.code === 'BROWSER_BUSY') throw error;
      throw new Error('Cannot open the Steel browser profile');
    }
  }

  return {
    async open(id) {
      if (stopping) throw new Error('Steel provider is closed');
      if (typeof id !== 'string' || !/^[1-9][0-9]{0,15}$/.test(id) || !Number.isSafeInteger(Number(id))) {
        throw new Error('Invalid browser account ID');
      }
      if (held) {
        if (held.id !== id || held.closing) throw busy();
        const current = await held.pending;
        if (!current.page.isClosed() && current.page.context().browser()?.isConnected()) return current;
        await current.close();
      }
      const slot = { id };
      held = slot;
      slot.pending = prepare(id, slot);
      return slot.pending;
    },
    async close() {
      stopping = true;
      if (held) await (await held.pending).close();
    },
  };
}
