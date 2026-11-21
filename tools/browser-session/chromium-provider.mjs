import { execFile } from 'node:child_process';
import { fileURLToPath } from 'node:url';
import { promisify } from 'node:util';
import { chromium } from 'playwright';
import { readProfile, writeProfile } from './profile-store.mjs';

async function launchChromium(executablePath) {
  const options = {
    executablePath,
    chromiumSandbox: true,
    headless: true,
    ignoreDefaultArgs: ['--password-store=basic', '--use-mock-keychain'],
    timeout: 15_000,
  };
  try {
    return await chromium.launch(options);
  } catch (error) {
    // shortcut: Playwright reports a missing headless shell as text, use its error code when one exists.
    if (executablePath || !error.message.startsWith("browserType.launch: Executable doesn't exist at ")) throw error;
    const cli = fileURLToPath(new URL('cli.js', import.meta.resolve('playwright/package.json')));
    await promisify(execFile)(process.execPath, [cli, 'install', '--only-shell', '--no-remove', 'chromium'], { timeout: 180_000 });
    return chromium.launch(options);
  }
}

export async function createChromiumProvider({ profileDir = '/profiles', executablePath } = {}) {
  const accounts = new Map();
  let browser;
  let launching;
  let shutdown;

  async function getBrowser() {
    if (browser?.isConnected()) return browser;
    launching ??= launchChromium(executablePath).then(opened => {
      browser = opened;
      return opened;
    }).finally(() => { launching = undefined; });
    return launching;
  }

  async function closeIdleBrowser() {
    if (accounts.size !== 0 || !browser) return;
    const idle = browser;
    browser = undefined;
    await idle.close().catch(() => {});
  }

  async function openAccount(id, entry) {
    const storageState = await readProfile(profileDir, id);
    const current = await getBrowser();
    const context = await current.newContext({
      acceptDownloads: false,
      storageState,
      viewport: { width: 1180, height: 728 },
    }).catch(() => { throw new Error('Cannot open browser profile context'); });
    let page;
    try {
      page = await context.newPage();
    } catch (error) {
      await context.close().catch(() => {});
      throw error;
    }

    let saving = Promise.resolve();
    return {
      page,
      async save() {
        if (entry.closing) throw new Error('Browser profile is closed');
        const saved = saving.then(async () => {
          const storageState = await context.storageState({ indexedDB: true });
          await writeProfile(profileDir, id, storageState);
        });
        saving = saved.catch(() => {});
        return saved;
      },
      close() {
        entry.closing ??= saving.then(() => context.close()).finally(async () => {
          accounts.delete(id);
          await closeIdleBrowser();
        });
        return entry.closing;
      },
    };
  }

  async function openSession(id) {
    if (shutdown) throw new Error('Browser provider is closed');
    const account = accounts.get(id);
    if (account) {
      const session = await account.pending;
      if (!account.closing && !session.page.isClosed() && session.page.context().browser()?.isConnected()) return session;
      await session.close();
      return openSession(id);
    }
    const entry = {};
    entry.pending = openAccount(id, entry).catch(async error => {
      accounts.delete(id);
      await closeIdleBrowser();
      throw error;
    });
    accounts.set(id, entry);
    return entry.pending;
  }

  return {
    open: openSession,
    close() {
      shutdown ??= Promise.allSettled(Array.from(accounts.values(), async ({ pending }) => (await pending).close()))
        .then(() => browser?.close());
      return shutdown;
    },
  };
}
