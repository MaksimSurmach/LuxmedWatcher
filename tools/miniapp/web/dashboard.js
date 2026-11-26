import { initialLanguage, language, languages, loadLanguage, t } from '/language.js';

const telegram = window.Telegram?.WebApp;
const main = document.querySelector('#main');
const navigation = document.querySelector('#navigation');
const announcement = document.querySelector('#announcement');
const portal = 'https://portalpacjenta.luxmed.pl/PatientPortal/NewPortal/Page/Reservation';
const referralsPortal = 'https://portalpacjenta.luxmed.pl/PatientPortal/NewPortal/Page/Referrals';
const icons = { home: '⌂', calendar: '▦', history: '▤', settings: '⚙', language: '◎', plus: '+', pin: '⌖', link: '↗' };
const icon = (name) => `<span class="icon" aria-hidden="true">${icons[name]}</span>`;
const escape = (value) => String(value ?? '').replace(/[&<>"']/g, (char) => ({
  '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;',
})[char]);
const localDate = new Intl.DateTimeFormat('sv-SE', { timeZone: 'Europe/Warsaw' });
const dateOnly = (date) => date ? localDate.format(new Date(date)) : '';
const dateText = (date) => shortDate.format(new Date(date));
const text = (key, values) => escape(t(key, values));

class DashboardError extends Error {
  constructor(key, fallback = '') {
    super(t(key) || fallback);
    this.key = key;
  }
}

function errorText(error) {
  return error instanceof DashboardError ? error.message : t('error.unavailable');
}
const tabs = ['home', 'watches', 'history', 'settings'];
const titleFor = (tab) => t(`tabs.${tab}`);
let shortDate;
let clock;
let weekdays;
let state;
let apiToken;
let cities;
let tab = 'home';
let account = 'unchecked';
let flashTimer;
let renderID = 0;

for (const target of document.querySelectorAll('[data-icon]')) target.innerHTML = icon(target.dataset.icon);

function renderLanguage() {
  const timeZone = 'Europe/Warsaw';
  shortDate = new Intl.DateTimeFormat(language, { timeZone, day: 'numeric', month: 'short' });
  clock = new Intl.DateTimeFormat(language, { timeZone, hour: '2-digit', minute: '2-digit' });
  const weekday = new Intl.DateTimeFormat(language, { timeZone, weekday: 'short' });
  // 1 January 2024 was a Monday, matching the schedule’s weekday order.
  weekdays = [t('editor.every_day'), ...Array.from({ length: 7 }, (_, index) =>
    weekday.format(Date.UTC(2024, 0, index + 1)))];
  navigation.setAttribute('aria-label', t('dashboard.navigation'));
  for (const button of navigation.querySelectorAll('button')) {
    button.lastElementChild.textContent = titleFor(button.dataset.tab);
  }
}

function flash(message) {
  clearTimeout(flashTimer);
  announcement.textContent = message;
  flashTimer = setTimeout(() => { announcement.textContent = ''; }, 4000);
}

async function api(action, data = {}) {
  let response;
  const headers = { 'Content-Type': 'application/json' };
  if (action === 'auth' || action === 'connect') headers['X-Telegram-Init-Data'] = telegram.initData;
  if (apiToken) headers.Authorization = `Bearer ${apiToken}`;
  try {
    response = await fetch(`/api/dashboard/${action}`, {
      method: 'POST', redirect: 'error', cache: 'no-store',
      headers,
      body: JSON.stringify(data), signal: AbortSignal.timeout(19_000),
    });
    if (response.status === 204) return;
    if (response.ok) return await response.json();
  } catch {
    throw new DashboardError('error.unavailable');
  }
  if (response.status === 401) throw new DashboardError('error.reopen');
  if (response.status === 403) throw new DashboardError('error.access_denied');
  if (response.status === 429) throw new DashboardError('error.browser_busy');
  if (response.status === 409) throw new DashboardError('error.connect_first');
  if (response.status === 400) throw new DashboardError('error.invalid_fields');
  if (response.status === 404) throw new DashboardError('error.stale');
  throw new DashboardError('error.unavailable');
}

async function run(button, action, errorTarget) {
  button.disabled = true;
  if (errorTarget) errorTarget.textContent = '';
  try {
    await action();
  } catch (error) {
    if (errorTarget) errorTarget.textContent = errorText(error);
    else flash(errorText(error));
  } finally {
    button.disabled = false;
  }
}

