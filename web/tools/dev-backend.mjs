// Shared by Vite and its isolation regression tests.
export function devBackend(env = process.env) {
  const target = env.SOLU_DEV_BACKEND || 'http://127.0.0.1:8787';
  if (env.SOLU_LOCAL_PREVIEW === '1') {
    const url = new URL(target);
    if (url.protocol !== 'http:' || url.hostname !== '127.0.0.1' ||
        !url.port || [8787, 8799, 18787, 28787, 38787].includes(Number(url.port)) ||
        url.username || url.password || url.pathname !== '/' || url.search || url.hash) {
      throw new Error('Local preview requires its own loopback daemon port');
    }
  }
  return target;
}
