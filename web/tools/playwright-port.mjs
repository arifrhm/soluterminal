import net from 'node:net';

const runPortEnvironmentKey = 'SOLU_PLAYWRIGHT_RUN_PORT';

/**
 * Ask the OS for an unused loopback port for this Playwright invocation.
 * The listener is closed before Playwright starts its web server, so the
 * configured command can bind the selected port itself.
 */
export function allocateLoopbackPort({
  createServer = () => net.createServer(),
} = {}) {
  return new Promise((resolve, reject) => {
    const server = createServer();
    server.unref();
    server.once('error', reject);
    server.listen(0, '127.0.0.1', () => {
      const address = server.address();
      if (!address || typeof address === 'string') {
        server.close();
        reject(new Error('could not allocate a loopback TCP port for Playwright'));
        return;
      }
      server.close((error) => {
        if (error) reject(error);
        else resolve(address.port);
      });
    });
  });
}

/**
 * Keep one allocated port across every process Playwright starts for a run.
 * Playwright reloads the config in replacement workers after a failed test;
 * without the inherited environment value, each worker would target a fresh
 * port while the web server remained on the original one.
 */
export async function resolvePlaywrightRunPort({
  env = process.env,
  allocate = allocateLoopbackPort,
} = {}) {
  const inherited = Number(env[runPortEnvironmentKey]);
  if (Number.isInteger(inherited) && inherited > 0 && inherited <= 65_535) {
    return inherited;
  }

  const port = await allocate();
  env[runPortEnvironmentKey] = String(port);
  return port;
}