function show(title, html, focus = true) {
  main.innerHTML = `<h1>${escape(title)}</h1>${html}`;
  if (focus) {
    window.scrollTo(0, 0);
    main.focus({ preventScroll: true });
  }
}

// The portal connection is only checked when the user asks (never automatically).
// Compact: a status line with the two actions, used on Home. Full: adds instructions, used in Settings.
function accountSection(compact = false) {
  return `<section class="card stack account${compact ? ' compact' : ''}" data-state="${account}" aria-labelledby="account-heading">
    <div class="account-head">
      <h2 id="account-heading">${text('account.title')}</h2>
      <p class="account-status" data-state="${account}" role="status">${text(`account.${account}`)}</p>
    </div>
    <div class="account-actions">
      <button type="button" class="secondary" id="connect"${compact ? '' : ' aria-describedby="account-instructions"'}>${text('account.sign_in')} ${icon('link')}</button>
      <button type="button" class="secondary" id="account-check">${text('settings.check_account')}</button>
    </div>
    <p class="muted error" data-account-error role="status"></p>
    ${compact ? '' : `<p class="muted" id="account-instructions">${text('account.instructions')}</p>`}
  </section>`;
}

function bindAccount() {
  const failure = main.querySelector('[data-account-error]');
  const connect = main.querySelector('#connect');
  connect.onclick = () => void run(connect, async () => {
    const { url } = await api('connect');
    const target = new URL(url);
    if (target.origin !== location.origin || target.pathname !== '/') throw new DashboardError('error.link');
    target.searchParams.set('return', 'app');
    target.hash = location.hash;
    location.assign(target.href);
  }, failure);
  const check = main.querySelector('#account-check');
  check.onclick = () => void run(check, async () => {
    account = (await api('account')).state;
    const section = main.querySelector('.account');
    const status = main.querySelector('.account-status');
    if (section) section.dataset.state = account;
    if (status) {
      status.dataset.state = account;
      status.textContent = t(`account.${account}`);
    }
  }, failure);
}

function home() {
  const count = state.Watches?.length ?? 0;
  const searchHint = count ? text('home.saved_searches', { count }) : text('watches.empty');
  show(t('home.title'), `<p class="home-intro">${text('home.intro')}</p>
    <div class="stack home">
      <button type="button" class="primary" id="new-watch">${icon('plus')}${text('watches.new')}</button>
      <p class="home-count">${searchHint}</p>
      <div class="rows">
        <button type="button" class="row" id="recommendations"><span>${text('editor.recommendations')}</span></button>
        <a class="row" href="${referralsPortal}" target="_blank" rel="noopener"><span>${text('home.referrals')}</span>${icon('link')}</a>
      </div>
      ${accountSection(true)}
      <button type="button" class="row row-quiet" data-open="settings" id="change-language">
        <span>${icon('language')}${text('home.change_language')}</span><small lang="${language}">${escape(languages[language])}</small>
      </button>
    </div>`);
  main.querySelector('#new-watch').onclick = () => void editor();
  main.querySelector('#recommendations').onclick = () => void editor(undefined, true);
  for (const button of main.querySelectorAll('[data-open]')) {
    button.onclick = async () => {
      await switchTab(button.dataset.open);
      if (button.id === 'change-language') main.querySelector('[name="locale"]')?.focus();
    };
  }
  bindAccount();
}

function hoursLabel(windows) {
  return windows.map(window => `${weekdays[window.weekday]} ${window.from}–${window.to}`).join(' · ');
}

const watchErrors = new Map([
  ['luxmed_account_missing', 'error.no_account'],
  ['luxmed_authentication', 'error.connect_first'],
  ['luxmed_unavailable', 'error.luxmed_unavailable'],
  ['luxmed_partial', 'watches.check_partial'],
]);

