import { initialLanguage, language, languages, setLanguage, t } from '/language.js';

const telegram = window.Telegram?.WebApp;
const main = document.querySelector('#main');
const navigation = document.querySelector('#navigation');
const announcement = document.querySelector('#announcement');
const portal = 'https://portalpacjenta.luxmed.pl/PatientPortal/NewPortal/Page/Reservation';
const referralsPortal = 'https://portalpacjenta.luxmed.pl/PatientPortal/NewPortal/Page/Referrals';
const icons = {
  user: '<circle cx="12" cy="8.5" r="3.75"/><path d="M4.75 19.5c1.2-3.3 4-5 7.25-5s6.05 1.7 7.25 5"/>',
  plus: '<path d="M12 5v14M5 12h14"/>',
  link: '<path d="M7.5 16.5l9-9M9.5 7.5h7v7"/>',
  back: '<path d="M14.5 6 8.5 12l6 6"/>',
};
const icon = (name) => `<span class="icon" aria-hidden="true"><svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.75" stroke-linecap="round" stroke-linejoin="round">${icons[name]}</svg></span>`;
const escape = (value) => String(value ?? '').replace(/[&<>"']/g, (char) => ({
  '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;',
})[char]);
const timeZone = 'Europe/Warsaw';
const localDate = new Intl.DateTimeFormat('en', { timeZone, year: 'numeric', month: '2-digit', day: '2-digit' });
const clock = new Intl.DateTimeFormat('en', { timeZone, hour: '2-digit', minute: '2-digit', hourCycle: 'h23' });

function dateParts(value) {
  const parts = localDate.formatToParts(new Date(value));
  const part = (type) => parts.find((item) => item.type === type).value;
  return { year: part('year'), month: part('month'), day: part('day') };
}

function dateOnly(value) {
  if (!value) return '';
  const { year, month, day } = dateParts(value);
  return `${year}-${month}-${day}`;
}

function shortDate(value) {
  const { month, day } = dateParts(value);
  return { day: Number(day), month: months[Number(month) - 1] };
}

const dateText = (value) => t('calendar.short_date', shortDate(value));
const text = (key, values) => escape(t(key, values));

class DashboardError extends Error {
  constructor(key) {
    super(t(key));
    this.key = key;
  }
}

function errorText(error) {
  return error instanceof DashboardError ? error.message : t('error.unavailable');
}
const tabs = ['history', 'watches', 'settings'];
const titleFor = (tab) => t(`tabs.${tab}`);
let months;
let weekdays;
let state;
let apiToken;
let cities;
let tab = 'history';
let flashTimer;
let renderID = 0;
let passwordSave = null;
let mountedAccount;

for (const target of document.querySelectorAll('[data-icon]')) target.innerHTML = icon(target.dataset.icon);

function renderLanguage() {
  months = t('calendar.months').split(',');
  weekdays = [t('editor.every_day'), ...t('calendar.weekdays').split(',')];
  navigation.setAttribute('aria-label', t('dashboard.navigation'));
  for (const button of navigation.querySelectorAll('.switch button')) {
    button.firstElementChild.textContent = titleFor(button.dataset.tab);
  }
  const me = navigation.querySelector('.me');
  me.setAttribute('aria-label', titleFor('settings'));
  me.title = titleFor('settings');
  // Initials from Telegram make the account button yours; the drawn person stays as the fallback.
  const user = telegram?.initDataUnsafe?.user;
  const initials = [user?.first_name, user?.last_name].map((name) => name?.trim()?.[0] ?? '').join('').toLocaleUpperCase(language);
  if (initials) me.innerHTML = `<span class="initials" aria-hidden="true">${escape(initials)}</span>`;
}

// The switch shows how much each side holds on every screen: open times found, searches saved.
function markTab(name) {
  for (const button of navigation.querySelectorAll('[data-tab]')) {
    if (button.dataset.tab === name) button.setAttribute('aria-current', 'page');
    else button.removeAttribute('aria-current');
  }
}
function setCount(name, value) {
  const target = navigation.querySelector(`[data-count="${name}"]`);
  target.textContent = value ? String(value) : '';
}

function flash(message) {
  clearTimeout(flashTimer);
  announcement.textContent = message;
  flashTimer = setTimeout(() => { announcement.textContent = ''; }, 4000);
}

