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
const shortDate = new Intl.DateTimeFormat('ru', { timeZone: 'Europe/Warsaw', day: 'numeric', month: 'short' });
const clock = new Intl.DateTimeFormat('ru', { timeZone: 'Europe/Warsaw', hour: '2-digit', minute: '2-digit' });
const dateOnly = (date) => date ? localDate.format(new Date(date)) : '';
const dateText = (date) => shortDate.format(new Date(date));
const weekdays = ['Каждый день', 'Пн', 'Вт', 'Ср', 'Чт', 'Пт', 'Сб', 'Вс'];
const titles = { watches: 'Поиски', history: 'Найденное', settings: 'Настройки' };
let state;
let cities;
let tab = 'watches';
let account = 'unchecked';
let flashTimer;
let renderID = 0;

for (const target of document.querySelectorAll('[data-icon]')) target.innerHTML = icon(target.dataset.icon);

function flash(message) {
  clearTimeout(flashTimer);
  announcement.textContent = message;
  flashTimer = setTimeout(() => { announcement.textContent = ''; }, 4000);
}

async function api(action, data = {}) {
  const response = await fetch(`/api/dashboard/${action}`, {
    method: 'POST', redirect: 'error', cache: 'no-store',
    headers: { 'Content-Type': 'application/json', 'X-Telegram-Init-Data': telegram.initData },
    body: JSON.stringify(data), signal: AbortSignal.timeout(19_000),
  });
  if (response.status === 204) return;
  if (response.ok) return response.json();
  if (response.status === 401 || response.status === 403) throw new Error('Откройте приложение заново из личного чата с ботом.');
  if (response.status === 409) throw new Error('Войдите в LuxMed через настройки, затем повторите.');
  if (response.status === 400) throw new Error('Проверьте заполненные поля.');
  if (response.status === 404) throw new Error('Данные изменились. Откройте экран заново.');
  throw new Error('Сервис временно недоступен. Попробуйте ещё раз.');
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
  const labels = { unchecked: 'Аккаунт LuxMed', connected: 'LuxMed подключён', needs_login: 'Войдите в LuxMed' };
  return `<div class="notice ${account}">${icon(account === 'connected' ? 'check' : 'time')}${labels[account]}</div>`;
}

function watchCard(watch) {
  const active = watch.Status === 'active';
  const dates = watch.DateFrom && watch.DateTo
    ? `${dateText(watch.DateFrom)} — ${dateText(watch.DateTo)}`
    : `Период поиска: ${watch.NextDays} дн.`;
  const lastRun = watch.LastCheckedAt ? `${dateText(watch.LastCheckedAt)}, ${clock.format(new Date(watch.LastCheckedAt))}` : '';
  return `<article class="card">
    <div class="card-head"><h2><button type="button" class="watch-open" data-edit="${watch.ID}">${escape(watch.ServiceName || watch.Name)}</button></h2>
      <span class="badge ${active ? 'active' : ''}">${active ? 'Активен' : 'На паузе'}</span></div>
    <p class="meta">${icon('pin')}${escape(watch.CityName)}</p>
    <p class="meta">${icon('calendar')}${dates}</p>
    ${watch.DoctorName ? `<p class="meta">${escape(watch.DoctorName)}</p>` : ''}
    ${lastRun ? `<p class="meta">${watch.LastError ? 'Ошибка проверки' : 'Проверено'} · ${lastRun}</p>` : ''}
    <div class="actions"><button type="button" class="secondary" data-watch="${watch.ID}" data-status="${active ? 'paused' : 'active'}">${active ? 'Приостановить' : 'Возобновить'}</button></div>
  </article>`;
}