function watchCard(watch) {
  const active = watch.Status === 'active';
  const dates = watch.DateFrom && watch.DateTo
    ? `${dateText(watch.DateFrom)} — ${dateText(watch.DateTo)}`
    : text('watches.period_days', { days: watch.NextDays });
  const lastRun = watch.LastCheckedAt ? `${dateText(watch.LastCheckedAt)}, ${clock.format(new Date(watch.LastCheckedAt))}` : '';
  const minutes = Math.ceil(watch.CheckIntervalSeconds / 60);
  const checkError = watch.LastError ? watchErrors.get(watch.LastError) ?? 'watches.check_error' : '';
  const needsLogin = checkError === 'error.connect_first' || checkError === 'error.no_account';
  return `<article class="card watch" data-status="${active ? 'active' : 'paused'}">
    <div class="card-head"><h2 id="watch-${watch.ID}">${escape(watch.ServiceName || watch.Name)}</h2>
      <span class="badge ${active ? 'active' : ''}">${text(active ? 'watches.active' : 'watches.paused')}</span></div>
    <p class="meta">${icon('pin')}${escape(watch.CityName)}</p>
    <p class="meta">${icon('calendar')}${dates}</p>
    ${watch.DoctorName ? `<p class="meta">${escape(watch.DoctorName)}</p>` : ''}
    ${watch.TimeWindows?.length ? `<p class="meta">${escape(hoursLabel(watch.TimeWindows))}</p>` : ''}
    ${minutes ? `<p class="meta">${text('watches.interval_card', { minutes })}</p>` : ''}
    ${lastRun ? `<p class="meta">${text('watches.checked')} · ${lastRun}</p>` : ''}
    ${checkError ? `<p class="meta error">${text(checkError)}</p>` : ''}
    <div class="actions">
      ${needsLogin ? `<button type="button" class="secondary" data-account-settings>${text('account.settings')}</button>` : ''}
      <button type="button" class="secondary" data-edit="${watch.ID}" aria-describedby="watch-${watch.ID}">${text('watches.edit')}</button>
      <button type="button" class="secondary" data-watch="${watch.ID}" data-status="${active ? 'paused' : 'active'}" aria-describedby="watch-${watch.ID}">${text(active ? 'watches.pause' : 'watches.resume')}</button>
    </div>
  </article>`;
}

function watches() {
  const items = state.Watches ?? [];
  show(titleFor('watches'), `<div class="stack">
    <button type="button" class="primary" id="new-watch">${icon('plus')}${text('watches.new')}</button>
    ${items.length ? items.map(watchCard).join('') : `<div class="card empty"><h2>${text('watches.empty')}</h2><p>${text('watches.empty_hint')}</p></div>`}
    ${items.length ? `<section class="stack">
      <p class="muted" id="check-hint">${text('watches.check_chat_hint')}</p>
      <button type="button" class="secondary" id="check-in-chat" aria-describedby="check-hint">${text('watches.check_chat')}</button>
    </section>` : ''}
  </div>`);
  main.querySelector('#new-watch').onclick = () => void editor();
  for (const button of main.querySelectorAll('[data-account-settings]')) {
    button.onclick = () => void switchTab('settings');
  }
  for (const button of main.querySelectorAll('[data-edit]')) {
    button.onclick = () => void editor(items.find((item) => item.ID === Number(button.dataset.edit)));
  }
  for (const button of main.querySelectorAll('[data-watch]')) {
    button.onclick = () => void run(button, async () => {
      await api('status', { ID: Number(button.dataset.watch), Status: button.dataset.status });
      await update('watches');
    });
  }
  const chat = main.querySelector('#check-in-chat');
  if (chat) chat.onclick = () => {
    if (telegram?.close) telegram.close();
    else flash(t('watches.check_chat_hint'));
  };
}

function slotExpired(slot, now) {
  return slot.Status === 'expired' || new Date(slot.DateTime) <= now;
}

function slotCard(slot, now) {
  const date = new Date(slot.DateTime);
  const expired = slotExpired(slot, now);
  const parts = shortDate.formatToParts(date);
  const part = (name) => parts.find((item) => item.type === name)?.value;
  return `<article class="card slot${expired ? ' expired' : ''}">
    <div class="date-tile"><strong>${escape(part('day'))}</strong>${escape(part('month'))}</div>
    <div class="slot-body">
      <h2>${clock.format(date)}</h2>
      ${slot.Facility ? `<p class="slot-clinic">${escape(slot.Facility)}</p>` : ''}
      ${slot.Address ? `<p class="meta">${escape(slot.Address)}</p>` : ''}
      ${slot.Doctor ? `<p class="meta">${escape(slot.Doctor)}</p>` : ''}
      <p class="meta slot-service">${escape(slot.Service || t('history.appointment'))}</p>
      ${expired ? `<p class="muted">${text(date <= now ? 'history.expired' : 'history.unavailable')}</p>` : ''}
    </div>
    ${expired ? '' : `<a class="primary" href="${portal}" target="_blank" rel="noopener">${text('history.open_portal')} ${icon('link')}</a>`}
  </article>`;
}

