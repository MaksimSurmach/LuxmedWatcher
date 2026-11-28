import RFB from '/vendor/core/rfb.js';
import keysyms from '/vendor/core/input/keysymdef.js';
import { initLogging } from '/vendor/core/util/logging.js';
import { initialLanguage, language, setLanguage, t } from '/language.js';
import { Screencast } from '/screencast.js';

initLogging('none');

const telegram = window.Telegram?.WebApp;
const bridge = window.frameElement?.loginSession;
const initData = bridge?.initData || telegram?.initData || '';
const locationURL = new URL(location.href);
let ticket = locationURL.searchParams.get('ticket') || new URLSearchParams(initData).get('start_param');
const returnToApp = locationURL.searchParams.get('return') === 'app';
if (locationURL.searchParams.has('ticket')) {
  locationURL.searchParams.delete('ticket');
  history.replaceState(history.state, '', locationURL);
}

const element = (id) => document.getElementById(id);
const status = element('status');
const hint = element('hint');
const screen = element('screen');
const controls = element('controls');
const keyboard = element('keyboard');
const paste = element('paste');
const typing = element('typing');
const keys = element('keys');
const zoom = element('zoom');
const label = (name) => t(`viewer.${name}`);
const phase = (name) => { document.querySelector('main').dataset.phase = name; };

function renderLanguage() {
  const labels = {
    title: 'title', close: 'close', keyboard: 'keyboard', paste: 'paste', keyboardLabel: 'keyboard_hint',
    backspace: 'backspace', 'tab-key': 'tab_key', 'enter-key': 'enter_key',
    retry: 'retry',
  };
  for (const [id, key] of Object.entries(labels)) element(id).textContent = key === 'retry' ? t('common.retry') : label(key);
  if (bridge) element('close').textContent = label('finish');
  document.title = label('title');
  screen.setAttribute('aria-label', label('screen'));
  element('scroll-up').setAttribute('aria-label', label('scroll_up'));
  element('scroll-down').setAttribute('aria-label', label('scroll_down'));
  zoom.textContent = label('zoom_in');
  status.textContent = '';
}

telegram?.expand();
window.visualViewport?.addEventListener('resize', () => {
  document.documentElement.style.setProperty('--visual-viewport-height', `${window.visualViewport.height}px`);
});

const keyCodes = { Tab: 0xff09, Backspace: 0xff08, Enter: 0xff0d };
let opening;
let retryTimer;
let retries = 0;
let standaloneToken;

let viewer;
let startupTimer;
let connected = false;
let closed = false;
let zoomed = false;
let scrollDrag;

function stopTransport(finished = false) {
  clearTimeout(startupTimer);
  clearTimeout(retryTimer);
  opening?.abort();
  opening = null;
  const previous = viewer;
  viewer = null;
  previous?.disconnect(finished);
  connected = false;
  keys.value = '';
  screen.hidden = typing.hidden = controls.hidden = true;
  zoomed = false;
  zoom.textContent = label('zoom_in');
  zoom.setAttribute('aria-pressed', 'false');
  screen.classList.remove('screencast-zoomed');
}

function problem(message, retry = false) {
  stopTransport();
  phase('closed');
  document.querySelector('main').dataset.outcome = 'trouble';
  hint.textContent = '';
  status.textContent = label(message);
  element('retry').hidden = !retry;
  if (retry && retries < 8 && !document.hidden) {
    retryTimer = setTimeout(() => { retries++; void start(); }, 1200 * (retries + 1));
  }
}

async function standaloneAPI(action, data = {}) {
  const response = await fetch('/api/dashboard/' + action, {
    method: 'POST', headers: { 'Content-Type': 'application/json', 'X-Telegram-Init-Data': initData,
      ...(standaloneToken && {Authorization: 'Bearer ' + standaloneToken}) },
    body: JSON.stringify(data), redirect: 'error', cache: 'no-store', signal: opening.signal,
  });
  if (!response.ok) throw new Error(String(response.status));
  return response.json();
}

