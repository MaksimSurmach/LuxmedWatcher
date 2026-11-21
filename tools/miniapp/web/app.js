import RFB from '/vendor/core/rfb.js';
import keysyms from '/vendor/core/input/keysymdef.js';
import { initLogging } from '/vendor/core/util/logging.js';

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

const copy = {
  en: {
    title: 'Sign in to LuxMed', screen: 'LuxMed browser', close: 'Close', keyboard: 'Keyboard',
    keyboardLabel: 'Tap a field in LuxMed, then type here.', backspace: 'Backspace',
    zoomIn: 'Zoom in', fit: 'Fit to screen', zoomed: 'Drag to pan. Use the arrows to scroll the page.',
    scrollUp: 'Scroll page up', scrollDown: 'Scroll page down',
    starting: 'Connecting…', connected: 'Swipe or use the arrows to scroll the page.',
    missing: 'Open a new sign-in link from your private bot chat.',
    failed: 'Connection failed. Ask the bot for a new link.',
    disconnected: 'The sign-in window is closed. Return to the bot.',
  },
  ru: {
    title: 'Вход в LuxMed', screen: 'Окно LuxMed', close: 'Закрыть', keyboard: 'Клавиатура',
    keyboardLabel: 'Коснитесь поля в LuxMed, затем печатайте здесь.', backspace: 'Стереть',
    zoomIn: 'Увеличить', fit: 'По размеру экрана', zoomed: 'Перетаскивайте экран. Стрелки прокручивают страницу.',
    scrollUp: 'Прокрутить страницу вверх', scrollDown: 'Прокрутить страницу вниз',
    starting: 'Подключение…', connected: 'Прокручивайте страницу свайпом или стрелками.',
    missing: 'Откройте новую ссылку на вход из личного чата с ботом.',
    failed: 'Не удалось подключиться. Запросите у бота новую ссылку.',
    disconnected: 'Окно входа закрыто. Вернитесь к боту.',
  },
  pl: {
    title: 'Zaloguj się do LUX MED', screen: 'Okno LUX MED', close: 'Zamknij', keyboard: 'Klawiatura',
    keyboardLabel: 'Dotknij pola w LUX MED, a następnie pisz tutaj.', backspace: 'Usuń',
    zoomIn: 'Powiększ', fit: 'Dopasuj do ekranu', zoomed: 'Przeciągaj obraz. Strzałki przewijają stronę.',
    scrollUp: 'Przewiń stronę w górę', scrollDown: 'Przewiń stronę w dół',
    starting: 'Łączenie…', connected: 'Przesuń palcem lub użyj strzałek, aby przewinąć stronę.',
    missing: 'Otwórz nowy link do logowania z prywatnej rozmowy z botem.',
    failed: 'Nie udało się połączyć. Poproś bota o nowy link.',
    disconnected: 'Okno logowania zamknięte. Wróć do bota.',
  },
};
const language = (telegram?.initDataUnsafe?.user?.language_code ?? navigator.language).slice(0, 2);
const labels = Object.hasOwn(copy, language) ? copy[language] : copy.en;
document.documentElement.lang = Object.hasOwn(copy, language) ? language : 'en';
document.title = labels.title;
const element = (id) => document.getElementById(id);
const status = element('status');
const screen = element('screen');
const controls = element('controls');
const keyboard = element('keyboard');
const typing = element('typing');
const keys = element('keys');
const zoom = element('zoom');
for (const id of ['title', 'close', 'keyboard', 'keyboardLabel', 'backspace']) {
  element(id).textContent = labels[id];
}
screen.setAttribute('aria-label', labels.screen);
element('scroll-up').setAttribute('aria-label', labels.scrollUp);
element('scroll-down').setAttribute('aria-label', labels.scrollDown);
zoom.textContent = labels.zoomIn;
status.textContent = labels.starting;
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
  status.textContent = message;
}

async function start() {
  if (!initData || !ticket) {
    status.textContent = labels.missing;
    return;
  }
  if (!window.isSecureContext) {
    close(labels.failed);
    return;
  }
  startupTimer = setTimeout(() => close(labels.failed), 30_000);
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
      status.textContent = labels.connected;
      controls.hidden = false;
        if (telegram?.isVersionAtLeast?.('7.7')) telegram.disableVerticalSwipes();
      if (matchMedia('(pointer: fine)').matches) viewer.focus({ preventScroll: true });
    });
    viewer.addEventListener('disconnect', () => close(connected ? labels.disconnected : labels.failed));
  } catch {
    close(labels.failed);
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
  close(labels.disconnected);
  if (returnToApp) {
    location.replace('/app?tab=settings' + location.hash);
  } else {
    telegram?.close();
  }
});
window.addEventListener('pagehide', () => close(labels.disconnected));
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
  zoom.textContent = zoomed ? labels.fit : labels.zoomIn;
  zoom.setAttribute('aria-pressed', String(zoomed));
  status.textContent = zoomed ? labels.zoomed : labels.connected;
});

void start();
