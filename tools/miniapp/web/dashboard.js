import { initialLanguage, language, languages, loadLanguage, t } from '/language.js';

const telegram = window.Telegram?.WebApp;
const main = document.querySelector('#main');
const navigation = document.querySelector('#navigation');
const announcement = document.querySelector('#announcement');
const portal = 'https://portalpacjenta.luxmed.pl/PatientPortal/NewPortal/Page/Reservation';
const icons = { calendar: '▦', history: '▤', settings: '⚙', plus: '+', pin: '⌖', time: '◷', check: '✓', link: '↗' };
const icon = (name) => `<span class="icon" aria-hidden="true">${icons[name]}</span>`;
const escape = (value) => String(value ?? '').replace(/[&<>"']/g, (char) => ({
  '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;',
})[char]);
const localDate = new Intl.DateTimeFormat('sv-SE', { timeZone: 'Europe/Warsaw' });
const dateOnly = (date) => date ? localDate.format(new Date(date)) : '';
const dateText = (date) => shortDate.format(new Date(date));
const text = (key, values) => escape(t(key, values));
const tabs = ['watches', 'history', 'settings'];
const titleFor = (tab) => t(`tabs.${tab}`);
let shortDate;
let clock;
let weekdays;
let state;
let apiToken;
let cities;
let tab = 'watches';
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
    throw new Error(t('error.unavailable'));
  }
  if (response.status === 401) throw new Error(t('error.reopen'));
  if (response.status === 403) throw new Error(t('error.access_denied'));
  if (response.status === 429) throw new Error(t('error.browser_busy'));
  if (response.status === 409) throw new Error(t('error.connect_first'));
  if (response.status === 400) throw new Error(t('error.invalid_fields'));
  if (response.status === 404) throw new Error(t('error.stale'));
  throw new Error(t('error.unavailable'));
}

