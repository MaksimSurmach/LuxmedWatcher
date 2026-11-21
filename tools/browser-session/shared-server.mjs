import { randomBytes, timingSafeEqual } from 'node:crypto';
import { createServer } from 'node:http';
import { WebSocket, WebSocketServer } from 'ws';
import { allowedPortalResources, fetchCatalog } from './portal-api.mjs';
import { checkPortal, observeSession, portalOrigin } from './session.mjs';
import { startScreencast } from './screencast-server.mjs';

const idleTime = { chromium: 15 * 60_000, steel: 15_000 };
const viewerOrigin = new URL(process.env.LUXMED_VIEWER_URL).origin;
const key = process.env.LUXMED_WORKER_KEY ?? '';
if (!/^[a-f0-9]{64}$/i.test(key)) throw new Error('LUXMED_WORKER_KEY must be a 32-byte lowercase hex secret');
const authorization = Buffer.from(`Bearer ${key}`);
const mode = process.env.LUXMED_BROWSER_BACKEND ?? 'chromium';
const profileDir = process.env.LUXMED_PROFILE_DIR ?? '/profiles';
let provider;
switch (mode) {
case 'chromium': {
  const { createChromiumProvider } = await import('./chromium-provider.mjs');
  provider = await createChromiumProvider({ profileDir, executablePath: process.env.LUXMED_CHROMIUM_PATH || undefined });
  break;
}
case 'steel': {
  const { createSteelProvider } = await import('./steel-provider.mjs');
  provider = await createSteelProvider({ profileDir, baseURL: process.env.STEEL_BASE_URL, cdpURL: process.env.STEEL_CDP_URL || undefined });
  break;
}
default:
  throw new Error('LUXMED_BROWSER_BACKEND must be chromium or steel');
}

const profiles = new Map();
const wss = new WebSocketServer({ noServer: true, maxPayload: 8192, handleProtocols: () => 'binary' });
let stopping = false;

function reply(res, status, body = '') {
  res.writeHead(status, { 'Content-Type': 'application/json', 'Cache-Control': 'no-store' }).end(body);
}

function authorized(req) {
  const presented = Buffer.from(req.headers.authorization ?? '');
  return authorization.byteLength === presented.byteLength && timingSafeEqual(authorization, presented);
}

function target(req) {
  if (!authorized(req) || stopping) return;
  const url = new URL(req.url, viewerOrigin);
  const match = /^\/accounts\/([1-9]\d{0,15})(\/.*)$/.exec(url.pathname);
  if (!match || !Number.isSafeInteger(Number(match[1])) || url.search.length > 4096) return;
  return { id: match[1], route: match[2], search: url.search };
}

function entryFor(id) {
  let entry = profiles.get(id);
  if (entry) return entry;
  if (profiles.size >= 12) return;
  entry = { busy: false, viewer: null, ticket: '', expires: 0, readyUntil: 0, snapshotAt: 0 };
  profiles.set(id, entry);
  return entry;
}

function deferCleanup(id, entry) {
  clearTimeout(entry.idle);
  entry.idle = setTimeout(async () => {
    if (entry.busy || entry.viewer) return;
    entry.busy = true;
    try {
      if (entry.readyUntil) await saveProfile(entry, true);
    } finally {
      await entry.handle?.close().catch(() => {});
      if (profiles.get(id) === entry) profiles.delete(id);
    }
  }, Math.max(idleTime[mode], entry.expires - Date.now()));
  entry.idle.unref();
}

async function saveProfile(entry, force = false) {
  if (!entry.handle || !force && Date.now() < entry.snapshotAt + 60_000) return;
  try {
    await entry.handle.save();
    entry.snapshotAt = Date.now();
  } catch {
    // Keep the latest valid on-disk profile if the context closes mid-snapshot.
  }
}

async function pageFor(id, entry) {
  if (!entry.handle || entry.handle.page.isClosed() || !entry.handle.page.context().browser()?.isConnected()) {
    const handle = await provider.open(id);
    entry.handle = handle;
    entry.report = observeSession(handle.page.context());
    entry.readyUntil = 0;
    entry.snapshotAt = 0;
  }
  return entry.handle.page;
}