function watches() {
  const items = state.Watches ?? [];
  show(titles.watches, `<div class="stack">${accountNotice()}
    <button type="button" class="primary" id="new-watch">${icon('plus')}Новый поиск</button>
    ${items.length ? items.map(watchCard).join('') : '<div class="card empty"><h2>Поисков пока нет</h2><p>Выберите услугу и даты. Бот пришлёт сообщение, когда найдёт слоты.</p></div>'}
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
    <div class="date-tile"><strong>${part('day')}</strong>${escape(part('month'))}</div>
    <div>
      <h2>${clock.format(date)}</h2>
      <h3>${escape(slot.Service || 'Приём')}</h3>
      ${[slot.Doctor, slot.Facility, slot.Address].filter(Boolean).map((line) => `<p class="meta">${escape(line)}</p>`).join('')}
      ${expired ? '<p class="muted">Слот уже недоступен</p>' : ''}
    </div>
    ${expired ? '' : `<a class="flat" href="${portal}" target="_blank" rel="noopener">Открыть LuxMed ${icon('link')}</a>`}
  </article>`;
}

async function historyScreen(id) {
  show(titles.history, '<div class="stack" id="slots"><p class="muted">Загружаем историю…</p></div>');
  const results = main.querySelector('#slots');
  async function append(offset) {
    const { Slots, More } = await api('history', { Offset: offset });
    if (id !== renderID) return;
    results.querySelector('button')?.remove();
    const cards = Slots.map(slotCard).join('');
    if (!offset) results.replaceChildren();
    results.insertAdjacentHTML('beforeend', cards || '<div class="card empty"><h2>Найденных слотов пока нет</h2><p>Здесь появятся результаты ваших поисков.</p></div>');
    if (More) {
      results.insertAdjacentHTML('beforeend', '<button class="secondary" type="button">Показать ещё</button>');
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
  show(titles.settings, `<div class="stack">
    <section class="card stack">
      <h2>Аккаунт LuxMed</h2>
      ${accountNotice()}
      <button type="button" class="primary" id="connect">Войти в LuxMed ${icon('link')}</button>
      <button type="button" class="secondary" id="account-check">Проверить подключение</button>
    </section>
    <form class="stack">
      <section class="card stack">
        <h2>Предпочтения</h2>
        <label class="field">Город по умолчанию<select name="city"></select></label>
        <button type="button" class="secondary" id="load-cities" hidden>Загрузить города</button>
        <label class="field">Язык бота и уведомлений<select name="locale">
          <option value="ru">Русский</option><option value="pl">Polski</option><option value="en">English</option>
        </select></label>
      </section>
      <p class="muted error" data-error role="status"></p>
      <button type="submit" class="primary">Сохранить</button>
    </form></div>`);
  const form = main.querySelector('form');
  const error = form.querySelector('[data-error]');
  const loadCities = main.querySelector('#load-cities');
  const selected = { ID: state.CityID, Name: state.CityName };
  function fillCities(items) {
    form.elements.city.innerHTML = cityOptions(items, selected, 'Не выбран');
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
  form.elements.locale.value = state.Locale;
  form.onsubmit = (event) => {
    event.preventDefault();
    void run(form.querySelector('[type="submit"]'), async () => {
      await api('settings', { Locale: form.elements.locale.value, CityID: Number(form.elements.city.value) });
      await update('settings');
      flash('Настройки сохранены');
    }, error);
  };
  const connect = main.querySelector('#connect');
  connect.onclick = () => void run(connect, async () => {
    const { url } = await api('connect');
    const target = new URL(url);
    if (target.origin !== location.origin || target.pathname !== '/') throw new Error('Ссылка на вход недоступна.');
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
  const title = watch ? 'Настроить поиск' : 'Новый поиск';
  show(title, '<p class="muted">Загружаем города…</p>');
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
    <fieldset class="card stack"><legend>Что искать</legend>
      <label class="field">Город<select name="city" required>${cityOptions(options, city, 'Выберите город')}</select></label>
      <label class="field">Услуга<input type="search" name="query" placeholder="Например, Dermatolog" maxlength="80" autocomplete="off"></label>
      <button type="button" class="secondary" id="search-services">Найти услугу</button>
      <div class="options" id="services"></div>
      <p class="meta" id="selected-service"></p>
      <button type="button" class="secondary" id="providers">Выбрать врача и клиники</button>
      <div class="stack" id="provider-options"></div>
    </fieldset>
    <fieldset class="card stack"><legend>Когда искать</legend>
      <label class="field">Период<select name="dateMode"><option value="days">Ближайшие дни</option><option value="range">Конкретные даты</option></select></label>
      <label class="field" id="days">Дней вперёд<input name="days" type="number" min="1" max="90" value="${watch?.NextDays || 14}"></label>
      <div class="columns" id="range" hidden>
        <label class="field">С<input name="from" type="date" value="${dateOnly(watch?.DateFrom)}"></label>
        <label class="field">По<input name="to" type="date" value="${dateOnly(watch?.DateTo)}"></label>
      </div>
      <details><summary>Дни недели и часы</summary>
        <p class="muted">Выберите «Каждый день» или отдельные дни. Без выбора поиск идёт в любое время.</p>
        ${weekdays.map((day, index) => {
          const window = existingHours.get(index);
          return `<div class="time-row" data-day="${index}">
            <label class="option"><input type="checkbox" ${window ? 'checked' : ''}>${day}</label>
            <input type="time" aria-label="${day}, с" value="${escape(window?.from || '00:00')}">
            <input type="time" aria-label="${day}, по" value="${escape(window?.to || '23:59')}">
          </div>`;
        }).join('')}
      </details>
      <label class="field">Проверять каждые, мин<input name="minutes" type="number" required min="${state.MinimumMinutes}" max="1440" value="${watch ? Math.ceil(watch.CheckIntervalSeconds / 60) : state.DefaultMinutes}"></label>
    </fieldset>
    <p class="muted error" data-error role="status"></p>
    <button type="submit" class="primary">Сохранить поиск</button>
    <button type="button" class="secondary" id="cancel">Отмена</button>
    ${watch ? '<button type="button" class="flat danger" id="delete">Удалить поиск</button>' : ''}
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
    picked.textContent = service ? `Выбрано: ${service.Name}` : 'Выберите услугу из списка.';
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
    if (!cityID) throw new Error('Сначала выберите город.');
    if (!append) serviceSearchOffset = 0;
    const seq = ++serviceSearch;
    const { Items, More } = await api('procedures', { CityID: cityID, Query: fields.query.value.trim(), Offset: serviceSearchOffset });
    if (seq !== serviceSearch) return;
    results.querySelector('[data-more]')?.remove();
    const items = Items ?? [];
    serviceSearchOffset += items.length;
    const html = items.map(({ ID, Name }) => `<button type="button" class="secondary" data-service="${ID}">${escape(Name)}</button>`).join('');
    if (!append) results.replaceChildren();
    results.insertAdjacentHTML('beforeend', html || '<p class="muted">Услуг с таким названием нет.</p>');
    for (const item of items) {
      results.querySelector(`[data-service="${item.ID}"]`).onclick = () => {
        if (service?.ID !== item.ID) resetProviders();
        service = item;
        if (providerOptions.querySelector('select')) picked.textContent = `Выбрано: ${service.Name}`;
        else markSelection();
        results.replaceChildren();
      };
    }
    if (More) {
      results.insertAdjacentHTML('beforeend', '<button type="button" class="flat" data-more>Показать ещё</button>');
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
      providerOptions.innerHTML = `<label class="field">Врач<select name="doctor">
        ${option(0, 'Любой')}${(Doctors ?? []).map((person) => option(person.ID, [person.AcademicTitle, person.FirstName, person.LastName].filter(Boolean).join(' '))).join('')}
        </select></label>
        <div><p class="muted">Клиники. Без выбора поиск идёт во всех.</p>
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
      if (!service) throw new Error('Выберите услугу из списка.');
      const windows = [...form.querySelectorAll('[data-day]')].filter((row) => row.querySelector('[type="checkbox"]').checked).map((row) => {
        const [from, to] = row.querySelectorAll('input[type="time"]');
        return { weekday: Number(row.dataset.day), from: from.value, to: to.value };
      });
      const range = fields.dateMode.value === 'range';
      await api('watch', { ID: watch?.ID ?? 0, CityID: Number(fields.city.value), ServiceID: service.ID, DoctorID: doctor,
        FacilityIDs: [...facilities], DateFrom: range ? fields.from.value : '', DateTo: range ? fields.to.value : '',
        NextDays: range ? 0 : Number(fields.days.value), TimeWindows: windows, CheckMinutes: Number(fields.minutes.value) });
      await update('watches');
      flash('Поиск сохранён');
    }, error);
  };
  form.querySelector('#cancel').onclick = () => void switchTab('watches');
  const remove = form.querySelector('#delete');
  if (remove) remove.onclick = () => {
    if (!window.confirm('Удалить этот поиск?')) return;
    void run(remove, async () => {
      await api('status', { ID: watch.ID, Status: 'deleted' });
      await update('watches');
      flash('Поиск удалён');
    }, error);
  };
}

function screenError(title, error, retry) {
  show(title, `<div class="card stack"><p class="error">${escape(error.message)}</p>${retry ? '<button type="button" class="secondary">Повторить</button>' : ''}</div>`);
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
    if (id === renderID) screenError(titles[next], error, () => switchTab(next));
  }
}

async function update(next) {
  state = await api('load');
  navigation.hidden = false;
  await switchTab(next);
}

function start() {
  if (!telegram?.initData) {
    screenError('Luxmed Watcher', new Error('Откройте приложение командой /app в личном чате с ботом.'));
    return;
  }
  telegram.ready();
  telegram.expand();
  telegram.enableVerticalSwipes?.();
  const initial = new URLSearchParams(location.search).get('tab');
  update(Object.hasOwn(titles, initial) ? initial : 'watches')
    .catch((error) => screenError('Luxmed Watcher', error, start));
}

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
start();