async function run(button, action, errorTarget) {
  button.disabled = true;
  if (errorTarget) errorTarget.textContent = '';
  try {
    await action();
  } catch (error) {
    if (errorTarget) errorTarget.textContent = error.message;
    else flash(error.message);
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

function accountNotice() {
  return `<div class="notice ${account}">${icon(account === 'connected' ? 'check' : 'time')}${text(`account.${account}`)}</div>`;
}

function watchCard(watch) {
  const active = watch.Status === 'active';
  const dates = watch.DateFrom && watch.DateTo
    ? `${dateText(watch.DateFrom)} — ${dateText(watch.DateTo)}`
    : text('watches.period_days', { days: watch.NextDays });
  const lastRun = watch.LastCheckedAt ? `${dateText(watch.LastCheckedAt)}, ${clock.format(new Date(watch.LastCheckedAt))}` : '';
  return `<article class="card">
    <div class="card-head"><h2><button type="button" class="watch-open" data-edit="${watch.ID}">${escape(watch.ServiceName || watch.Name)}</button></h2>
      <span class="badge ${active ? 'active' : ''}">${text(active ? 'watches.active' : 'watches.paused')}</span></div>
    <p class="meta">${icon('pin')}${escape(watch.CityName)}</p>
    <p class="meta">${icon('calendar')}${dates}</p>
    ${watch.DoctorName ? `<p class="meta">${escape(watch.DoctorName)}</p>` : ''}
    ${lastRun ? `<p class="meta">${text(watch.LastError ? 'watches.check_error' : 'watches.checked')} · ${lastRun}</p>` : ''}
    <div class="actions"><button type="button" class="secondary" data-watch="${watch.ID}" data-status="${active ? 'paused' : 'active'}">${text(active ? 'watches.pause' : 'watches.resume')}</button></div>
  </article>`;
}

function watches() {
  const items = state.Watches ?? [];
  show(titleFor('watches'), `<div class="stack">${accountNotice()}
    <button type="button" class="primary" id="new-watch">${icon('plus')}${text('watches.new')}</button>
    ${items.length ? items.map(watchCard).join('') : `<div class="card empty"><h2>${text('watches.empty')}</h2><p>${text('watches.empty_hint')}</p></div>`}
  </div>`);
  main.querySelector('#new-watch').onclick = () => void editor();
  for (const button of main.querySelectorAll('[data-edit]')) {
    button.onclick = () => void editor(items.find((item) => item.ID === Number(button.dataset.edit)));
  }
  for (const button of main.querySelectorAll('[data-watch]')) {
    button.onclick = () => void run(button, async () => {
      await api('status', { ID: Number(button.dataset.watch), Status: button.dataset.status });
      await update('watches');
    });
  }
}

function slotCard(slot) {
  const date = new Date(slot.DateTime);
  const expired = slot.Status === 'expired' || date < new Date();
  const parts = shortDate.formatToParts(date);
  const part = (name) => parts.find((item) => item.type === name)?.value;
  return `<article class="card slot">
    <div class="date-tile"><strong>${escape(part('day'))}</strong>${escape(part('month'))}</div>
    <div>
      <h2>${clock.format(date)}</h2>
      <h3>${escape(slot.Service || t('history.appointment'))}</h3>
      ${[slot.Doctor, slot.Facility, slot.Address].filter(Boolean).map((line) => `<p class="meta">${escape(line)}</p>`).join('')}
      ${expired ? `<p class="muted">${text('history.expired')}</p>` : ''}
    </div>
    ${expired ? '' : `<a class="flat" href="${portal}" target="_blank" rel="noopener">${text('history.open_portal')} ${icon('link')}</a>`}
  </article>`;
}

async function historyScreen(id) {
  show(titleFor('history'), `<div class="stack" id="slots"><p class="muted">${text('history.loading')}</p></div>`);
  const results = main.querySelector('#slots');
  async function append(offset) {
    const { Slots, More } = await api('history', { Offset: offset });
    if (id !== renderID) return;
    results.querySelector('button')?.remove();
    const cards = Slots.map(slotCard).join('');
    if (!offset) results.replaceChildren();
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
    <section class="card stack">
      <h2>${text('account.unchecked')}</h2>
      ${accountNotice()}
      <button type="button" class="primary" id="connect">${text('account.needs_login')} ${icon('link')}</button>
      <button type="button" class="secondary" id="account-check">${text('settings.check_account')}</button>
    </section>
    <form class="stack">
      <section class="card stack">
        <h2>${text('settings.preferences')}</h2>
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
  const connect = main.querySelector('#connect');
  connect.onclick = () => void run(connect, async () => {
    const { url } = await api('connect');
    const target = new URL(url);
    if (target.origin !== location.origin || target.pathname !== '/') throw new Error(t('error.link'));
    target.searchParams.set('return', 'app');
    target.hash = location.hash;
    location.assign(target.href);
  });
  const check = main.querySelector('#account-check');
  check.onclick = () => void run(check, async () => {
    account = (await api('account')).state;
    if (id === renderID) {
      const notice = main.querySelector('.notice');
      notice.outerHTML = accountNotice();
    }
  });
}

async function editor(watch) {
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
  show(title, `<form class="stack">
    <fieldset class="card stack"><legend>${text('editor.what')}</legend>
      <label class="field">${text('editor.city')}<select name="city" required>${cityOptions(options, city, t('editor.choose_city'))}</select></label>
      <label class="field">${text('editor.service')}<input type="search" name="query" placeholder="${text('editor.service_placeholder')}" maxlength="80" autocomplete="off"></label>
      <button type="button" class="secondary" id="search-services">${text('editor.find_service')}</button>
      <div class="options" id="services"></div>
      <p class="meta" id="selected-service"></p>
      <button type="button" class="secondary" id="providers">${text('editor.choose_providers')}</button>
      <div class="stack" id="provider-options"></div>
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
      <label class="field">${text('editor.interval')}<input name="minutes" type="number" required min="${state.MinimumMinutes}" max="1440" value="${watch ? Math.ceil(watch.CheckIntervalSeconds / 60) : state.DefaultMinutes}"></label>
    </fieldset>
    <p class="muted error" data-error role="status"></p>
    <button type="submit" class="primary">${text('editor.save')}</button>
    <button type="button" class="secondary" id="cancel">${text('common.cancel')}</button>
    ${watch ? `<button type="button" class="flat danger" id="delete">${text('editor.delete')}</button>` : ''}
  </form>`);
  const form = main.querySelector('form');
  const fields = form.elements;
  const results = form.querySelector('#services');
  const picked = form.querySelector('#selected-service');
  const providers = form.querySelector('#providers');
  const providerOptions = form.querySelector('#provider-options');
  const error = form.querySelector('[data-error]');
  let serviceSearch = 0;
  let serviceSearchOffset = 0;

  function markSelection() {
    picked.textContent = service ? t('editor.selected_service', { service: service.Name }) : t('editor.choose_service');
    providers.disabled = !service;
    if (service && (doctor || facilities.size)) {
      providerOptions.textContent = [watch?.DoctorName, ...(watch?.FacilityNames ?? [])].filter(Boolean).join(' · ');
    }
  }
  function resetProviders() {
    doctor = 0;
    facilities = new Set();
    providerOptions.replaceChildren();
  }
  fields.city.value = city.ID || 0;
  fields.city.onchange = () => {
    serviceSearch++;
    service = null;
    resetProviders();
    results.replaceChildren();
    markSelection();
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
  const search = form.querySelector('#search-services');
  async function findServices(append = false) {
    const cityID = Number(fields.city.value);
    if (!cityID) throw new Error(t('error.city_first'));
    if (!append) serviceSearchOffset = 0;
    const seq = ++serviceSearch;
    const { Items, More } = await api('procedures', { CityID: cityID, Query: fields.query.value.trim(), Offset: serviceSearchOffset });
    if (seq !== serviceSearch) return;
    results.querySelector('[data-more]')?.remove();
    const items = Items ?? [];
    serviceSearchOffset += items.length;
    const html = items.map(({ ID, Name }) => `<button type="button" class="secondary" data-service="${ID}">${escape(Name)}</button>`).join('');
    if (!append) results.replaceChildren();
    results.insertAdjacentHTML('beforeend', html || `<p class="muted">${text('editor.no_services')}</p>`);
    for (const item of items) {
      results.querySelector(`[data-service="${item.ID}"]`).onclick = () => {
        if (service?.ID !== item.ID) resetProviders();
        service = item;
        if (providerOptions.querySelector('select')) picked.textContent = t('editor.selected_service', { service: service.Name });
        else markSelection();
        results.replaceChildren();
      };
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
          <input type="checkbox" data-facility="${ID}" ${facilities.has(ID) ? 'checked' : ''}><span>${escape(Name)}${Address ? `<br><span class="muted">${escape(Address)}</span>` : ''}</span></label>`).join('')}</div></div>`;
      fields.doctor.value = doctor;
      fields.doctor.onchange = () => { doctor = Number(fields.doctor.value); };
      for (const checkbox of providerOptions.querySelectorAll('[data-facility]')) {
        checkbox.onchange = () => {
          const facility = Number(checkbox.dataset.facility);
          if (checkbox.checked) facilities.add(facility);
          else facilities.delete(facility);
        };
      }
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
      if (!service) throw new Error(t('editor.choose_service'));
      const windows = [...form.querySelectorAll('[data-day]')].filter((row) => row.querySelector('[type="checkbox"]').checked).map((row) => {
        const [from, to] = row.querySelectorAll('input[type="time"]');
        return { weekday: Number(row.dataset.day), from: from.value, to: to.value };
      });
      const range = fields.dateMode.value === 'range';
      await api('watch', { ID: watch?.ID ?? 0, CityID: Number(fields.city.value), ServiceID: service.ID, DoctorID: doctor,
        FacilityIDs: [...facilities], DateFrom: range ? fields.from.value : '', DateTo: range ? fields.to.value : '',
        NextDays: range ? 0 : Number(fields.days.value), TimeWindows: windows, CheckMinutes: Number(fields.minutes.value) });
      await update('watches');
      flash(t('watches.saved'));
    }, error);
  };
  form.querySelector('#cancel').onclick = () => void switchTab('watches');
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
  show(title, `<div class="card stack"><p class="error">${escape(error.message)}</p>${retry ? `<button type="button" class="secondary">${text('common.retry') || 'Spróbuj ponownie'}</button>` : ''}</div>`);
  if (retry) main.querySelector('button').onclick = retry;
}

async function switchTab(next) {
  tab = next;
  const id = ++renderID;
  for (const button of navigation.querySelectorAll('button')) {
    if (button.dataset.tab === tab) button.setAttribute('aria-current', 'page');
    else button.removeAttribute('aria-current');
  }
  try {
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
    screenError('Luxmed Watcher', new Error('Nie udało się wczytać aplikacji.'), start);
    return;
  }
  renderLanguage();
  if (!telegram?.initData) {
    screenError('Luxmed Watcher', new Error(t('error.open_from_bot')));
    return;
  }
  show(titleFor('watches'), `<p>${text('dashboard.loading')}</p>`, false);
  const initial = new URLSearchParams(location.search).get('tab');
  try {
    ({ token: apiToken } = await api('auth'));
    await update(tabs.includes(initial) ? initial : 'watches');
  } catch (error) {
    screenError('Luxmed Watcher', error, error.message === t('error.access_denied') ? undefined : start);
  }
}

telegram?.ready();
telegram?.expand();
telegram?.enableVerticalSwipes?.();

main.addEventListener('click', (event) => {
  const link = event.target.closest('a');
  if (link?.href !== portal || !telegram?.openLink) return;
  event.preventDefault();
  telegram.openLink(portal);
});

navigation.onclick = (event) => {
  const button = event.target.closest('[data-tab]');
  if (button) void switchTab(button.dataset.tab);
};
void start();
