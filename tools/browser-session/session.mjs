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

export async function checkPortal(page, origin = portalOrigin) {
  let reservationLoaded = false;
  let confirmedLogin;
  const loginNavigation = new Promise(resolve => { confirmedLogin = resolve; });
  const observeNavigation = (response) => {
    try {
      const request = response.request();
      if (!request.isNavigationRequest() || request.frame() !== page.mainFrame()) return;
      const route = new URL(response.url());
      if (onPage(route, origin, reservation) && response.ok()) reservationLoaded = true;
      if (reservationLoaded && onPage(route, origin, login) &&
          (response.ok() || response.status() === 401)) confirmedLogin('needs_login');
    } catch {
      // A detached frame cannot establish the portal session state.
    }
  };
  page.on('response', observeNavigation);
  try {
    const response = await page.goto(`${origin}${reservation}`, { waitUntil: 'domcontentloaded', timeout: 15_000 });
    const navigation = response && new URL(response.url());
    if (navigation?.origin === origin && response.status() === 401) {
      return 'needs_login';
    }
    if (!response?.ok()) {
      return 'unavailable';
    }

    if (onPage(navigation, origin, login)) {
      return 'needs_login';
    }
    if (!onPage(navigation, origin, reservation)) {
      return 'unavailable';
    }

    reservationLoaded = true;
    const form = page.getByPlaceholder('Wpisz lub wybierz z listy', { exact: true });
    return await Promise.race([
      loginNavigation,
      form.waitFor({ timeout: 10_000 }).then(() =>
        onPage(new URL(page.url()), origin, reservation) ? 'ready' : 'unavailable'),
    ]);
  } catch {
    return 'unavailable';
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
