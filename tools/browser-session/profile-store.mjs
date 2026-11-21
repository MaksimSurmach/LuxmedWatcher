import { randomUUID } from 'node:crypto';
import { constants } from 'node:fs';
import { lstat, mkdir, open, rename, rm, writeFile } from 'node:fs/promises';
import { join } from 'node:path';

function isStorageState(state) {
  return Array.isArray(state?.cookies) && Array.isArray(state?.origins);
}

async function profilePath(profileDir, id) {
  if (typeof id !== 'string' || !/^[1-9][0-9]*$/u.test(id)) throw new Error('Invalid browser account ID');
  const directory = join(profileDir, id);
  await mkdir(directory, { mode: 0o700, recursive: true });
  const accountDir = await lstat(directory);
  if (!accountDir.isDirectory() || (accountDir.mode & 0o077)) throw new Error('Browser profile directory must be private');
  return join(directory, 'storage-state.json');
}

export async function readProfile(profileDir, id) {
  const path = await profilePath(profileDir, id);
  try {
    const file = await open(path, constants.O_RDONLY | constants.O_NOFOLLOW);
    try {
      const stat = await file.stat();
      if (!stat.isFile() || (stat.mode & 0o077)) throw new Error('State must be a private file');
      const { accountId, storageState } = JSON.parse(await file.readFile('utf8'));
      if (accountId !== id || !isStorageState(storageState)) throw new Error('Invalid browser profile state');
      return storageState;
    } finally {
      await file.close();
    }
  } catch (error) {
    if (error.code !== 'ENOENT') throw new Error('Cannot read browser profile state');
  }
}

export async function writeProfile(profileDir, id, storageState) {
  if (!isStorageState(storageState)) throw new Error('Invalid browser profile state');
  const path = await profilePath(profileDir, id);
  const temporary = `${path}.${randomUUID()}`;
  try {
    await writeFile(temporary, JSON.stringify({ accountId: id, storageState }), { flag: 'wx', mode: 0o600 });
    await rename(temporary, path);
  } finally {
    await rm(temporary, { force: true });
  }
}