async function api(action, data = {}, attemptID = '') {
  let response;
  const headers = { 'Content-Type': 'application/json' };
  if (action === 'auth' || action === 'connect') headers['X-Telegram-Init-Data'] = telegram.initData;
  if (apiToken) headers.Authorization = `Bearer ${apiToken}`;
  if (attemptID) headers['X-LuxMed-Login-Attempt'] = attemptID;
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
  if (response.status === 409 && action === 'credentials') throw new DashboardError('account.login_failed');
  if (response.status === 409) throw new DashboardError('error.connect_first');
  if (response.status === 400) throw new DashboardError('error.invalid_fields');
  if (response.status === 404 || response.status === 412) throw new DashboardError('error.stale');
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

// Found and Searches are named by the switch above them, so their heading is for screen readers only.
function show(title, html, focus = true) {
  const named = tab === 'history' || tab === 'watches';
  main.innerHTML = `<h1 class="${named ? 'sr-only' : 'title'}">${escape(title)}</h1>${html}`;
  if (focus) {
    window.scrollTo(0, 0);
    main.focus({ preventScroll: true });
  }
}

// The portal connection is only checked when the user asks (never automatically).
// The pill names a state; the button below it carries the command.
const accountStatusKey = (status) => status === 'no_saved_searches' ? 'watches.empty'
  : ['needs_login', 'search_paused'].includes(status) ? 'watches.state_login' : `account.${status}`;

// One main action per state: sign in when LUX MED needs it, otherwise check the connection.
const needsSignIn = (account) => ['needs_login', 'search_paused'].includes(account);
function accountSection() {
  const account = state.AccountStatus;
  return `<section class="card stack account" data-state="${account}" aria-labelledby="account-heading">
    <div class="account-head">
      <h2 id="account-heading">${text('account.title')}</h2>
      <p class="account-status" data-state="${account}" role="status">${text(accountStatusKey(account))}</p>
    </div>
    <div class="account-actions">
      ${state.SavedLogin ? '' : `<button type="button" class="${account === 'connected' ? 'flat' : 'secondary'}" id="connect"${account === 'connected' ? '' : ' aria-describedby="account-instructions"'}>${text(account === 'search_paused' ? 'account.connect_luxmed' : 'account.sign_in')} ${icon('link')}</button>`}
      ${state.HasCredentials ? `<button type="button" class="flat danger" id="forget-credentials">${text(state.SavedLogin ? 'account.forget' : 'account.delete_credentials')}</button>` : ''}
      <button type="button" class="${needsSignIn(account) ? 'flat' : 'secondary'}" id="account-check">${text('settings.check_account')}</button>
    </div>
    ${state.SavedLogin ? '' : `<p class="muted" id="account-instructions"${account === 'connected' ? ' hidden' : ''}>${text('account.instructions')}</p>`}
    <details${state.SavedLogin && needsSignIn(account) ? ' open' : ''}>
      <summary>${text(state.SavedLogin ? 'account.change_credentials' : 'account.password_mode')}</summary>
      <form id="credentials" method="post" class="stack">
        <label class="field">${text('account.login')}<input name="login" autocomplete="username" autocapitalize="none" spellcheck="false" maxlength="254" required></label>
        <label class="field">${text('account.password')}<input name="password" type="password" autocomplete="current-password" maxlength="1024" required></label>
        <p class="muted">${text('account.encryption')}</p>
        <button type="submit" class="primary">${text('account.save_credentials')}</button>
      </form>
    </details>
    <p class="muted error" data-account-error role="status"></p>
  </section>`;
}

async function signIn(revisions, attemptID, viewID) {
  const { url } = await api('connect', revisions, attemptID);
  let target;
  try {
    target = new URL(url);
  } catch {
    throw new DashboardError('error.link');
  }
  if (target.origin !== location.origin || target.pathname !== '/') throw new DashboardError('error.link');
  target.searchParams.set('return', 'app');
  target.hash = location.hash;
  if (viewID !== renderID) return false;
  location.assign(target.href);
  return true;
}

function accountModeChanged(latest, displayed) {
  return latest.SavedLogin !== displayed.SavedLogin ||
    latest.HasCredentials !== displayed.HasCredentials ||
    latest.CredentialRevision !== displayed.CredentialRevision ||
    latest.CredentialSaveRevision !== displayed.CredentialSaveRevision;
}

function refreshMountedAccount() {
  if (!mountedAccount || mountedAccount.viewID !== renderID || !mountedAccount.section.isConnected) return;
  const { section, displayed } = mountedAccount;
  if (accountModeChanged(state, displayed)) {
    section.outerHTML = accountSection();
    bindAccount();
    return;
  }
  const account = state.AccountStatus;
  const connect = section.querySelector('#connect');
  if (connect) {
    const instructions = section.querySelector('#account-instructions');
    instructions.hidden = account === 'connected';
    if (instructions.hidden) connect.removeAttribute('aria-describedby');
    else connect.setAttribute('aria-describedby', instructions.id);
    connect.firstChild.textContent = t(account === 'search_paused' ? 'account.connect_luxmed' : 'account.sign_in') + ' ';
    connect.className = account === 'connected' ? 'flat' : 'secondary';
  } else if (needsSignIn(account) && section.dataset.state !== account) {
    section.querySelector('details').open = true;
  }
  section.querySelector('#account-check').className = needsSignIn(account) ? 'flat' : 'secondary';
  section.dataset.state = account;
  const status = section.querySelector('.account-status');
  status.dataset.state = account;
  status.textContent = t(accountStatusKey(account));
}

function bindAccount() {
  const viewID = renderID;
  const section = main.querySelector('.account');
  const { SavedLogin, HasCredentials, CredentialRevision, CredentialSaveRevision } = state;
  const revisions = { CredentialRevision, CredentialSaveRevision };
  const displayed = { SavedLogin, HasCredentials, ...revisions };
  mountedAccount = { viewID, section, displayed };
  async function refreshAccount(error) {
    try {
      await showAccountSnapshot(await api('load'));
    } catch (refreshError) {
      if (!error && viewID === renderID) throw refreshError;
      flash(errorText(error ?? refreshError));
      return;
    }
    if (error) flash(errorText(error));
  }

  async function refreshStaleAction(error) {
    if (error.key !== 'error.stale') throw error;
    await refreshAccount(error);
  }

  function displayedAttempt() {
    if (passwordSave && passwordSave.revision === CredentialRevision && passwordSave.saveRevision === CredentialSaveRevision) {
      return passwordSave;
    }
    return null;
  }

  async function chooseChrome(action, attempt) {
    if (attempt) attempt.choosingChrome = true;
    try {
      await action(attempt?.id);
    } catch (error) {
      if (!['error.stale', 'error.unavailable', 'error.link'].includes(error.key)) throw error;
      await refreshAccount(error);
    } finally {
      if (attempt) {
        attempt.choosingChrome = false;
        if (!attempt.saving && passwordSave === attempt) passwordSave = null;
      }
    }
  }

  const failure = section.querySelector('[data-account-error]');
  const connect = section.querySelector('#connect');
  if (connect) connect.onclick = () => void run(connect, () => chooseChrome(async (id) => {
    if (!await signIn(revisions, id, viewID)) await refreshAccount();
  }, displayedAttempt()), failure);

  const form = section.querySelector('#credentials');
  form.onsubmit = (event) => {
    event.preventDefault();
    if (passwordSave?.saving || passwordSave?.choosingChrome) return;
    const attempt = {
      id: crypto.randomUUID(), revision: CredentialRevision, saveRevision: CredentialSaveRevision,
      saving: true, choosingChrome: false,
    };
    passwordSave = attempt;
    void run(form.querySelector('button'), async () => {
      try {
        try {
          await api('credentials', {
            Login: form.elements.login.value, Password: form.elements.password.value,
            CredentialRevision, CredentialSaveRevision,
          }, attempt.id);
        } finally {
          form.elements.password.value = '';
        }
        if (!attempt.choosingChrome) await refreshAccount();
      } catch (error) {
        if (!attempt.choosingChrome) await refreshStaleAction(error);
      } finally {
        attempt.saving = false;
        if (!attempt.choosingChrome && passwordSave === attempt) passwordSave = null;
      }
    }, failure);
  };
  const forget = section.querySelector('#forget-credentials');
  if (forget) forget.onclick = () => void run(forget, () => chooseChrome(async (id) => {
    await api('forget-credentials', revisions, id);
    await refreshAccount();
  }, SavedLogin ? displayedAttempt() : null), failure);
  const check = section.querySelector('#account-check');
  let refreshTimer;
  let manualCheck = 0;

  async function showAccountSnapshot(latest) {
    if (latest.CredentialRevision < state.CredentialRevision ||
        latest.CredentialSaveRevision < state.CredentialSaveRevision) return;
    if (viewID !== renderID) {
      const {
        SavedLogin, HasCredentials, CredentialRevision, CredentialSaveRevision,
        AccountStatus, ReconnectRequired, ReconnectEpisodeID,
      } = latest;
      Object.assign(state, {
        SavedLogin, HasCredentials, CredentialRevision, CredentialSaveRevision,
        AccountStatus, ReconnectRequired, ReconnectEpisodeID,
      });
      refreshMountedAccount();
      return;
    }
    applySnapshot(latest);
    if (tab === 'history' || tab === 'watches') {
      await switchTab(tab, { reload: false });
      return;
    }
    refreshMountedAccount();
  }

  function scheduleRefresh() {
    clearTimeout(refreshTimer);
    if (!check.isConnected || !['verification_pending', 'search_paused'].includes(state.AccountStatus)) return;
    refreshTimer = setTimeout(refreshAccountStatus, 5000);
  }

  async function refreshAccountStatus() {
    if (!check.isConnected) return;
    if (!document.hidden && !check.disabled) {
      const revision = manualCheck;
      try {
        const latest = await api('load');
        if (check.isConnected && revision === manualCheck &&
            (latest.AccountStatus !== state.AccountStatus || accountModeChanged(latest, displayed))) {
          await showAccountSnapshot(latest);
        }
      } catch (error) {
        if (check.isConnected && revision === manualCheck && ['error.reopen', 'error.access_denied'].includes(error?.key)) {
          failure.textContent = errorText(error);
          return;
        }
      }
    }
    scheduleRefresh();
  }

  check.onclick = () => void run(check, async () => {
    manualCheck++;
    clearTimeout(refreshTimer);
    try {
      await api('account');
      if (!check.isConnected) return;
      const latest = await api('load');
      if (check.isConnected) await showAccountSnapshot(latest);
    } finally {
      scheduleRefresh();
    }
  }, failure);
  scheduleRefresh();
}

function hoursLabel(windows) {
  return windows.map(window => `${weekdays[window.weekday]} ${window.from}–${window.to}`).join(' · ');
}

const watchErrors = new Map([
  ['luxmed_unavailable', 'error.luxmed_unavailable'],
  ['luxmed_partial', 'watches.check_partial'],
]);
const timeOf = (date) => `${dateText(date)}, ${clock.format(new Date(date))}`;

// One quiet line that answers "is it still watching?"; sign-in trouble becomes a banner with its fix.
function watchStatus() {
  const items = state.Watches ?? [];
  const now = new Date();
  const checks = items.map((watch) => Date.parse(watch.LastCheckedAt)).filter(Number.isFinite);
  const last = checks.length ? Math.max(...checks) : 0;
  const lastText = last && (dateOnly(last) === dateOnly(now) ? clock.format(last) : timeOf(last));
  const active = items.filter((watch) => watch.Status === 'active').length;
  const line = [items.length ? t('watches.active_count', { active, total: items.length }) : '', last ? `${t('watches.checked')} ${lastText}` : '']
    .filter(Boolean).join(' · ');
  const trouble = ['needs_login', 'search_paused'].includes(state.AccountStatus);
  // The dot only breathes when checks can actually run.
  const pulse = trouble ? ' trouble' : active ? '' : ' off';
  return `${line ? `<p class="status-line"><span class="pulse${pulse}" aria-hidden="true"></span>${escape(line)}</p>` : ''}
    ${trouble ? `<div class="alert" role="status"><p>${text(`account.${state.AccountStatus}`)}</p>
      <button type="button" class="primary" data-sign-in>${text('account.sign_in')}</button></div>` : ''}`;
}
const newWatchButton = () => `<div class="dock"><button type="button" class="primary" id="new-watch">${icon('plus')}${text('watches.new')}</button></div>`;
function bindShared() {
  main.querySelector('#new-watch')?.addEventListener('click', () => void editor());
  for (const button of main.querySelectorAll('[data-sign-in]')) button.onclick = () => void switchTab('settings');
}

// Monitoring state and the last successful check are separate facts: a search can be
// active and still be failing, and the user needs to see when it last worked.
function watchCard(watch) {
  const active = watch.Status === 'active';
  const dates = watch.DateFrom && watch.DateTo
    ? `${escape(dateText(watch.DateFrom))} — ${escape(dateText(watch.DateTo))}`
    : text('watches.period_days', { days: watch.NextDays });
  const minutes = Math.ceil(watch.CheckIntervalSeconds / 60);
  const account = state.AccountStatus;
  const accessError = ['luxmed_authentication', 'luxmed_account_missing'].includes(watch.LastError);
  const needsLogin = active && needsSignIn(account);
  let checkError = '';
  if (accessError) {
    if (needsLogin || account === 'verification_pending') checkError = `account.${account}`;
  } else if (watch.LastError) {
    checkError = watchErrors.get(watch.LastError) ?? 'watches.check_error';
  }
  const success = watch.LastSuccessAt ? timeOf(watch.LastSuccessAt) : t('watches.never_succeeded');
  const stateKey = !active ? 'paused' : needsLogin ? 'problem' : checkError ? 'degraded' : 'active';
  // The stub carries what you check first: is it watching, and how far ahead.
  const figure = watch.DateFrom ? Number(dateParts(watch.DateFrom).day) : watch.NextDays;
  const unit = watch.DateFrom ? shortDate(watch.DateFrom).month : t('watches.days_unit');
  return `<article class="pass watch" data-status="${active ? 'active' : 'paused'}">
    <div class="stub" data-state="${stateKey}">
      <small class="state">${text(!active ? 'watches.paused' : needsLogin ? 'watches.state_login' : 'watches.active')}</small>
      <b>${figure}</b><small>${escape(unit)}</small>
    </div>
    <div class="body">
      <h2 id="watch-${watch.ID}">${escape(watch.ServiceName || watch.Name)}</h2>
      <p class="meta">${escape(watch.CityName)}${watch.DoctorName ? ` · ${escape(watch.DoctorName)}` : ''}</p>
      <p class="meta">${[watch.DateFrom ? dates : '', watch.TimeWindows?.length ? escape(hoursLabel(watch.TimeWindows)) : '', minutes ? text('watches.interval_card', { minutes }) : ''].filter(Boolean).join(' · ')}</p>
      <p class="meta">${text('watches.last_success')}: <span class="value">${escape(success)}</span></p>
      ${state.Booking ? `<p class="meta autobook" data-on="${Boolean(watch.AutoBook)}">${text('watches.autobook')}: ${text(watch.AutoBook ? 'watches.autobook_on' : 'watches.autobook_off')}</p>` : ''}
      ${checkError && !needsLogin ? `<p class="notice" role="status">${text(checkError)}</p>` : ''}
      <div class="links">
        <button type="button" class="flat" data-edit="${watch.ID}" aria-describedby="watch-${watch.ID}">${text('watches.edit')}</button>
        <button type="button" class="flat" data-watch="${watch.ID}" data-status="${active ? 'paused' : 'active'}" aria-describedby="watch-${watch.ID}">${text(active ? 'watches.pause' : 'watches.resume')}</button>
        ${state.Booking ? `<button type="button" class="flat" data-autobook="${watch.ID}" aria-pressed="${Boolean(watch.AutoBook)}" aria-describedby="watch-${watch.ID}">${text(watch.AutoBook ? 'watches.autobook_disable' : 'watches.autobook_enable')}</button>` : ''}
      </div>
    </div>
  </article>`;
}

function setAutoBook(button, watch, enabled) {
  void run(button, async () => {
    await api('autobook', { ID: watch.ID, Enabled: enabled });
    await update('watches');
    flash(t(enabled ? 'watches.autobook_enabled' : 'watches.autobook_disabled'));
  });
}

// Auto-booking acts on the user's behalf, so it needs an explicit confirmation of what it will do.
function autoBookDialog(button, watch) {
  const dialog = document.querySelector('#booking');
  openSession(dialog);
  dialog.querySelector('h2').textContent = t('watches.autobook_title');
  dialog.querySelector('dl').innerHTML = `<div><dt>${escape(watch.ServiceName || watch.Name)}</dt><dd>${escape(watch.CityName)}</dd></div>`;
  const note = dialog.querySelector('#booking-note');
  note.className = 'muted note';
  note.removeAttribute('role');
  note.textContent = t('watches.autobook_note');
  const open = dialog.querySelector('#booking-open');
  open.hidden = false;
  open.lastElementChild.hidden = true;
  open.firstChild.textContent = t('watches.autobook_enable');
  open.onclick = () => { dialog.close(); setAutoBook(button, watch, true); };
  dialog.querySelector('#booking-cancel').textContent = t('common.cancel');
  dialog.querySelector('#booking-cancel').onclick = () => dialog.close();
  dialog.showModal();
}

function watches() {
  const items = state.Watches ?? [];
  show(titleFor('watches'), `${watchStatus()}<div class="stack list">
    ${items.length ? items.map(watchCard).join('') : `<div class="empty"><h2>${text('watches.empty')}</h2><p>${text('watches.empty_hint')}</p></div>`}
    ${items.length ? `<section class="stack">
      <p class="muted" id="check-hint">${text('watches.check_chat_hint')}</p>
      <button type="button" class="secondary" id="check-in-chat" aria-describedby="check-hint">${text('watches.check_chat')}</button>
    </section>` : ''}
  </div>${newWatchButton()}`);
  bindShared();
  for (const button of main.querySelectorAll('[data-edit]')) {
    button.onclick = () => void editor(items.find((item) => item.ID === Number(button.dataset.edit)));
  }
  for (const button of main.querySelectorAll('[data-watch]')) {
    button.onclick = () => void run(button, async () => {
      await api('status', { ID: Number(button.dataset.watch), Status: button.dataset.status });
      await update('watches');
    });
  }
  for (const button of main.querySelectorAll('[data-autobook]')) {
    const watch = items.find((item) => item.ID === Number(button.dataset.autobook));
    button.onclick = () => (watch.AutoBook ? setAutoBook(button, watch, false) : autoBookDialog(button, watch));
  }
  const chat = main.querySelector('#check-in-chat');
  if (chat) chat.onclick = () => {
    if (telegram?.close) telegram.close();
    else flash(t('watches.check_chat_hint'));
  };
}

// Day names come from the catalog: browsers lack some locales (Belarusian falls back to Russian).
const weekdayIndex = new Intl.DateTimeFormat('en', { timeZone, weekday: 'short' });
const dayNames = ['Mon', 'Tue', 'Wed', 'Thu', 'Fri', 'Sat', 'Sun'];
function dayLabel(value) {
  const label = `${weekdays[dayNames.indexOf(weekdayIndex.format(new Date(value))) + 1]}, ${dateText(value)}`;
  return label[0].toLocaleUpperCase(language) + label.slice(1);
}
const isExpired = (slot, now) => slot.Status === 'expired' || new Date(slot.DateTime) <= now;

// One found time is one ticket coupon: the stub carries the date, the body the time, doctor and street.
function coupon({ slot, index, available, time }, now, next) {
  const when = clock.format(time);
  const doctor = slot.Doctor || t('history.no_doctor');
  const address = slot.Address || slot.Facility;
  const { day } = dateParts(time);
  const weekday = weekdays[dayNames.indexOf(weekdayIndex.format(new Date(time))) + 1];
  const stub = `<span class="stub" aria-hidden="true"><small>${escape(weekday)}</small><b>${Number(day)}</b><small>${escape(shortDate(time).month)}</small></span>`;
  const clockTime = `<time class="t" datetime="${escape(slot.DateTime)}">${escape(when)}</time>`;
  const details = `<span class="who">${escape(doctor)}${slot.Service ? ` · ${escape(slot.Service)}` : ''}</span><small>${escape(address)}</small>`;
  if (!available) {
    const stamp = text(time <= now ? 'history.stamp_past' : 'history.stamp_gone');
    return `<li><div class="coupon void" tabindex="-1" data-expired-slot="${index}">
      <span class="sr-only">${escape(dayLabel(time))}</span>${stub}<span class="body">
        <span class="coupon-head">${clockTime}<span class="stamp">${stamp}</span></span>${details}</span></div></li>`;
  }
  return `<li><button type="button" class="coupon${next ? ' first' : ''}" data-slot="${index}" aria-label="${escape(`${dayLabel(time)}. ${t('history.row_label', { time: when, doctor: [doctor, slot.Service].filter(Boolean).join(', '), address })}`)}">
    ${stub}<span class="body">${clockTime}${details}</span></button></li>`;
}

// Open times first, soonest at the top; times that are gone sink below, newest first.
function slotGroups(slots, now) {
  const rows = slots.map((slot, index) => ({ slot, index, time: Date.parse(slot.DateTime), available: !isExpired(slot, now) }));
  const open = rows.filter((row) => row.available).sort((a, b) => a.time - b.time);
  const gone = rows.filter((row) => !row.available).sort((a, b) => b.time - a.time);
  return `<ul class="wallet">${[...open, ...gone].map((row, i) => coupon(row, now, row.available && i === 0)).join('')}</ul>`;
}

// The booking dialog is shared; each opening gets a session so late answers can tell
// whether the dialog still shows their request.
let dialogSession = 0;
function openSession(dialog) {
  dialog.dataset.session = String(++dialogSession);
  return dialog.dataset.session;
}
const current = (dialog, session) => dialog.open && dialog.dataset.session === session;

function bookingDialog(slot) {
  const dialog = document.querySelector('#booking');
  const date = new Date(slot.DateTime);
  dialog.querySelector('h2').textContent = t('history.confirm_title');
  dialog.querySelector('dl').innerHTML = [
    [t('history.appointment'), slot.Service || t('history.appointment')],
    [dayLabel(date), clock.format(date)],
    [slot.Doctor || t('history.no_doctor'), slot.Facility],
    [slot.Address, ''],
  ].filter(([a]) => a).map(([a, b]) => `<div><dt>${escape(a)}</dt>${b ? `<dd>${escape(b)}</dd>` : ''}</div>`).join('');
  const note = dialog.querySelector('#booking-note');
  const open = dialog.querySelector('#booking-open');
  note.textContent = t(state.Booking ? 'history.book_note' : 'history.confirm_note');
  note.className = 'muted note';
  note.removeAttribute('role');
  open.hidden = false;
  open.lastElementChild.hidden = Boolean(state.Booking);
  open.firstChild.textContent = `${t(state.Booking ? 'history.book_confirm' : 'history.confirm_open')} `;
  const session = openSession(dialog);
  open.onclick = state.Booking ? () => void run(open, async () => {
    // The server answers with the provider's decision: confirmed, or the time is already taken.
    let Status;
    try {
      ({ Status } = await api('book', { ID: slot.ID }));
    } catch (error) {
      if (current(dialog, session)) note.textContent = errorText(error);
      else flash(errorText(error));
      return;
    }
    const confirmed = Status === 'confirmed';
    const outcome = confirmed
      ? t('history.book_confirmed', { time: `${dayLabel(date)}, ${clock.format(date)}`, address: slot.Address || slot.Facility })
      : t('history.book_taken');
    slot.Status = 'expired';
    // A late answer must not rewrite a dialog that now shows another time: announce it
    // and refresh the list once no dialog is open.
    if (!current(dialog, session)) {
      // switchTab clears announcements, so announce after the list is refreshed.
      const refresh = async () => { await switchTab('history'); flash(outcome); };
      if (tab !== 'history') flash(outcome);
      else if (!dialog.open) void refresh();
      else {
        flash(outcome);
        dialog.addEventListener('close', () => void refresh(), { once: true });
      }
      return;
    }
    note.className = 'note outcome';
    note.dataset.kind = confirmed ? 'confirmed' : 'taken';
    note.setAttribute('role', 'status');
    note.textContent = outcome;
    open.hidden = true;
    dialog.querySelector('#booking-cancel').textContent = t('common.close');
    dialog.addEventListener('close', () => void switchTab('history'), { once: true });
  }) : () => {
    dialog.close();
    if (telegram?.openLink) telegram.openLink(portal);
    else window.open(portal, '_blank', 'noopener');
  };
  dialog.querySelector('#booking-cancel').textContent = t('common.cancel');
  dialog.querySelector('#booking-cancel').onclick = () => dialog.close();
  dialog.showModal();
}

async function historyScreen(id) {
  show(titleFor('history'), `${watchStatus()}<div class="stack" id="slots" tabindex="-1"><p class="muted">${text('history.loading')}</p></div>${newWatchButton()}`);
  bindShared();
  const results = main.querySelector('#slots');
  const loaded = [];
  const byID = new Map();
  async function append(offset, cursor) {
    let page;
    try {
      page = await api('history', cursor ? { After: cursor } : { Offset: offset });
    } catch (error) {
      if (cursor && error?.key === 'error.stale' && id === renderID) return historyScreen(id);
      throw error;
    }
    if (id !== renderID) return;
    const { Slots, More, Next } = page;
    const first = loaded.length;
    for (const slot of Slots) {
      const previous = byID.get(slot.ID);
      if (previous) {
        Object.assign(previous, slot);
      } else {
        byID.set(slot.ID, slot);
        loaded.push(slot);
      }
    }
    const now = new Date();
    if (!loaded.length) {
      results.innerHTML = `<div class="empty"><span class="stub ghost" aria-hidden="true"></span><h2>${text('history.empty')}</h2><p>${text(state.Watches?.length ? 'history.empty_hint' : 'watches.empty_hint')}</p></div>`;
      return;
    }
    results.innerHTML = `${slotGroups(loaded, now)}${More ? `<button class="secondary" type="button" data-more>${text('common.more')}</button>` : ''}`;
    for (const button of results.querySelectorAll('[data-slot]')) button.onclick = () => bookingDialog(loaded[Number(button.dataset.slot)]);
    const next = results.querySelector('[data-more]');
    if (next) next.onclick = () => void run(next, () => append(offset + Slots.length, Next));
    // Re-rendering removes the focused button. Keep focus at the newly loaded results.
    if (offset) {
      const available = [...results.querySelectorAll('[data-slot]')].find((row) => Number(row.dataset.slot) >= first);
      const unavailable = [...results.querySelectorAll('[data-expired-slot]')].find((row) => Number(row.dataset.expiredSlot) >= first);
      const target = available ?? next ?? unavailable ?? results;
      target.focus();
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
    <form class="stack" id="preferences">
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
  const form = main.querySelector('#preferences');
  const error = form.querySelector('[data-error]');
  const loadCities = main.querySelector('#load-cities');
  const selected = { ID: state.CityID, Name: state.CityName };
  function fillCities(items) {
    const select = form.elements.city;
    const value = select.value || String(selected.ID ?? 0);
    select.innerHTML = cityOptions(items, selected, t('settings.no_city'));
    select.value = value;
  }
  fillCities(cities ?? []);
  loadCities.hidden = true;
  if (!cities) {
    getCities().then((items) => { if (id === renderID) fillCities(items); })
      .catch((failure) => { if (id === renderID) { loadCities.hidden = false; error.textContent = errorText(failure); } });
  }
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
  markTab('watches');
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
  const presetDays = ['14', '30', '90'];
  const hourPresets = [['', 'editor.any_time'], ['07:00-12:00', 'editor.morning'], ['12:00-17:00', 'editor.afternoon'], ['17:00-21:00', 'editor.evening']];
  // Three short steps instead of one long form: what, when, then a review that saves.
  show(title, `<form class="stack wizard" novalidate>
    <div class="progress"><span class="dots" aria-hidden="true"><i></i><i></i><i></i></span><span id="step-label"></span></div>
    <section class="step" data-step="1">
      <h2 tabindex="-1">${text('editor.what')}</h2>
      <label class="field">${text('editor.city')}<select name="city" required>${cityOptions(options, city, t('editor.choose_city'))}</select></label>
      ${watch ? '' : `<section class="quick-services stack" tabindex="-1" aria-label="${text('editor.recommendations')}">
        <p class="muted" id="picks-status" role="status"></p>
        <div class="stack" id="picks-list"></div>
        <button type="button" class="secondary" id="retry-picks" hidden>${text('common.retry')}</button>
      </section>`}
      <div class="search-field">
        <label class="field">${text('editor.service')}<input type="search" name="query" placeholder="${text('editor.service_placeholder')}" maxlength="80" autocomplete="off"></label>
        <button type="button" class="secondary" id="search-services">${text('editor.find_service')}</button>
      </div>
      <div class="options" id="services" aria-live="polite"></div>
      <p class="selection" id="selected-service" tabindex="-1" data-set="false"></p>
      <a class="flat referrals" href="${referralsPortal}" target="_blank" rel="noopener">${text('editor.referrals')} ${icon('link')}</a>
    </section>
    <section class="step" data-step="2" hidden>
      <h2 tabindex="-1">${text('editor.when')}</h2>
      <div class="group" role="group" aria-labelledby="period-label"><p class="label" id="period-label">${text('editor.period')}</p>
        <div class="chips">${['editor.preset_2w', 'editor.preset_1m', 'editor.preset_3m'].map((key, i) =>
          `<button type="button" class="chip" data-days="${presetDays[i]}">${text(key)}</button>`).join('')}
          <button type="button" class="chip" data-range>${text('editor.date_range')}</button></div></div>
      <select name="dateMode" hidden><option value="days">${text('editor.upcoming')}</option><option value="range">${text('editor.date_range')}</option></select>
      <label class="field" id="days">${text('editor.days_ahead')}<input name="days" type="number" min="1" max="90" value="${watch?.NextDays || 14}"></label>
      <div class="columns" id="range" hidden>
        <label class="field">${text('editor.from')}<input name="from" type="date" value="${dateOnly(watch?.DateFrom)}"></label>
        <label class="field">${text('editor.to')}<input name="to" type="date" value="${dateOnly(watch?.DateTo)}"></label>
      </div>
      <div class="group" role="group" aria-labelledby="hours-label"><p class="label" id="hours-label">${text('editor.time_of_day')}</p>
        <div class="chips">${hourPresets.map(([hours, key]) => `<button type="button" class="chip" data-hours="${hours}">${text(key)}</button>`).join('')}</div></div>
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
    </section>
    <section class="step" data-step="3" hidden>
      <h2 tabindex="-1" id="summary-heading">${text('editor.summary')}</h2>
      <dl id="summary" class="summary"></dl>
      <div class="advanced" id="advanced" hidden>
        <div class="stack">
          <button type="button" class="secondary" id="providers">${text('editor.choose_providers')}</button>
          <div class="stack" id="provider-options"></div>
          <label class="field">${text('editor.interval')}<input name="minutes" type="number" required min="${state.MinimumMinutes}" max="1440" value="${watch ? Math.ceil(watch.CheckIntervalSeconds / 60) : state.DefaultMinutes}"></label>
        </div>
      </div>
      ${watch ? `<button type="button" class="flat danger" id="delete">${text('editor.delete')}</button>` : ''}
    </section>
    <p class="muted error" data-error role="status"></p>
    <div class="dock step-nav">
      <button type="button" class="secondary" id="back"></button>
      <button type="button" class="primary" id="next">${text('common.next')}</button>
      <button type="submit" class="primary" id="save" hidden>${text('editor.save')}</button>
    </div>
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
    markChips();
    const range = fields.dateMode.value === 'range';
    const preset = form.querySelector(`[data-days="${fields.days.value}"]`);
    const dates = !range ? preset?.textContent ?? t('watches.period_days', { days: fields.days.value })
      : fields.from.value && fields.to.value ? `${dateText(fields.from.value)} — ${dateText(fields.to.value)}` : '—';
    const { doctorName, clinics } = providerSummary();
    const hours = selectedHours();
    const rows = [
      ['editor.service', service?.Name ?? t('editor.choose_service'), 1],
      ['editor.city', Number(fields.city.value) ? fields.city.selectedOptions[0]?.textContent : t('editor.choose_city'), 1],
      ['editor.period', dates, 2],
      ['editor.days_and_hours', form.querySelector('[data-hours][aria-pressed="true"]')?.textContent ?? hoursLabel(hours), 2],
      ['editor.doctor', doctorName, 4],
      ['editor.clinics', clinics, 4],
      ['editor.interval', fields.minutes.value || '—', 4],
    ];
    summary.innerHTML = rows.map(([label, value, goto]) => `<div><dt>${text(label)}</dt><dd>${escape(value)}</dd>${goto
      ? `<button type="button" class="flat change" data-goto="${goto}" aria-label="${escape(`${t('editor.change')}: ${t(label)}`)}">${text('editor.change')}</button>` : ''}</div>`).join('');

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
    quickPicks.hidden = false;
    retryPicks.hidden = true;
    picksStatus.hidden = false;
    picksStatus.textContent = t(cityID ? 'editor.loading_recommendations' : 'editor.choose_city');
    if (!cityID) return;
    try {
      const { Referred, Recent, Saved, Popular, PortalIncomplete } = await api('procedure-picks', { CityID: cityID });
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
      picksStatus.hidden = !PortalIncomplete && picksList.childElementCount > 0;
      if (!picksStatus.hidden) {
        picksStatus.textContent = t(PortalIncomplete ? 'error.luxmed_unavailable' : 'editor.no_recommendations');
      }
      retryPicks.hidden = !PortalIncomplete;
      // Nothing to recommend: drop the block instead of announcing its absence.
      const empty = !PortalIncomplete && !picksList.childElementCount;
      quickPicks.hidden = empty;
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
    form.querySelector('#days').hidden = range || presetDays.includes(fields.days.value);
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
  function markChips() {
    const range = fields.dateMode.value === 'range';
    for (const chip of form.querySelectorAll('[data-days]')) chip.setAttribute('aria-pressed', String(!range && chip.dataset.days === fields.days.value));
    form.querySelector('[data-range]').setAttribute('aria-pressed', String(range));
    const hours = selectedHours();
    for (const chip of form.querySelectorAll('[data-hours]')) {
      const [from, to] = chip.dataset.hours.split('-');
      const on = chip.dataset.hours ? hours.length === 1 && hours[0].weekday === 0 && hours[0].from === from && hours[0].to === to : !hours.length;
      chip.setAttribute('aria-pressed', String(on));
    }
  }
  for (const chip of form.querySelectorAll('[data-days], [data-range]')) {
    chip.onclick = () => {
      fields.dateMode.value = chip.dataset.days ? 'days' : 'range';
      if (chip.dataset.days) fields.days.value = chip.dataset.days;
      updateDates();
      summarize();
      if (!chip.dataset.days) fields.from.focus();
    };
  }
  // A time-of-day chip writes the "Every day" row; specific days stay in the disclosure below.
  for (const chip of form.querySelectorAll('[data-hours]')) {
    chip.onclick = () => {
      for (const row of form.querySelectorAll('[data-day]')) row.querySelector('[type="checkbox"]').checked = false;
      if (chip.dataset.hours) {
        const every = form.querySelector('[data-day="0"]');
        const [from, to] = every.querySelectorAll('input[type="time"]');
        [from.value, to.value] = chip.dataset.hours.split('-');
        every.querySelector('[type="checkbox"]').checked = true;
      }
      summarize();
    };
  }

  const back = form.querySelector('#back');
  const next = form.querySelector('#next');
  const save = form.querySelector('#save');
  const stepLabel = form.querySelector('#step-label');
  let step = 1;
  function goTo(n, focus = true) {
    step = n;
    for (const section of form.querySelectorAll('.step')) section.hidden = Number(section.dataset.step) !== n;
    form.querySelectorAll('.dots i').forEach((dot, i) => { dot.dataset.on = String(i < n); });
    stepLabel.textContent = t('editor.step', { n });
    back.textContent = t(n === 1 ? 'common.cancel' : 'common.back');
    next.hidden = n === 3;
    save.hidden = n !== 3;
    error.textContent = '';
    if (n === 3) summarize();
    if (focus) {
      window.scrollTo(0, 0);
      form.querySelector(`[data-step="${n}"] h2`).focus({ preventScroll: true });
    }
  }
  function serviceMissing() {
    goTo(1, false);
    picked.dataset.set = 'error';
    picked.textContent = t('editor.choose_service');
    picked.focus();
  }
  function stepInvalid(n) {
    const invalid = [...form.querySelectorAll(`[data-step="${n}"] :is(input, select)`)].find((field) => field.willValidate && !field.validity.valid);
    if (!invalid) return false;
    invalid.closest('details')?.setAttribute('open', '');
    invalid.closest('#advanced')?.removeAttribute('hidden');
    invalid.focus();
    error.textContent = t('error.invalid_fields');
    return true;
  }
  next.onclick = () => {
    if (step === 1 && !service) return serviceMissing();
    if (stepInvalid(step)) return;
    goTo(step + 1);
  };
  back.onclick = () => (step === 1 ? void switchTab(tab) : goTo(step - 1));
  const advanced = form.querySelector('#advanced');
  summary.onclick = (event) => {
    const target = event.target.closest('[data-goto]');
    if (!target) return;
    // Doctor, clinics and interval open in place below the review instead of a fourth step.
    if (target.dataset.goto === '4') {
      advanced.hidden = false;
      (providers.disabled ? fields.minutes : providers).focus();
      return;
    }
    goTo(Number(target.dataset.goto));
  };
  // Editing starts at the review: everything is already filled in.
  goTo(watch ? 3 : 1, false);

  form.onsubmit = (event) => {
    event.preventDefault();
    if (step < 3) {
      next.click();
      return;
    }
    void run(save, async () => {
      const invalid = Array.from(form.elements).find((field) => field.willValidate && !field.validity.valid);
      if (invalid) {
        const section = invalid.closest('details');
        if (section) section.open = true;
        invalid.focus();
        throw new DashboardError('error.invalid_fields');
      }
      if (!service) return serviceMissing();
      const windows = selectedHours();
      const range = fields.dateMode.value === 'range';
      await api('watch', { ID: watch?.ID ?? 0, CityID: Number(fields.city.value), ServiceID: service.ID, DoctorID: doctor,
        FacilityIDs: [...facilities], DateFrom: range ? fields.from.value : '', DateTo: range ? fields.to.value : '',
        NextDays: range ? 0 : Number(fields.days.value), TimeWindows: windows, CheckMinutes: Number(fields.minutes.value) });
      await update('watches');
      flash(t('watches.saved'));
    }, error);
  };
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
  show(title, `<div class="card stack"><p class="error">${escape(errorText(error))}</p>${retry ? `<button type="button" class="secondary">${text('common.retry')}</button>` : ''}</div>`);
  if (retry) main.querySelector('button').onclick = retry;
}

async function switchTab(next, { reload = true } = {}) {
  clearTimeout(flashTimer);
  announcement.textContent = '';
  tab = next;
  const id = ++renderID;
  markTab(tab);
  try {
    if (reload) {
      show(titleFor(next), `<p>${text('dashboard.loading')}</p>`, false);
      const latest = await api('load');
      if (id !== renderID) return;
      applySnapshot(latest);
    }
    if (tab === 'watches') watches();
    if (tab === 'history') await historyScreen(id);
    if (tab === 'settings') await settingsScreen(id);
  } catch (error) {
    if (id === renderID) screenError(titleFor(next), error, () => switchTab(next, { reload }));
  }
}

function applySnapshot(latest) {
  state = latest;
  setCount('watches', state.Watches?.length ?? 0);
  setCount('history', state.AvailableSlots);
  setLanguage(state.Locale);
  renderLanguage();
}

async function update(next) {
  applySnapshot(await api('load'));
  navigation.hidden = false;
  await switchTab(next, { reload: false });
}

async function start() {
  const params = new URLSearchParams(location.search);
  setLanguage(initialLanguage(telegram, params.get('lang')));
  renderLanguage();
  if (!telegram?.initData) {
    screenError('Luxmed Watcher', new DashboardError('error.open_from_bot'));
    return;
  }
  show('Luxmed Watcher', `<p class="muted">${text('dashboard.loading')}</p>`, false);
  // Older bot links still say tab=home; Found is the home now.
  const initial = params.get('tab') === 'home' ? 'history' : params.get('tab');
  try {
    ({ token: apiToken } = await api('auth'));
    await update(tabs.includes(initial) ? initial : 'history');
  } catch (error) {
    screenError('Luxmed Watcher', error, error?.key === 'error.access_denied' ? undefined : start);
  }
}

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
