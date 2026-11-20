import { chromium } from 'playwright';

export const portalOrigin = 'https://portalpacjenta.luxmed.pl';
const portalPage = '/PatientPortal/NewPortal/Page';
const reservation = `${portalPage}/Reservation`;
const login = `${portalPage}/Account/Login`;

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
  try {
    const response = await page.goto(`${origin}${reservation}`, { waitUntil: 'domcontentloaded', timeout: 15_000 });
    if (!response?.ok()) {
      return 'unavailable';
    }

    const route = new URL(page.url());
    if (onPage(route, origin, login)) {
      return 'needs_login';
    }
    if (!onPage(route, origin, reservation)) {
      return 'unavailable';
    }

    await page.getByPlaceholder('Wpisz lub wybierz z listy', { exact: true }).waitFor({ timeout: 10_000 });
    const current = new URL(page.url());
    return onPage(current, origin, reservation) ? 'ready' : 'unavailable';
  } catch {
    return onPage(new URL(page.url()), origin, login) ? 'needs_login' : 'unavailable';
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
