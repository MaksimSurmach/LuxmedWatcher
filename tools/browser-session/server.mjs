import { chromium } from 'playwright';
import { WebSocket, WebSocketServer } from 'ws';
import { randomBytes } from 'node:crypto';
import { readFile } from 'node:fs/promises';
import { createServer } from 'node:http';
import { connect } from 'node:net';
import { allowedPortalResources, fetchCatalog, isBookingRequest, readBookingBody } from './portal-api.mjs';
import { observeSession, portalOrigin, portalSessionState, probePortal } from './session.mjs';
import { renderViewerPage } from './viewer-copy.mjs';

const viewerOrigin = new URL(process.env.LUXMED_VIEWER_URL).origin;
const context = await chromium.launchPersistentContext('/profile', {
  chromiumSandbox: true,
  ignoreDefaultArgs: ['--password-store=basic', '--use-mock-keychain'],
  headless: false,
  acceptDownloads: false,
  viewport: { width: 1180, height: 728 },
});
const reportSession = observeSession(context);
const page = context.pages()[0] ?? await context.newPage();
const wss = new WebSocketServer({ noServer: true, maxPayload: 1 << 20, handleProtocols: () => 'binary' });
let viewer;
let ticket;
let deadline = 0;
let readyUntil = 0;
let busy = false;

function reply(res, status, body = '', retryAfter) {
  res.writeHead(status, {
    'Content-Type': 'application/json',
    'Cache-Control': 'no-store',
    ...(retryAfter && { 'Retry-After': retryAfter }),
  }).end(body);
}

async function portalReady(force = false) {
  if (!force && Date.now() < readyUntil && new URL(page.url()).origin === portalOrigin) {
    return { status: 200 };
  }
  const result = await probePortal(page);
  reportSession(portalSessionState(result.status));
  readyUntil = result.status === 200 ? Date.now() + 60_000 : 0;
  return result;
}

const api = createServer(async (req, res) => {
  try {
    const url = new URL(req.url, 'http://worker');
    if (req.method === 'GET' && url.pathname === '/healthz') return reply(res, 200);
    if (busy || viewer) return reply(res, 409);
    if (req.method === 'POST' && url.pathname === '/viewer') {
      busy = true;
      try {
        await page.bringToFront();
        if (new URL(page.url()).origin !== portalOrigin) await portalReady(true);
        ticket = randomBytes(32).toString('hex');
        deadline = Date.now() + 5 * 60_000;
        return reply(res, 200, JSON.stringify({ ticket }));
      } finally {
        busy = false;
      }
    }
    const booking = isBookingRequest(req.method, url.pathname, url.search);
    if ((!booking && (req.method !== 'GET' || url.pathname !== '/session' && !allowedPortalResources.has(url.pathname))) || url.search.length > 4096) {
      return reply(res, 404);
    }
    busy = true;
    try {
      const { status, retryAfter } = await portalReady();
      if (status !== 200) return reply(res, status, '', retryAfter);
      if (url.pathname === '/session') return reply(res, 200);
      const payload = booking ? await readBookingBody(req) : undefined;
      if (booking && !payload) return reply(res, 400);
      const result = await fetchCatalog(page, url.pathname + url.search, payload);
      if (result.status === 401) readyUntil = 0;
      return reply(res, result.status, result.body, result.retryAfter);
    } finally {
      busy = false;
    }
  } catch {
    reply(res, 503);
  }
});
const web = createServer(async (req, res) => {
  try {
    if (req.method !== 'GET') return reply(res, 405);
    const { pathname: route, searchParams } = new URL(req.url, viewerOrigin);
    const paths = {
      '/connect': ['viewer.html', 'text/html'],
      '/viewer.mjs': ['viewer.mjs', 'text/javascript'],
      '/viewer-copy.mjs': ['viewer-copy.mjs', 'text/javascript'],
    };
    let file = paths[route];
    if (!file && /^\/vendor\/(core|vendor)\/[\w/.-]+\.js$/.test(route) && !route.includes('..')) {
      file = [`node_modules/@novnc/novnc/${route.slice(8)}`, 'text/javascript'];
    }
    if (!file) return reply(res, 404);
    const source = await readFile(new URL(file[0], import.meta.url));
    const body = route === '/connect' ? renderViewerPage(source.toString('utf8'), searchParams.get('lang')) : source;
    const socketOrigin = viewerOrigin.replace(/^http/, 'ws');
    res.writeHead(200, {
      'Content-Type': file[1], 'Cache-Control': 'no-store', 'Referrer-Policy': 'no-referrer',
      'X-Content-Type-Options': 'nosniff', 'X-Frame-Options': 'DENY',
      'Content-Security-Policy': `default-src 'none'; script-src 'self'; style-src 'unsafe-inline'; connect-src ${socketOrigin}; img-src 'self' data:`,
    }).end(body);
  } catch {
    reply(res, 404);
  }
});
web.on('upgrade', (req, socket, head) => {
  const protocols = (req.headers['sec-websocket-protocol'] ?? '').split(',').map(value => value.trim());
  if (req.url !== '/vnc' || req.headers.origin !== viewerOrigin || !ticket ||
      Date.now() > deadline || busy || viewer || !protocols.includes('binary') || !protocols.includes(`ticket-${ticket}`)) {
    socket.end('HTTP/1.1 403 Forbidden\r\nConnection: close\r\n\r\n');
    return;
  }
  ticket = undefined;
  readyUntil = 0;
  wss.handleUpgrade(req, socket, head, (client) => {
    viewer = client;
    const vnc = connect(5900, '127.0.0.1');
    const timeout = setTimeout(() => client.close(), 10 * 60_000);
    const release = () => {
      clearTimeout(timeout);
      vnc.destroy();
      if (viewer === client) viewer = undefined;
    };
    client.once('close', release);
    client.once('error', () => client.terminate());
    vnc.once('error', () => client.terminate());
    vnc.once('close', () => client.terminate());
    client.on('message', (bytes, binary) => {
      if (!binary || vnc.writableLength > 1 << 20) return client.terminate();
      vnc.write(bytes);
    });
    vnc.on('data', bytes => {
      if (client.readyState !== WebSocket.OPEN || client.bufferedAmount > 8 << 20) return client.terminate();
      client.send(bytes, { binary: true });
    });
  });
});

api.listen(8091, '0.0.0.0');
web.listen(8092, '0.0.0.0');
async function stop() {
  viewer?.terminate();
  web.close();
  api.close();
  await context.close();
}
process.on('SIGTERM', stop);
process.on('SIGINT', stop);
