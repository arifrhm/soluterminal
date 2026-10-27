import { defineConfig } from 'vitest/config';
import react from '@vitejs/plugin-react';

// Separate vitest config so vite.config.ts stays focused on the
// production build. Sharing one file works but tangles `server.proxy`
// into the test runner's surface area, which we don't want.
export default defineConfig({
  plugins: [react()],
  test: {
    environment: 'jsdom',
    globals: true,
    include: ['src/**/*.test.{ts,tsx}', 'tools/**/*.test.mjs'],
    maxWorkers: 4,
    setupFiles: ['./src/test/setup.ts'],
  },
});
