import dictionaries from '/i18n.js';

export const languages = Object.freeze({
  pl: 'Polski', en: 'English', ru: 'Русский', be: 'Беларуская', uk: 'Українська',
});

const fallback = 'pl';
export let language = fallback;

export function initialLanguage(telegram, hint) {
  if (Object.hasOwn(languages, hint)) return hint;
  for (const value of [telegram?.initDataUnsafe?.user?.language_code, ...(navigator.languages ?? []), navigator.language]) {
    if (typeof value !== 'string') continue;
    const locale = value.toLowerCase().split(/[-_]/, 1)[0];
    if (Object.hasOwn(languages, locale)) return locale;
  }
  return fallback;
}

export function setLanguage(locale) {
  language = Object.hasOwn(languages, locale) ? locale : fallback;
  document.documentElement.lang = language;
}

export function t(key, values = {}) {
  return dictionaries[language][key]?.replace(/\{(\w+)\}/g, (_, name) => values[name] ?? '') ?? '';
}