async function launchTicket() {
  if (bridge) return bridge.issueTicket();
  if (!standaloneToken) ({ token: standaloneToken } = await standaloneAPI('auth'));
  if (ticket) {
    const first = ticket;
    ticket = null;
    return first;
  }
  const { url } = await standaloneAPI('resume-viewer');
  return new URL(url).searchParams.get('ticket');
}

function init() {
  setLanguage(initialLanguage(telegram, locationURL.searchParams.get('lang')));
  renderLanguage();
  element('close').hidden = !bridge && !returnToApp && !(initData && typeof telegram?.close === 'function');
  if (!initData || (!bridge && !ticket)) return problem('missing');
  if (!window.isSecureContext) return problem('failed');
  void start();
}

async function start() {
  if (closed) return;
  stopTransport();
  const attempt = new AbortController();
  opening = attempt;
  element('retry').hidden = true;
  phase('connecting');
  status.textContent = label(retries ? 'reconnecting' : 'starting');
  const fail = () => { if (opening === attempt) problem('interrupted', true); };
  startupTimer = setTimeout(fail, 90_000);
  try {
    const launch = await launchTicket();
    if (opening !== attempt) return;
    const authorization = bridge?.authorization || 'Bearer ' + standaloneToken;
    const response = await fetch('/api/session', {
      method: 'POST', headers: { 'Content-Type': 'application/json', Authorization: authorization },
      body: JSON.stringify({ initData, ticket: launch }), redirect: 'error', cache: 'no-store', signal: attempt.signal,
    });
    if (opening !== attempt) return;
    if (response.status === 403) return problem('access_denied');
    if (!response.ok) throw new Error(String(response.status));
    const { viewerToken, transport } = await response.json();
    if (opening !== attempt) return;
    if (typeof viewerToken !== 'string' || !/^[a-zA-Z0-9_-]+$/.test(viewerToken)) throw new Error('Invalid session');
    const address = new URL('/vnc', location.href);
    address.protocol = location.protocol === 'https:' ? 'wss:' : 'ws:';
    const protocols = ['binary', `viewer-${viewerToken}`];
    screen.hidden = false;
    if (transport === 'cdp') viewer = new Screencast(screen, address.href, protocols);
    else if (transport === undefined || transport === 'vnc') {
      viewer = new RFB(screen, address.href, { wsProtocols: protocols });
      viewer.scaleViewport = true;
    } else throw new Error('Unknown viewer transport');
    viewer.addEventListener('connect', () => {
      if (opening !== attempt) return;
      clearTimeout(startupTimer);
      connected = true;
      retries = 0;
      phase('live');
      status.textContent = label('live');
      hint.textContent = label('connected');
      controls.hidden = false;
      if (!bridge && telegram?.isVersionAtLeast?.('7.7')) telegram.disableVerticalSwipes();
      if (matchMedia('(pointer: fine)').matches) viewer.focus({ preventScroll: true });
    });
    viewer.addEventListener('disconnect', fail);
  } catch (error) {
    if (opening !== attempt) return;
    if (error.key === 'error.access_denied' || error.message === '403') return problem('access_denied');
    if (error.key === 'error.reopen') return problem('missing');
    fail();
  }
}

document.addEventListener('visibilitychange', () => {
  if (!closed && !document.hidden && !element('retry').hidden) void start();
});
element('retry').onclick = () => { retries = 0; void start(); };

function forwardText(text) {
  if (!connected || !text) return;
  if (viewer instanceof Screencast) {
    viewer.sendText(text);
    return;
  }
  for (const character of text) {
    const code = character.codePointAt(0);
    if (code >= 0x20 && code !== 0x7f) viewer.sendKey(keysyms.lookup(code), null);
  }
}

function sendText() {
  const text = keys.value;
  keys.value = '';
  forwardText(text);
}

function pasteText(text) {
  if (!connected) return;
  if (!text) {
    hint.textContent = label('paste_empty');
  } else if (text.length > 2048) {
    hint.textContent = label('paste_too_long');
  } else {
    forwardText(text);
    hint.textContent = label('connected');
  }
}

function sendKey(key, code) {
  if (viewer instanceof Screencast) viewer.sendKey(key, code || key);
  else viewer.sendKey(keyCodes[key], code || null);
}