async function historyScreen(id) {
  show(titleFor('history'), `<div class="stack" id="slots"><p class="muted">${text('history.loading')}</p></div>`);
  const results = main.querySelector('#slots');
  async function append(offset) {
    const { Slots, More } = await api('history', { Offset: offset });
    if (id !== renderID) return;
    results.querySelector('button')?.remove();
    const now = new Date();
    const cards = Slots.map((slot) => slotCard(slot, now)).join('');
    if (!offset) results.replaceChildren();
    if (!results.querySelector('[data-found-note]') && Slots.some((slot) => !slotExpired(slot, now))) {
      results.insertAdjacentHTML('beforeend', `<p class="muted" data-found-note>${text('history.found_note')}</p>`);
    }
    results.insertAdjacentHTML('beforeend', cards || `<div class="card empty"><h2>${text('history.empty')}</h2><p>${text('history.empty_hint')}</p></div>`);
    if (More) {
      results.insertAdjacentHTML('beforeend', `<button class="secondary" type="button">${text('common.more')}</button>`);
      const next = results.lastElementChild;
      next.onclick = () => void run(next, () => append(offset + Slots.length));
    }
  }
  await append(0);
}

async function getCities() {
  if (!cities) cities = await api('cities');
  return cities ?? [];
}

const option = (id, name) => `<option value="${Number(id)}">${escape(name)}</option>`;
function cityOptions(items, selected, placeholder) {
  if (selected?.ID && !items.some(({ ID }) => ID === selected.ID)) items = [selected, ...items];
  return option(0, placeholder) + items.map(({ ID, Name }) => option(ID, Name)).join('');
}

async function settingsScreen(id) {
  show(titleFor('settings'), `<div class="stack">
    ${accountSection()}
    <form class="stack">
      <section class="card stack" aria-labelledby="preferences-heading">
        <h2 id="preferences-heading">${text('settings.preferences')}</h2>
        <label class="field">${text('settings.default_city')}<select name="city"></select></label>
        <button type="button" class="secondary" id="load-cities" hidden>${text('settings.load_cities')}</button>
        <label class="field">${text('settings.language')}<select name="locale">
          ${Object.entries(languages).map(([code, name]) => `<option value="${code}" lang="${code}">${escape(name)}</option>`).join('')}
        </select></label>
      </section>
      <p class="muted error" data-error role="status"></p>
      <button type="submit" class="primary">${text('common.save')}</button>
    </form></div>`);
  const form = main.querySelector('form');
  const error = form.querySelector('[data-error]');
  const loadCities = main.querySelector('#load-cities');
  const selected = { ID: state.CityID, Name: state.CityName };
  function fillCities(items) {
    form.elements.city.innerHTML = cityOptions(items, selected, t('settings.no_city'));
    form.elements.city.value = selected.ID ?? 0;
  }
  fillCities(cities ?? []);
  loadCities.hidden = Boolean(cities);
  loadCities.onclick = () => void run(loadCities, async () => {
    const items = await getCities();
    if (id === renderID) {
      fillCities(items);
      loadCities.hidden = true;
    }
  }, error);
  form.elements.locale.value = Object.hasOwn(languages, state.Locale) ? state.Locale : language;
  form.onsubmit = (event) => {
    event.preventDefault();
    void run(form.querySelector('[type="submit"]'), async () => {
      await api('settings', { Locale: form.elements.locale.value, CityID: Number(form.elements.city.value) });
      await update('settings');
      flash(t('settings.saved'));
    }, error);
  };
  bindAccount();
}

