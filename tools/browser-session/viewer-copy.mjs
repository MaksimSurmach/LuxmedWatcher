const messages = {
  pl: {
    title: 'Logowanie do LUX MED',
    loading: 'Łączenie z przeglądarką…',
    close: 'Zamknij podgląd',
    connected: 'Zaloguj się do LUX MED. Jeśli pojawi się prośba o kod, wpisz go w oknie logowania.',
    closed: 'Podgląd zamknięty. Aby zalogować się ponownie, poproś bota o nowy link.',
    missing: 'Poproś bota w Telegramie o link do logowania.',
    failed: 'Nie udało się połączyć z przeglądarką. Poproś bota o nowy link.',
  },
  en: {
    title: 'Sign in to LUX MED',
    loading: 'Connecting to the browser…',
    close: 'Close viewer',
    connected: 'Sign in to LUX MED. If prompted for a code, enter it in the sign-in window.',
    closed: 'Viewer closed. Ask the bot for a new link to sign in again.',
    missing: 'Ask the Telegram bot for a sign-in link.',
    failed: 'Could not connect to the browser. Ask the bot for a new link.',
  },
  ru: {
    title: 'Вход в LUX MED',
    loading: 'Подключение к браузеру…',
    close: 'Закрыть просмотр',
    connected: 'Войдите в LUX MED. Если потребуется код, введите его в окне входа.',
    closed: 'Просмотр закрыт. Для повторного входа запросите в боте новую ссылку.',
    missing: 'Запросите ссылку для входа в боте Telegram.',
    failed: 'Не удалось подключиться к браузеру. Запросите в боте новую ссылку.',
  },
  uk: {
    title: 'Вхід у LUX MED',
    loading: 'Підключення до браузера…',
    close: 'Закрити перегляд',
    connected: 'Увійдіть у LUX MED. Якщо знадобиться код, введіть його у вікні входу.',
    closed: 'Перегляд закрито. Для повторного входу попросіть бота надіслати нове посилання.',
    missing: 'Попросіть бота в Telegram надіслати посилання для входу.',
    failed: 'Не вдалося підключитися до браузера. Попросіть бота надіслати нове посилання.',
  },
  be: {
    title: 'Уваход у LUX MED',
    loading: 'Падключэнне да браўзера…',
    close: 'Закрыць прагляд',
    connected: 'Увайдзіце ў LUX MED. Калі спатрэбіцца код, увядзіце яго ў акне ўваходу.',
    closed: 'Прагляд закрыты. Каб увайсці зноў, папрасіце бота даслаць новую спасылку.',
    missing: 'Папрасіце бота ў Telegram даслаць спасылку для ўваходу.',
    failed: 'Не ўдалося падключыцца да браўзера. Папрасіце бота даслаць новую спасылку.',
  },
};

export function viewerCopy(locale) {
  const lang = Object.hasOwn(messages, locale) ? locale : 'ru';
  return { lang, ...messages[lang] };
}

export function renderViewerPage(template, locale) {
  const copy = viewerCopy(locale);
  return template.replace(/{{(lang|title|loading|close)}}/g, (_, key) => copy[key]);
}
