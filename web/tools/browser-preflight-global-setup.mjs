import { ensureBrowserCanStart } from './browser-preflight.mjs';

export default async function () {
  await ensureBrowserCanStart();
}
