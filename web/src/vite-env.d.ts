/// <reference types="vite/client" />

// Release version baked in at build time (vite define) from
// tauri.conf.json. 'dev' for non-release builds; vitest doesn't define
// it at all, so reads must go through declared-or-undefined guards.
declare const __SOLU_WEB_VERSION__: string | undefined;

// Build-time Vite define, true only for SOLU_CAPTURE_HARNESS=1 builds;
// Vitest leaves it undefined, so reads must use a guarded check.
declare const __SOLU_CAPTURE_HARNESS__: boolean | undefined;

declare const __SOLU_PREVIEW_FIXTURES__: Record<string, unknown> | undefined;
declare const __SOLU_LOCAL_PREVIEW__: boolean | undefined;
