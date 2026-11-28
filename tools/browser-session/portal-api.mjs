export const allowedPortalResources = new Set([
  'Dictionary/cities',
  'Dictionary/serviceVariantsGroups',
  'Dictionary/facilitiesAndDoctors',
  'PopularServicesParameters/popularServicesParameters',
  'RecentSearchTermsParameters/recentSearchData',
  'Reservation/GetReferralsForReservation',
  'terms/index',
  'terms/oneDayTerms',
].map(path => `/PatientPortal/NewPortal/${path}`));

export const forgeryPath = '/PatientPortal/NewPortal/security/getforgerytoken';
const bookingPaths = new Set(['LockTerm', 'Confirm', 'ReleaseTerm']
  .map(action => '/PatientPortal/NewPortal/Reservation/' + action));

export function isBookingRequest(method, path, search) {
  if (method !== 'POST' || !bookingPaths.has(path)) return false;
  if (!path.endsWith('/ReleaseTerm')) return search === '';
  const params = new URLSearchParams(search);
  return params.size === 1 && /^[a-z0-9-]{1,128}$/i.test(params.get('reservationId') ?? '');
}

export async function readBookingBody(req) {
  if (!/^application\/json(?:;|$)/i.test(req.headers['content-type'] ?? '')) return;
  const parts = [];
  let size = 0;
  for await (const part of req) {
    size += part.length;
    if (size > 16384) return;
    parts.push(part);
  }
  try {
    const parsed = JSON.parse(Buffer.concat(parts).toString('utf8'));
    if (parsed && typeof parsed === 'object' && !Array.isArray(parsed)) return JSON.stringify(parsed);
  } catch {
    return undefined;
  }
}

export async function fetchCatalog(page, path, payload) {
  return page.evaluate(async ({ target, body, forgery }) => {
    const headers = { Accept: 'application/json', 'X-Requested-With': 'XMLHttpRequest' };
    if (body) {
      const tokenResponse = await fetch(forgery, { credentials: 'same-origin', redirect: 'error', headers, signal: AbortSignal.timeout(6000) });
      if (!tokenResponse.ok) return { status: tokenResponse.status };
      const { token } = await tokenResponse.json();
      if (typeof token !== 'string' || !token) return { status: 502 };
      headers['XSRF-TOKEN'] = token;
      headers['Content-Type'] = 'application/json';
    }
    const response = await fetch(target, {
      credentials: 'same-origin', redirect: body ? 'error' : 'follow', headers,
      ...(body && { method: 'POST', body }),
      signal: AbortSignal.timeout(8_000),
    });
    if (response.redirected) {
      const final = new URL(response.url);
      const loginPath = /^\/PatientPortal\/(?:NewPortal\/Page\/)?Account\/Login\/?$/i;
      return { status: final.origin === location.origin && loginPath.test(final.pathname) ? 401 : 502 };
    }
    if (response.status === 0) return { status: 502 };
    if (!response.ok) {
      const retryAfter = response.headers.get('Retry-After');
      const status = body && response.status === 409 ? 502 : response.status;
      return { status, ...(retryAfter && { retryAfter }) };
    }
    if (!response.headers.get('Content-Type')?.toLowerCase().includes('json')) return { status: 502 };
    const reader = response.body.getReader();
    const decoder = new TextDecoder();
    let bytes = 0;
    let text = '';
    for (;;) {
      const { done, value } = await reader.read();
      if (done) break;
      bytes += value.length;
      if (bytes > 8 * 1024 * 1024) {
        await reader.cancel();
        return { status: 502 };
      }
      text += decoder.decode(value, { stream: true });
    }
    return { status: 200, body: text + decoder.decode() };
  }, { target: path, body: payload, forgery: forgeryPath });
}
