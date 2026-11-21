export const languages = Object.freeze({
  pl: 'Polski', en: 'English', ru: 'Русский', be: 'Беларуская', uk: 'Українська',
});

const fallback = 'pl';
export let language = fallback;
let messages = {};

export function initialLanguage(telegram, hint) {
  if (Object.hasOwn(languages, hint)) return hint;
  for (const value of [telegram?.initDataUnsafe?.user?.language_code, ...(navigator.languages ?? []), navigator.language]) {
    if (typeof value !== 'string') continue;
    const locale = value.toLowerCase().split(/[-_]/, 1)[0];
    if (Object.hasOwn(languages, locale)) return locale;
  }
  return fallback;
}

export async function loadLanguage(locale) {
  locale = Object.hasOwn(languages, locale) ? locale : fallback;
  if (locale === language && Object.keys(messages).length) return;
  try {
    const response = await fetch(`/i18n/${locale}`, {
      redirect: 'error', signal: AbortSignal.timeout?.(5000),
    });
    if (!response.ok) throw new Error('Translation unavailable');
    messages = await response.json();
    language = locale;
    document.documentElement.lang = locale;
  } catch (error) {
    if (locale === fallback) throw error;
    await loadLanguage(fallback);
  }
}

export function t(key, values = {}) {
  return messages[key]?.replace(/\{(\w+)\}/g, (_, name) => values[name] ?? '') ?? '';
}
