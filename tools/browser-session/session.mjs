import { chromium } from 'playwright';

export const portalOrigin = 'https://portalpacjenta.luxmed.pl';
const portalPage = '/PatientPortal/NewPortal/Page';
const reservation = `${portalPage}/Reservation`;
const login = `${portalPage}/Account/Login`;

const refreshPath = '/patientportal/newportal/token/refresh';

// These observations come from existing portal traffic and fresh checks.
export function observeSession(context, {
  origin = portalOrigin,
  write = entry => console.info(JSON.stringify(entry)),
} = {}) {
  context.on('response', async (response) => {
    try {
      const url = new URL(response.url());
      if (url.origin !== origin || url.pathname.toLowerCase() !== refreshPath) return;
      const request = response.request();
      if (!['GET', 'POST'].includes(request.method()) ||
          new URL(request.frame().url()).origin !== origin) return;
      const at = new Date().toISOString();
      const headerNames = await response.allHeaders().then(Object.keys, () => null);
      write({
        event: 'luxmed_session_refresh_response',
        at,
        status: response.status(),
        from_service_worker: response.fromServiceWorker(),
        set_cookie_present: headerNames?.includes('set-cookie') ?? null,
        authorization_token_present: headerNames?.includes('authorization-token') ?? null,
      });
    } catch {
      // Detached frames and browser shutdown can race with passive observation.
    }
  });

  return (state) => {
    if (['ready', 'needs_login', 'unavailable'].includes(state)) {
      write({ event: 'luxmed_session_check', at: new Date().toISOString(), state });
    }
  };
}

export function openProfile(profileDir, { headless = false } = {}) {
  return chromium.launchPersistentContext(profileDir, {
    channel: 'chrome',
    chromiumSandbox: true,
    ignoreDefaultArgs: ['--password-store=basic', '--use-mock-keychain'],
    headless,
    acceptDownloads: false,
    timeout: 15_000,
  });
}

function onPage(url, origin, path) {
  return url.origin === origin && (url.pathname === path || url.pathname.startsWith(`${path}/`));
}

export function portalSessionState(status) {
  if (status === 200) return 'ready';
  if (status === 401) return 'needs_login';
  return 'unavailable';
}

export async function checkPortal(page, origin = portalOrigin) {
  return portalSessionState((await probePortal(page, origin)).status);
}

function throttledNavigation(response) {
  const retryAfter = response.headers()['retry-after'];
  return { status: 429, ...(retryAfter && { retryAfter }) };
}

export async function probePortal(page, origin = portalOrigin) {
  let reservationLoaded = false;
  let navigationResult;
  let finishNavigation;
  const navigationFinished = new Promise(resolve => { finishNavigation = resolve; });
  const observeNavigation = (response) => {
    try {
      const request = response.request();
      if (navigationResult || !request.isNavigationRequest() || request.frame() !== page.mainFrame()) return;
      const route = new URL(response.url());
      const status = response.status();
      const isReservation = onPage(route, origin, reservation);
      if (isReservation && (response.ok() || (status >= 300 && status < 400))) {
        reservationLoaded = true;
      }
      if (isReservation && status === 429) {
        navigationResult = throttledNavigation(response);
      } else if ((isReservation && status === 401) ||
                 (reservationLoaded && onPage(route, origin, login) && (response.ok() || status === 401))) {
        navigationResult = { status: 401 };
      }
      if (navigationResult) finishNavigation(navigationResult);
    } catch {
      // A detached frame cannot establish the portal session state.
    }
  };
  page.on('response', observeNavigation);
  try {
    const response = await page.goto(`${origin}${reservation}`, { waitUntil: 'domcontentloaded', timeout: 15_000 });
    if (navigationResult) return navigationResult;
    const navigation = response && new URL(response.url());
    if (navigation?.origin === origin && response.status() === 401) {
      return { status: 401 };
    }
    if (navigation && onPage(navigation, origin, reservation) && response.status() === 429) {
      return throttledNavigation(response);
    }
    if (!response?.ok()) {
      return { status: 503 };
    }

    if (onPage(navigation, origin, login)) {
      return { status: 401 };
    }
    if (!onPage(navigation, origin, reservation)) {
      return { status: 503 };
    }

    reservationLoaded = true;
    const form = page.getByPlaceholder('Wpisz lub wybierz z listy', { exact: true });
    return await Promise.race([
      navigationFinished,
      form.waitFor({ timeout: 10_000 }).then(() => ({
        status: onPage(new URL(page.url()), origin, reservation) ? 200 : 503,
      })),
    ]);
  } catch {
    return navigationResult ?? { status: 503 };
  } finally {
    page.off('response', observeNavigation);
  }
}

export async function waitForLogin(page, origin = portalOrigin) {
  try {
    await page.waitForURL(
      (url) => onPage(url, origin, `${portalPage}/Dashboard`) || onPage(url, origin, reservation),
      { waitUntil: 'domcontentloaded', timeout: 10 * 60_000 },
    );
  } catch {
    return 'needs_login';
  }
  return checkPortal(page, origin);
}
