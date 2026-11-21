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

export async function fetchCatalog(page, path) {
  return page.evaluate(async (target) => {
    const response = await fetch(target, {
      credentials: 'same-origin', redirect: 'manual',
      headers: { Accept: 'application/json', 'X-Requested-With': 'XMLHttpRequest' },
      signal: AbortSignal.timeout(8_000),
    });
    if ([0, 401].includes(response.status)) return { status: 401 };
    if (!response.ok) return { status: response.status };
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
  }, path);
}