async function editor(watch, focusPicks = false) {
  const id = ++renderID;
  const title = t(watch ? 'editor.edit' : 'watches.new');
  show(title, `<p class="muted">${text('editor.loading_cities')}</p>`);
  let options;
  try {
    options = await getCities();
  } catch (error) {
    if (id === renderID) screenError(title, error, () => editor(watch));
    return;
  }
  if (id !== renderID) return;
  const city = watch ? { ID: watch.CityID, Name: watch.CityName } : { ID: state.CityID, Name: state.CityName };
  let service = watch ? { ID: watch.ServiceID, Name: watch.ServiceName } : null;
  let doctor = watch?.DoctorID ?? 0;
  let facilities = new Set(watch?.FacilityIDs);
  const existingHours = new Map((watch?.TimeWindows ?? []).map((window) => [window.weekday, window]));
  const advancedOpen = Boolean(doctor || facilities.size);
  show(title, `<form class="stack">
    <fieldset class="card stack"><legend>${text('editor.what')}</legend>
      <label class="field">${text('editor.city')}<select name="city" required>${cityOptions(options, city, t('editor.choose_city'))}</select></label>
      ${watch ? '' : `<section class="quick-services stack" tabindex="-1" aria-labelledby="recommendations-heading">
        <h2 id="recommendations-heading">${text('editor.recommendations')}</h2>
        <p class="muted" id="picks-status" role="status"></p>
        <div class="stack" id="picks-list"></div>
        <button type="button" class="secondary" id="retry-picks" hidden>${text('common.retry')}</button>
      </section>`}
      <p class="catalog-heading">${text('editor.catalog')}</p>
      <label class="field">${text('editor.service')}<input type="search" name="query" placeholder="${text('editor.service_placeholder')}" maxlength="80" autocomplete="off"></label>
      <button type="button" class="secondary" id="search-services">${text('editor.find_service')}</button>
      <div class="options" id="services" aria-live="polite"></div>
      <p class="selection" id="selected-service" tabindex="-1" data-set="false"></p>
      <a class="flat referrals" href="${referralsPortal}" target="_blank" rel="noopener">${text('editor.referrals')} ${icon('link')}</a>
    </fieldset>
    <fieldset class="card stack"><legend>${text('editor.when')}</legend>
      <label class="field">${text('editor.period')}<select name="dateMode"><option value="days">${text('editor.upcoming')}</option><option value="range">${text('editor.date_range')}</option></select></label>
      <label class="field" id="days">${text('editor.days_ahead')}<input name="days" type="number" min="1" max="90" value="${watch?.NextDays || 14}"></label>
      <div class="columns" id="range" hidden>
        <label class="field">${text('editor.from')}<input name="from" type="date" value="${dateOnly(watch?.DateFrom)}"></label>
        <label class="field">${text('editor.to')}<input name="to" type="date" value="${dateOnly(watch?.DateTo)}"></label>
      </div>
      <details><summary>${text('editor.days_and_hours')}</summary>
        <p class="muted">${text('editor.hours_hint')}</p>
        ${weekdays.map((day, index) => {
          const window = existingHours.get(index);
          return `<div class="time-row" data-day="${index}">
            <label class="option"><input type="checkbox" ${window ? 'checked' : ''}>${escape(day)}</label>
            <input type="time" aria-label="${text('editor.time_from', { day })}" value="${escape(window?.from || '00:00')}">
            <input type="time" aria-label="${text('editor.time_to', { day })}" value="${escape(window?.to || '23:59')}">
          </div>`;
        }).join('')}
      </details>
    </fieldset>
    <details class="card advanced" id="advanced"${advancedOpen ? ' open' : ''}>
      <summary>${text('editor.advanced')}</summary>
      <div class="stack">
        <button type="button" class="secondary" id="providers">${text('editor.choose_providers')}</button>
        <div class="stack" id="provider-options"></div>
        <label class="field">${text('editor.interval')}<input name="minutes" type="number" required min="${state.MinimumMinutes}" max="1440" value="${watch ? Math.ceil(watch.CheckIntervalSeconds / 60) : state.DefaultMinutes}"></label>
      </div>
    </details>
    <section class="card stack summary" aria-labelledby="summary-heading">
      <h2 id="summary-heading">${text('editor.summary')}</h2>
      <dl id="summary"></dl>
    </section>
    <p class="muted error" data-error role="status"></p>
    <button type="submit" class="primary">${text('editor.save')}</button>
    <button type="button" class="secondary" id="cancel">${text('common.cancel')}</button>
    ${watch ? `<button type="button" class="flat danger" id="delete">${text('editor.delete')}</button>` : ''}
  </form>`);
  const form = main.querySelector('form');
  const fields = form.elements;
  const results = form.querySelector('#services');
  const quickPicks = form.querySelector('.quick-services');
  const picksList = form.querySelector('#picks-list');
  const picksStatus = form.querySelector('#picks-status');
  const retryPicks = form.querySelector('#retry-picks');
  const picked = form.querySelector('#selected-service');
  const providers = form.querySelector('#providers');
  const providerOptions = form.querySelector('#provider-options');
  const summary = form.querySelector('#summary');
  const error = form.querySelector('[data-error]');
  let serviceSearch = 0;
  let serviceSearchOffset = 0;
  let picksRequest = 0;

  // A closed disclosure cannot show native validation; open it so the user sees the problem.
  form.addEventListener('invalid', (event) => {
    const details = event.target.closest('details');
    if (details) details.open = true;
  }, true);

  function providerSummary() {
    const select = providerOptions.querySelector('select');
    const doctorName = !doctor ? t('editor.any_doctor')
      : select ? select.selectedOptions[0]?.textContent : watch?.DoctorName;
    const boxes = [...providerOptions.querySelectorAll('[data-facility]:checked')];
    const clinics = !facilities.size ? t('editor.all_clinics')
      : (boxes.length ? boxes.map((box) => box.dataset.name) : watch?.FacilityNames ?? []).filter(Boolean).join(', ')
        || String(facilities.size);
    return { doctorName, clinics };
  }
  function selectedHours() {
    return [...form.querySelectorAll('[data-day]')].filter(row => row.querySelector('[type="checkbox"]').checked).map(row => {
      const [from, to] = row.querySelectorAll('input[type="time"]');
      return { weekday: Number(row.dataset.day), from: from.value, to: to.value };
    });
  }
  function summarize() {
    const range = fields.dateMode.value === 'range';
    const dates = !range ? t('watches.period_days', { days: fields.days.value })
      : fields.from.value && fields.to.value ? `${dateText(fields.from.value)} — ${dateText(fields.to.value)}` : '—';
    const { doctorName, clinics } = providerSummary();
    const hours = selectedHours();
    const rows = [
      ['editor.service', service?.Name ?? t('editor.choose_service')],
      ['editor.city', Number(fields.city.value) ? fields.city.selectedOptions[0]?.textContent : t('editor.choose_city')],
      ['editor.period', dates],
      ['editor.days_and_hours', hoursLabel(hours) || t('editor.any_time')],
      ['editor.doctor', doctorName],
      ['editor.clinics', clinics],
      ['editor.interval', fields.minutes.value ? t('watches.interval_card', { minutes: fields.minutes.value }) : '—'],
    ];
    summary.innerHTML = rows.map(([label, value]) => `<div><dt>${text(label)}</dt><dd>${escape(value)}</dd></div>`).join('');
  }
  function markSelection() {
    picked.textContent = service ? t('editor.selected_service', { service: service.Name }) : t('editor.choose_service');
    picked.dataset.set = String(Boolean(service));
    providers.disabled = !service;
    if (service && (doctor || facilities.size) && !providerOptions.querySelector('select')) {
      providerOptions.textContent = [watch?.DoctorName, ...(watch?.FacilityNames ?? [])].filter(Boolean).join(' · ');
    }
    for (const button of quickPicks?.querySelectorAll('[data-service]') ?? []) {
      button.setAttribute('aria-pressed', String(Number(button.dataset.service) === service?.ID));
    }
    summarize();
  }
  function resetProviders() {
    doctor = 0;
    facilities = new Set();
    providerOptions.replaceChildren();
  }
  function selectService(item) {
    if (service?.ID !== item.ID) resetProviders();
    service = item;
    serviceSearch++;
    results.replaceChildren();
    markSelection();
    picked.focus();
  }
  async function loadQuickPicks() {
    if (!quickPicks) return;
    const request = ++picksRequest;
    const cityID = Number(fields.city.value);
    picksList.replaceChildren();
    retryPicks.hidden = true;
    picksStatus.hidden = false;
    picksStatus.textContent = t(cityID ? 'editor.loading_recommendations' : 'editor.choose_city');
    if (!cityID) return;
    try {
      const { Referred, Recent, Saved, Popular } = await api('procedure-picks', { CityID: cityID });
      if (id !== renderID || request !== picksRequest) return;
      const groups = [
        ['editor.referred_services', Referred],
        ['editor.recent_services', Recent],
        ['editor.saved_services', Saved],
        ['editor.popular_services', Popular],
      ];
      for (const [label, items] of groups) {
        if (!items?.length) continue;
        const choices = items.map(({ ID, Name }) =>
          `<button type="button" class="secondary" data-service="${ID}" aria-pressed="false">${escape(Name)}</button>`);
        picksList.insertAdjacentHTML('beforeend', `<section><h3>${text(label)}</h3>
          <div class="actions">${choices.slice(0, 2).join('')}</div>
          ${choices.length > 2 ? `<details class="more-options"><summary>${text('common.more')}</summary>
            <div class="actions">${choices.slice(2).join('')}</div></details>` : ''}
        </section>`);
        const group = picksList.lastElementChild;
        for (const item of items) group.querySelector(`[data-service="${item.ID}"]`).onclick = () => selectService(item);
      }
      picksStatus.hidden = picksList.childElementCount > 0;
      if (!picksStatus.hidden) picksStatus.textContent = t('editor.no_recommendations');
      markSelection();
    } catch (error) {
      if (id !== renderID || request !== picksRequest) return;
      picksStatus.textContent = errorText(error);
      retryPicks.hidden = false;
    }
  }
  if (retryPicks) retryPicks.onclick = () => void loadQuickPicks();
  fields.city.value = city.ID || 0;
  fields.city.onchange = () => {
    serviceSearch++;
    service = null;
    resetProviders();
    results.replaceChildren();
    markSelection();
    void loadQuickPicks();
  };
  function updateDates() {
    const range = fields.dateMode.value === 'range';
    form.querySelector('#range').hidden = !range;
    form.querySelector('#days').hidden = range;
    fields.from.required = range;
    fields.to.required = range;
    fields.days.disabled = range;
  }
  fields.dateMode.value = watch?.DateFrom ? 'range' : 'days';
  fields.dateMode.onchange = updateDates;
  updateDates();
  markSelection();
  // Keep the summary current for every field change without extra state.
  form.addEventListener('input', summarize);
  form.addEventListener('change', summarize);
  if (focusPicks) (fields.city.value === '0' ? fields.city : quickPicks)?.focus();
  void loadQuickPicks();
  const search = form.querySelector('#search-services');
  async function findServices(append = false) {
    const cityID = Number(fields.city.value);
    if (!cityID) throw new DashboardError('error.city_first');
    if (!append) {
      serviceSearchOffset = 0;
      results.innerHTML = `<p class="muted">${text('editor.searching')}</p>`;
    }
    const seq = ++serviceSearch;
    let response;
    try {
      response = await api('procedures', { CityID: cityID, Query: fields.query.value.trim(), Offset: serviceSearchOffset });
    } catch (failure) {
      if (!append && seq === serviceSearch) results.replaceChildren();
      throw failure;
    }
    const { Items, More } = response;
    if (seq !== serviceSearch) return;
    results.querySelector('[data-more]')?.remove();
    const items = Items ?? [];
    serviceSearchOffset += items.length;
    const html = items.map(({ ID, Name }) => `<button type="button" class="secondary" data-service="${ID}">${escape(Name)}</button>`).join('');
    if (!append) results.replaceChildren();
    results.insertAdjacentHTML('beforeend', html || `<p class="muted">${text('editor.no_services')}</p>`);
    for (const item of items) {
      results.querySelector(`[data-service="${item.ID}"]`).onclick = () => selectService(item);
    }
    if (More) {
      results.insertAdjacentHTML('beforeend', `<button type="button" class="flat" data-more>${text('common.more')}</button>`);
      const next = results.lastElementChild;
      next.onclick = () => void run(next, () => findServices(true), error);
    }
  }
  fields.query.oninput = () => {
    serviceSearch++;
    results.replaceChildren();
  };
  fields.query.onkeydown = (event) => {
    if (event.key === 'Enter' && !event.isComposing) {
      event.preventDefault();
      search.click();
    }
  };
  search.onclick = () => void run(search, () => findServices(), error);
  providers.onclick = () => templateProviders();

  function templateProviders() {
    void run(providers, async () => {
      const selectedCity = fields.city.value;
      const selectedService = service.ID;
      const { Doctors, Facilities } = await api('providers', { CityID: Number(selectedCity), ServiceID: selectedService });
      if (selectedCity !== fields.city.value || selectedService !== service?.ID) return;
      providerOptions.innerHTML = `<label class="field">${text('editor.doctor')}<select name="doctor">
        ${option(0, t('editor.any_doctor'))}${(Doctors ?? []).map((person) => option(person.ID, [person.AcademicTitle, person.FirstName, person.LastName].filter(Boolean).join(' '))).join('')}
        </select></label>
        <div><p class="muted">${text('editor.facilities_hint')}</p>
        <div class="options">${(Facilities ?? []).map(({ ID, Name, Address }) => `<label class="option">
          <input type="checkbox" data-facility="${ID}" data-name="${escape(Name)}" ${facilities.has(ID) ? 'checked' : ''}><span>${escape(Name)}${Address ? `<br><span class="muted">${escape(Address)}</span>` : ''}</span></label>`).join('')}</div></div>`;
      fields.doctor.value = doctor;
      fields.doctor.onchange = () => { doctor = Number(fields.doctor.value); };
      for (const checkbox of providerOptions.querySelectorAll('[data-facility]')) {
        checkbox.onchange = () => {
          const facility = Number(checkbox.dataset.facility);
          if (checkbox.checked) facilities.add(facility);
          else facilities.delete(facility);
        };
      }
      summarize();
    }, error);
  }

  for (const row of form.querySelectorAll('[data-day]')) {
    const day = Number(row.dataset.day);
    const checkbox = row.querySelector('[type="checkbox"]');
    checkbox.onchange = () => {
      if (!checkbox.checked) return;
      for (const other of form.querySelectorAll('[data-day]')) {
        if (other !== row && (day === 0 || Number(other.dataset.day) === 0)) other.querySelector('[type="checkbox"]').checked = false;
      }
    };
  }
  form.onsubmit = (event) => {
    event.preventDefault();
    void run(form.querySelector('[type="submit"]'), async () => {
      if (!service) throw new DashboardError('editor.choose_service');
      const windows = selectedHours();
      const range = fields.dateMode.value === 'range';
      await api('watch', { ID: watch?.ID ?? 0, CityID: Number(fields.city.value), ServiceID: service.ID, DoctorID: doctor,
        FacilityIDs: [...facilities], DateFrom: range ? fields.from.value : '', DateTo: range ? fields.to.value : '',
        NextDays: range ? 0 : Number(fields.days.value), TimeWindows: windows, CheckMinutes: Number(fields.minutes.value) });
      await update('watches');
      flash(t('watches.saved'));
    }, error);
  };
  form.querySelector('#cancel').onclick = () => void switchTab(tab);
  const remove = form.querySelector('#delete');
  if (remove) remove.onclick = () => {
    if (!window.confirm(t('editor.confirm_delete'))) return;
    void run(remove, async () => {
      await api('status', { ID: watch.ID, Status: 'deleted' });
      await update('watches');
      flash(t('watches.deleted'));
    }, error);
  };
}

