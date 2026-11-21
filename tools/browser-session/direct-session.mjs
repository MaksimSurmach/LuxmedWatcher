import { request } from 'playwright';
import { readProfile, writeProfile } from './profile-store.mjs';
import { portalOrigin } from './session.mjs';

const refreshPath = '/PatientPortal/NewPortal/Token/Refresh';
const sessionProbe = '/PatientPortal/NewPortal/Dictionary/cities';
const maxResponseBytes = 8 * 1024 * 1024;

function cookieFor(state, origin, name) {
  const host = new URL(origin).hostname;
  return state?.cookies.find(c => {
    const domain = c.domain.replace(/^\./, '');
    return c.name.toLowerCase() === name && (host === domain || host.endsWith('.' + domain)) &&
      (c.expires === -1 || c.expires * 1000 > Date.now());
  });
}

// Read the expiry as a scheduling hint. LuxMed decides whether the token is valid.
function expiry(state, origin) {
  const token = cookieFor(state, origin, 'authorization-token');
  if (!token || token.value.length > 16_384) return 0;
  try {
    const { exp } = JSON.parse(Buffer.from(token.value.split('.')[1], 'base64url'));
    return Number.isSafeInteger(exp) && exp > 0 && exp <= 253402300799 ? exp * 1000 : 0;
  } catch {
    return 0;
  }
}

function responseStatus(response, origin) {
  const status = response.status();
  if (status >= 300 && status < 400) {
    const location = response.headers().location;
    if (location) {
      const redirect = new URL(location, origin);
      if (redirect.origin === origin && redirect.pathname.toLowerCase().endsWith('/account/login')) return 401;
    }
    return 502;
  }
  return status;
}

export async function requestPortalResource(profileDir, id, path, {
  origin = portalOrigin,
  report = record => console.info(JSON.stringify(record)),
} = {}) {
  let state = await readProfile(profileDir, id);
  if (!cookieFor(state, origin, 'authorization-token') && !cookieFor(state, origin, 'refreshtoken')) {
    return { status: 401 };
  }

  const client = await request.newContext({
    baseURL: origin, storageState: state, timeout: 8_000,
    extraHTTPHeaders: { Accept: 'application/json', 'X-Requested-With': 'XMLHttpRequest' },
  });
  const get = resource => client.get(resource, { maxRedirects: 0, maxRetries: 0 });

  async function saveCookies(next) {
    state = { ...state, cookies: next.cookies };
    await writeProfile(profileDir, id, state);
  }

  async function refresh() {
    if (!cookieFor(state, origin, 'refreshtoken')) return { status: 401 };
    const before = expiry(state, origin);
    const response = await get(refreshPath);
    try {
      let status = responseStatus(response, origin);
      let after;
      if (status === 200) {
        const next = await client.storageState();
        after = expiry(next, origin);
        if (after > Math.max(before, Date.now())) await saveCookies(next);
        else status = 401;
      }
      report({
        event: 'luxmed_http_refresh', at: new Date().toISOString(), status,
        ...(status === 200 && { expires_at: new Date(after).toISOString() }),
      });
      return { status, retryAfter: response.headers()['retry-after'] };
    } finally {
      await response.dispose();
    }
  }

  async function fetchResource() {
    const response = await get(path === '/session' ? sessionProbe : path);
    try {
      const status = responseStatus(response, origin);
      const headers = response.headers();
      if (status !== 200) return { status, retryAfter: headers['retry-after'] };
      if (Number(headers['content-length']) > maxResponseBytes ||
          !headers['content-type']?.toLowerCase().includes('json')) return { status: 502 };
      // shortcut: this client buffers one response; switch to a streaming client if the upstream exceeds the size budget.
      const body = await response.body();
      if (body.byteLength > maxResponseBytes) return { status: 502 };
      if (headers['set-cookie']) await saveCookies(await client.storageState());
      if (path === '/session') return { status };
      if (path.startsWith('/PatientPortal/NewPortal/terms/')) {
        report({ event: 'luxmed_http_search', at: new Date().toISOString(), status, bytes: body.byteLength });
      }
      return { status, body };
    } finally {
      await response.dispose();
    }
  }

  try {
    if (expiry(state, origin) <= Date.now()) {
      const renewed = await refresh();
      return renewed.status === 200 ? await fetchResource() : renewed;
    }

    const result = await fetchResource();
    if (result.status !== 401) return result;
    const renewed = await refresh();
    return renewed.status === 200 ? await fetchResource() : renewed;
  } finally {
    await client.dispose();
  }
}
