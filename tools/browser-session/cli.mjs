import { lstat, mkdir } from 'node:fs/promises';
import { homedir } from 'node:os';
import { join } from 'node:path';
import { checkPortal, openProfile, waitForLogin } from './session.mjs';

const [, , command] = process.argv;
if (process.argv.length !== 3 || (command !== 'connect' && command !== 'check')) {
  console.error('Usage: npm run connect or npm run check');
  process.exit(1);
}

const profileDir = join(homedir(), '.luxmed-watcher', 'chrome-profile');
let status = 'unavailable';
try {
  await mkdir(profileDir, { recursive: true, mode: 0o700 });
  const profile = await lstat(profileDir);
  if (!profile.isDirectory() || (process.platform !== 'win32' && (profile.mode & 0o077) !== 0)) {
    throw new Error('Profile directory is not private');
  }
  const browser = await openProfile(profileDir);
  try {
    const page = browser.pages()[0] ?? await browser.newPage();
    status = await checkPortal(page);
    if (command === 'connect' && status === 'needs_login') {
      console.error('Sign in directly in the LuxMed Chrome window and complete any verification.');
      status = await waitForLogin(page);
    }
  } finally {
    await browser.close();
  }
} catch {
  status = 'unavailable';
  console.error('Could not use the dedicated Chrome profile. Check that Chrome is installed and the profile is available.');
}

console.log(JSON.stringify({ status }));
process.exitCode = { ready: 0, needs_login: 2, unavailable: 3 }[status];
