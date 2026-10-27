import { defineConfig } from 'playwright/test';
import { createPlaywrightConfig } from './tools/playwright-config.mjs';

export default defineConfig(await createPlaywrightConfig());
