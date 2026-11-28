import { randomBytes, timingSafeEqual } from 'node:crypto';
import { createServer } from 'node:http';
import { WebSocket, WebSocketServer } from 'ws';
import { allowedPortalResources } from './portal-api.mjs';
import { requestPortalResource } from './direct-session.mjs';
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
const views = new Set();
const wss = new WebSocketServer({ noServer: true, maxPayload: 8192, handleProtocols: () => 'binary' });
let stopping = false;

function reply(res, status, body = '', retryAfter) {
  res.writeHead(status, {
    'Content-Type': 'application/json',
    'Cache-Control': 'no-store',
    ...(retryAfter && { 'Retry-After': retryAfter }),
  }).end(body);
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
  entry = { busy: false, viewer: null, ticket: '', expires: 0, needsLogin: false };
  profiles.set(id, entry);
  return entry;
}

function deferCleanup(id, entry) {
  clearTimeout(entry.idle);
  entry.idle = setTimeout(() => {
    if (!entry.busy && !entry.viewer && !entry.parked) profiles.delete(id);
  }, Math.max(idleTime[mode], entry.expires - Date.now()));
  entry.idle.unref();
}

const api = createServer(async (req, res) => {
  if (req.method === 'GET' && req.url === '/healthz') return reply(res, 200);
  const address = target(req);
  if (!address) return reply(res, 403);
  const { id, route, search } = address;
  const isViewer = req.method === 'POST' && route === '/viewer' && !search;
  if (!isViewer && (req.method !== 'GET' || route !== '/session' && !allowedPortalResources.has(route))) return reply(res, 404);
  const entry = entryFor(id);
  if (!entry || entry.busy || entry.viewer || !isViewer && (entry.parked || Date.now() < entry.expires)) return reply(res, 409);
  if (isViewer) {
    entry.ticket = randomBytes(32).toString('hex');
    entry.expires = Date.now() + 45_000;
    deferCleanup(id, entry);
    return reply(res, 200, JSON.stringify({ ticket: entry.ticket }));
  }

  entry.busy = true;
  try {
    if (entry.needsLogin) return reply(res, 401);
    const { status, body, retryAfter } = await requestPortalResource(profileDir, id, route === '/session' ? route : route + search);
    entry.needsLogin = status === 401;
    return reply(res, status, body, retryAfter);
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
  wss.handleUpgrade(req, socket, head, (client) => {
    const task = view(address.id, entry, client);
    views.add(task);
    void task.finally(() => views.delete(task));
  });
});

async function view(id, entry, socket) {
  entry.viewer = socket;
  const timeout = setTimeout(() => socket.close(1012), 10 * 60_000);
  socket.on('message', () => timeout.refresh());
  // A sleeping Telegram client may leave a half-open TCP connection.
  let alive = true;
  socket.on('pong', () => { alive = true; });
  const heartbeat = setInterval(() => {
    if (!alive) return socket.terminate();
    alive = false;
    socket.ping();
  }, 12_000);
  const disconnected = new Promise(resolve => socket.once('close', (code, reason) => resolve(code === 1000 && reason.toString() === 'finished')));
  socket.once('error', () => socket.terminate());
  const resumed = entry.parked;
  clearTimeout(entry.parkTimer);
  entry.parked = null;
  let handle = resumed;
  let cleanup;
  let finished = false;

  try {
    handle ??= await provider.open(id);
    if (socket.readyState === WebSocket.OPEN) {
      if (!resumed) {
        const report = observeSession(handle.page.context());
        report(await checkPortal(handle.page));
      }
      if (socket.readyState === WebSocket.OPEN) cleanup = await startScreencast(handle.page, socket);
    }
    finished = await disconnected;
  } catch (error) {
    socket.close(error?.code === 'BROWSER_BUSY' ? 1013 : 1011);
    await disconnected;
  } finally {
    clearTimeout(timeout);
    clearInterval(heartbeat);
    await cleanup?.().catch(() => {});
    if (handle && !stopping && !finished && cleanup && mode === 'chromium') {
      entry.parked = handle;
      entry.parkTimer = setTimeout(() => {
        entry.parked = null;
        entry.busy = true;
        const task = release(entry, handle).finally(() => {
          entry.busy = false;
          deferCleanup(id, entry);
        });
        views.add(task);
        void task.finally(() => views.delete(task));
      }, 120_000);
    } else if (handle) {
      await release(entry, handle);
    }
    entry.viewer = null;
    if (!stopping) deferCleanup(id, entry);
  }
}

async function release(entry, handle) {
  try {
    await handle.save();
    entry.needsLogin = false;
  } catch {
    console.warn('Could not save the LuxMed login profile');
  } finally {
    await handle.close().catch(() => {});
  }
}

api.listen(8091, '0.0.0.0');
web.listen(8092, '0.0.0.0');
async function stop() {
  if (stopping) return;
  stopping = true;
  for (const entry of profiles.values()) {
    clearTimeout(entry.idle);
    clearTimeout(entry.parkTimer);
    if (entry.parked) views.add(release(entry, entry.parked));
    entry.viewer?.terminate();
  }
  api.close();
  web.close();
  await Promise.allSettled(views);
  await provider.close().catch(() => {
    console.warn('Could not close the browser provider cleanly');
  });
}
process.on('SIGTERM', () => void stop());
process.on('SIGINT', () => void stop());
