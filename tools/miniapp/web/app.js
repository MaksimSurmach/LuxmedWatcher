import RFB from '/vendor/core/rfb.js';
import keysyms from '/vendor/core/input/keysymdef.js';
import { initLogging } from '/vendor/core/util/logging.js';
import { initialLanguage, loadLanguage, t } from '/language.js';

initLogging('none');

const telegram = window.Telegram?.WebApp;
const initData = telegram?.initData ?? '';
const locationURL = new URL(location.href);
const ticket = locationURL.searchParams.get('ticket') || new URLSearchParams(initData).get('start_param');
const returnToApp = locationURL.searchParams.get('return') === 'app';
if (locationURL.searchParams.has('ticket')) {
  locationURL.searchParams.delete('ticket');
  history.replaceState(history.state, '', locationURL);
}

const element = (id) => document.getElementById(id);
const status = element('status');
const screen = element('screen');
const controls = element('controls');
const keyboard = element('keyboard');
const typing = element('typing');
const keys = element('keys');
const zoom = element('zoom');
const label = (name) => t(`viewer.${name}`);

function renderLanguage() {
  const labels = {
    title: 'title', close: 'close', keyboard: 'keyboard', keyboardLabel: 'keyboard_hint',
    backspace: 'backspace', 'tab-key': 'tab_key', 'enter-key': 'enter_key',
  };
  for (const [id, key] of Object.entries(labels)) element(id).textContent = label(key);
  document.title = label('title');
  screen.setAttribute('aria-label', label('screen'));
  element('scroll-up').setAttribute('aria-label', label('scroll_up'));
  element('scroll-down').setAttribute('aria-label', label('scroll_down'));
  zoom.textContent = label('zoom_in');
  status.textContent = label('starting');
}

telegram?.ready();
telegram?.expand();
window.visualViewport?.addEventListener('resize', () => {
  document.documentElement.style.setProperty('--visual-viewport-height', `${window.visualViewport.height}px`);
});

const keyCodes = { Tab: 0xff09, Backspace: 0xff08, Enter: 0xff0d };
const opening = new AbortController();
let viewer;
let startupTimer;
let connected = false;
let closed = false;
let zoomed = false;
let scrollDrag;

function close(message) {
  if (closed) return;
  closed = true;
  connected = false;
  clearTimeout(startupTimer);
  opening.abort();
  viewer?.disconnect();
  keys.value = '';
  typing.hidden = true;
  controls.hidden = true;
  status.textContent = label(message) || 'Nie udało się wczytać okna logowania.';
}

async function start() {
  try {
    await loadLanguage(initialLanguage(telegram, locationURL.searchParams.get('lang')));
    renderLanguage();
  } catch {
    close('failed');
    return;
  }
  if (closed) {
    status.textContent = label('disconnected');
    return;
  }
  if (!initData || !ticket) {
    status.textContent = label('missing');
    return;
  }
  if (!window.isSecureContext) {
    close('failed');
    return;
  }
  startupTimer = setTimeout(() => close('failed'), 30_000);
  try {
    const response = await fetch('/api/session', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ initData, ticket }),
      redirect: 'error',
      cache: 'no-store',
      signal: opening.signal,
    });
    if (!response.ok) throw new Error('Request failed');
    const { viewerToken } = await response.json();
    if (closed) return;
    if (typeof viewerToken !== 'string' || !/^[a-zA-Z0-9_-]+$/.test(viewerToken)) {
      throw new Error('Invalid session');
    }

    const address = new URL('/vnc', location.href);
    address.protocol = location.protocol === 'https:' ? 'wss:' : 'ws:';
    viewer = new RFB(screen, address.href, { wsProtocols: ['binary', `viewer-${viewerToken}`] });
    viewer.scaleViewport = true;
    viewer.addEventListener('connect', () => {
      if (closed) return;
      clearTimeout(startupTimer);
      connected = true;
      status.textContent = label('connected');
      controls.hidden = false;
      if (telegram?.isVersionAtLeast?.('7.7')) telegram.disableVerticalSwipes();
      if (matchMedia('(pointer: fine)').matches) viewer.focus({ preventScroll: true });
    });
    viewer.addEventListener('disconnect', () => close(connected ? 'disconnected' : 'failed'));
  } catch {
    close('failed');
  }
}

function sendText() {
  const text = keys.value;
  keys.value = '';
  if (!connected) return;
  for (const character of text) {
    const code = character.codePointAt(0);
    if (code >= 0x20 && code !== 0x7f) viewer.sendKey(keysyms.lookup(code), null);
  }
}

element('close').addEventListener('click', () => {
  close('disconnected');
  if (returnToApp) {
    location.replace('/app?tab=settings' + location.hash);
  } else {
    telegram?.close();
  }
});
window.addEventListener('pagehide', () => close('disconnected'));
keyboard.addEventListener('click', () => {
  typing.hidden = !typing.hidden;
  keyboard.setAttribute('aria-expanded', String(!typing.hidden));
  if (!typing.hidden) {
    keys.focus({ preventScroll: true });
  } else {
    keys.value = '';
    viewer?.focus({ preventScroll: true });
  }
});
keys.addEventListener('input', (event) => {
  if (!event.isComposing) sendText();
});
keys.addEventListener('compositionend', sendText);
keys.addEventListener('keydown', (event) => {
  if (event.isComposing || !connected || !Object.hasOwn(keyCodes, event.key)) return;
  event.preventDefault();
  viewer.sendKey(keyCodes[event.key], event.code || null);
});
element('remote-keys').addEventListener('click', (event) => {
  const key = event.target.closest('button')?.dataset.key;
  if (!connected || !key) return;
  viewer.sendKey(keyCodes[key], key);
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
  viewer.scaleViewport = !zoomed;
  viewer.clipViewport = zoomed;
  viewer.dragViewport = zoomed;
  zoom.textContent = label(zoomed ? 'fit' : 'zoom_in');
  zoom.setAttribute('aria-pressed', String(zoomed));
  status.textContent = label(zoomed ? 'zoomed' : 'connected');
});

void start();