function screenError(title, error, retry) {
  show(title, `<div class="card stack"><p class="error">${escape(errorText(error))}</p>${retry ? `<button type="button" class="secondary">${text('common.retry') || 'Spróbuj ponownie'}</button>` : ''}</div>`);
  if (retry) main.querySelector('button').onclick = retry;
}

async function switchTab(next) {
  clearTimeout(flashTimer);
  announcement.textContent = '';
  tab = next;
  const id = ++renderID;
  for (const button of navigation.querySelectorAll('button')) {
    if (button.dataset.tab === tab) button.setAttribute('aria-current', 'page');
    else button.removeAttribute('aria-current');
  }
  try {
    if (tab === 'home') home();
    if (tab === 'watches') watches();
    if (tab === 'history') await historyScreen(id);
    if (tab === 'settings') await settingsScreen(id);
  } catch (error) {
    if (id === renderID) screenError(titleFor(next), error, () => switchTab(next));
  }
}

async function update(next) {
  state = await api('load');
  await loadLanguage(state.Locale);
  renderLanguage();
  navigation.hidden = false;
  await switchTab(next);
}

async function start() {
  try {
    await loadLanguage(initialLanguage(telegram));
  } catch {
    screenError('Luxmed Watcher', new DashboardError('error.unavailable', 'Nie udało się wczytać aplikacji. Spróbuj ponownie.'), start);
    return;
  }
  renderLanguage();
  if (!telegram?.initData) {
    screenError('Luxmed Watcher', new DashboardError('error.open_from_bot'));
    return;
  }
  show(titleFor('home'), `<p>${text('dashboard.loading')}</p>`, false);
  const initial = new URLSearchParams(location.search).get('tab');
  try {
    ({ token: apiToken } = await api('auth'));
    await update(tabs.includes(initial) ? initial : 'home');
  } catch (error) {
    screenError('Luxmed Watcher', error, error?.key === 'error.access_denied' ? undefined : start);
  }
}

telegram?.ready();
telegram?.expand();
telegram?.enableVerticalSwipes?.();

main.addEventListener('click', (event) => {
  const link = event.target.closest('a');
  if (![portal, referralsPortal].includes(link?.href) || !telegram?.openLink) return;
  event.preventDefault();
  telegram.openLink(link.href);
});

navigation.onclick = (event) => {
  const button = event.target.closest('[data-tab]');
  if (button) void switchTab(button.dataset.tab);
};
void start();