async function portalReady(page, entry) {
  if (Date.now() < entry.readyUntil && new URL(page.url()).origin === portalOrigin) return 'ready';
  const state = await checkPortal(page);
  entry.report(state);
  entry.readyUntil = state === 'ready' ? Date.now() + 60_000 : 0;
  return state;
}

const api = createServer(async (req, res) => {
  if (req.method === 'GET' && req.url === '/healthz') return reply(res, 200);
  const address = target(req);
  if (!address) return reply(res, 403);
  const { id, route, search } = address;
  const isViewer = req.method === 'POST' && route === '/viewer' && !search;
  if (!isViewer && (req.method !== 'GET' || route !== '/session' && !allowedPortalResources.has(route))) return reply(res, 404);
  const entry = entryFor(id);
  if (!entry || entry.busy || entry.viewer || !isViewer && Date.now() < entry.expires) return reply(res, 409);
  if (isViewer) {
    entry.ticket = randomBytes(32).toString('hex');
    entry.expires = Date.now() + 45_000;
    deferCleanup(id, entry);
    return reply(res, 200, JSON.stringify({ ticket: entry.ticket }));
  }

  entry.busy = true;
  try {
    const page = await pageFor(id, entry);
    const ready = await portalReady(page, entry);
    if (ready === 'needs_login') return reply(res, 401);
    if (ready !== 'ready') return reply(res, 503);
    await saveProfile(entry);
    if (route === '/session') return reply(res, 200);
    const response = await fetchCatalog(page, route + search);
    if (response.status === 401) entry.readyUntil = 0;
    return reply(res, response.status, response.body);
  } catch (error) {
    reply(res, error?.code === 'BROWSER_BUSY' ? 409 : 503);
  } finally {
    entry.busy = false;
    if (!stopping) deferCleanup(id, entry);
  }
});

const web = createServer((_req, res) => reply(res, 404));
web.on('upgrade', (req, socket, head) => {
  const address = target(req);
  const entry = address && profiles.get(address.id);
  const protocols = (req.headers['sec-websocket-protocol'] ?? '').split(',').map(value => value.trim());
  if (!address || req.headers.origin !== viewerOrigin || address.route !== '/vnc' || address.search ||
    !entry || entry.busy || entry.viewer || Date.now() >= entry.expires ||
    !protocols.includes('binary') || !protocols.includes(`ticket-${entry.ticket}`)) {
    socket.end('HTTP/1.1 403 Forbidden\r\nConnection: close\r\n\r\n');
    return;
  }
  entry.ticket = '';
  entry.expires = 0;
  entry.readyUntil = 0;
  wss.handleUpgrade(req, socket, head, (client) => void view(address.id, entry, client));
});

async function view(id, entry, socket) {
  entry.viewer = socket;
  const timeout = setTimeout(() => socket.close(), 10 * 60_000);
  let cleanup;
  let closed = false;

  const opening = (async () => {
    const page = await pageFor(id, entry);
    if (closed) return;
    if (new URL(page.url()).origin !== portalOrigin) await portalReady(page, entry);
    if (closed || socket.readyState !== WebSocket.OPEN) return;
    cleanup = await startScreencast(page, socket);
  })();

  socket.once('error', () => socket.terminate());
  socket.once('close', async () => {
    closed = true;
    clearTimeout(timeout);
    try {
      await opening;
      await cleanup?.();
      await saveProfile(entry, true);
    } catch {
      // The page may have closed before the viewer attached.
    } finally {
      entry.viewer = null;
      if (!stopping) deferCleanup(id, entry);
    }
  });
  try {
    await opening;
  } catch (error) {
    socket.close(error?.code === 'BROWSER_BUSY' ? 1013 : 1011);
  }
}

api.listen(8091, '0.0.0.0');
web.listen(8092, '0.0.0.0');
async function stop() {
  if (stopping) return;
  stopping = true;
  for (const entry of profiles.values()) {
    clearTimeout(entry.idle);
    entry.viewer?.terminate();
  }
  api.close();
  web.close();
  await provider.close().catch(() => {
    console.warn('Could not close the browser provider cleanly');
  });
}
process.on('SIGTERM', () => void stop());
process.on('SIGINT', () => void stop());
