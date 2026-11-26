import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import test from 'node:test';
import { renderViewerPage, viewerCopy } from './viewer-copy.mjs';

const template = await readFile(new URL('./viewer.html', import.meta.url), 'utf8');

test('renders the standalone sign-in viewer in all supported languages', () => {
  const titles = {
    pl: 'Logowanie do LUX MED',
    en: 'Sign in to LUX MED',
    ru: 'Вход в LUX MED',
    uk: 'Вхід у LUX MED',
    be: 'Уваход у LUX MED',
  };
  for (const [locale, title] of Object.entries(titles)) {
    const copy = viewerCopy(locale);
    const page = renderViewerPage(template, locale);
    assert.ok(page.includes(`<html lang="${locale}">`), locale);
    assert.ok(page.includes(`<title>${title}</title>`), locale);
    assert.ok(page.includes(`role="status">${copy.loading}</span>`), locale);
    assert.ok(page.includes(`id="done" disabled>${copy.close}</button>`), locale);
    for (const key of Object.keys(viewerCopy('ru'))) {
      assert.ok(copy[key], `${locale}: missing ${key}`);
      if (locale !== 'ru') assert.notEqual(copy[key], viewerCopy('ru')[key], `${locale}: untranslated ${key}`);
    }
  }
});

test('keeps the Russian fallback for old and unsupported links', () => {
  for (const locale of [null, 'de', '__proto__']) {
    assert.equal(renderViewerPage(template, locale), renderViewerPage(template, 'ru'));
  }
});
