import { mkdtempSync, rmSync } from 'node:fs';

// Each suite owns exactly the directories it creates, including partial
// fixtures left by failed assertions. Never discover/delete other runs by
// prefix or age: their files may still be in use or retained as evidence.
export function createTempFixtures() {
  const owned = new Set();
  const cleanup = () => {
    const errors = [];
    for (const dir of owned) {
      try {
        rmSync(dir, { recursive: true, force: true, maxRetries: 3 });
        owned.delete(dir);
      } catch (error) {
        errors.push(error);
      }
    }
    if (errors.length) throw new AggregateError(errors, 'Could not remove test fixtures');
  };
  // Also cover an orderly early worker exit before afterAll. SIGKILL/host
  // crashes cannot run cleanup; their leftovers are not safe to adopt blindly.
  const onExit = () => {
    try { cleanup(); } catch (error) { console.error(error.message); }
  };
  process.once('exit', onExit);
  return {
    mkdtempSync(prefix, options) {
      const dir = mkdtempSync(prefix, options);
      owned.add(dir);
      return dir;
    },
    cleanup,
    dispose() {
      cleanup();
      process.removeListener('exit', onExit);
    },
  };
}