element('close').addEventListener('click', () => {
  closed = true;
  stopTransport(true);
  if (bridge) { bridge.finish(); return; }
  if (returnToApp) {
    location.replace(`/app?tab=settings&lang=${language}${location.hash}`);
  } else {
    telegram?.close();
  }
});
window.addEventListener('pagehide', () => { closed = true; stopTransport(); });
function showKeyboard(show) {
  typing.hidden = !show;
  keyboard.setAttribute('aria-expanded', String(!typing.hidden));
  if (show) {
    keys.focus({ preventScroll: true });
  } else {
    keys.value = '';
    viewer?.focus({ preventScroll: true });
  }
}
keyboard.addEventListener('click', () => showKeyboard(typing.hidden));
paste.addEventListener('click', async () => {
  if (!connected) return;
  paste.disabled = true;
  try {
    pasteText(await navigator.clipboard.readText());
  } catch {
    if (!connected) return;
    showKeyboard(true);
    hint.textContent = label('paste_manually');
  } finally {
    paste.disabled = false;
  }
});
keys.addEventListener('paste', (event) => {
  if (!event.clipboardData) return;
  event.preventDefault();
  pasteText(event.clipboardData.getData('text/plain'));
});
keys.addEventListener('input', (event) => {
  if (!event.isComposing) sendText();
});
keys.addEventListener('compositionend', sendText);
keys.addEventListener('keydown', (event) => {
  if (event.isComposing || !connected || !Object.hasOwn(keyCodes, event.key)) return;
  event.preventDefault();
  sendKey(event.key, event.code);
});
element('remote-keys').addEventListener('click', (event) => {
  const key = event.target.closest('button')?.dataset.key;
  if (!connected || !key) return;
  sendKey(key, key);
  keys.focus({ preventScroll: true });
});

function scrollPage(canvas, clientX, clientY, steps) {
  for (let i = 0; i < Math.abs(steps); i++) {
    canvas.dispatchEvent(new WheelEvent('wheel', {
      clientX, clientY, deltaY: 3 * Math.sign(steps), deltaMode: WheelEvent.DOM_DELTA_LINE,
      bubbles: true, cancelable: true,
    }));
  }
}

controls.addEventListener('click', (event) => {
  const steps = Number(event.target.closest('[data-scroll]')?.dataset.scroll);
  if (!connected || !steps) return;
  const canvas = screen.querySelector('canvas');
  if (!canvas) return;
  const { left, top, width, height } = canvas.getBoundingClientRect();
  scrollPage(canvas, left + width / 2, top + height / 2, steps);
});

// noVNC maps a one-finger drag to a held mouse button. In fit mode we use it
// for page scrolling and leave taps and zoomed viewport panning to noVNC.
function handlePageSwipe(event) {
  if (!connected || zoomed || event.detail?.type !== 'drag') return;
  event.stopPropagation();
  const { clientX, clientY } = event.detail;
  if (event.type === 'gesturestart') {
    scrollDrag = { x: clientX, y: clientY, lastY: clientY };
  } else if (event.type === 'gestureend') {
    scrollDrag = undefined;
  } else if (scrollDrag) {
    const pixelsPerStep = 24;
    const steps = Math.trunc((scrollDrag.lastY - clientY) / pixelsPerStep);
    scrollDrag.lastY -= steps * pixelsPerStep;
    scrollPage(event.target, scrollDrag.x, scrollDrag.y, steps);
  }
}
for (const type of ['gesturestart', 'gesturemove', 'gestureend']) {
  screen.addEventListener(type, handlePageSwipe, { capture: true });
}

zoom.addEventListener('click', () => {
  if (!connected) return;
  zoomed = !zoomed;
  if (viewer instanceof Screencast) {
    viewer.setZoom(zoomed);
  } else {
    viewer.scaleViewport = !zoomed;
    viewer.clipViewport = zoomed;
    viewer.dragViewport = zoomed;
  }
  zoom.textContent = label(zoomed ? 'fit' : 'zoom_in');
  zoom.setAttribute('aria-pressed', String(zoomed));
  hint.textContent = label(zoomed ? 'zoomed' : 'connected');
});

init();
