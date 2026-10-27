import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'
import { readFileSync } from 'node:fs'
import { buildPreviewFixtures } from './tools/preview-fixtures.mjs'
import { devBackend } from './tools/dev-backend.mjs'

const localPreview = process.env.SOLU_LOCAL_PREVIEW === '1'
const backend = devBackend()

function serviceWorkerShellPrecache() {
  return {
    name: 'solu-service-worker-shell-precache',
    apply: 'build' as const,
    generateBundle(
      _options: unknown,
      bundle: Record<string, { fileName: string; type: string; isEntry?: boolean; imports?: string[] }>,
    ) {
      const swSource = readFileSync(new URL('./src/sw.js', import.meta.url), 'utf8')
      const chunksByFileName = new Map(
        Object.values(bundle)
          .filter((item) => item.type === 'chunk')
          .map((item) => [item.fileName, item]),
      )
      const shellJs = new Set<string>()
      const visitChunk = (fileName: string) => {
        if (shellJs.has(fileName)) return
        const chunk = chunksByFileName.get(fileName)
        if (!chunk) return
        shellJs.add(fileName)
        for (const imported of chunk.imports ?? []) visitChunk(imported)
      }
      for (const item of Object.values(bundle)) {
        if (item.type === 'chunk' && item.isEntry) visitChunk(item.fileName)
      }
      const shellUrls = Object.values(bundle)
        .filter((item) => (
          (item.type === 'chunk' && shellJs.has(item.fileName)) ||
          (item.type === 'asset' && /\.css$/.test(item.fileName))
        ))
        .map((item) => item.fileName)
        .map((fileName) => '/' + fileName)
        .sort()
      this.emitFile({
        type: 'asset',
        fileName: 'sw.js',
        source: `self.__SOLU_BUILD_SHELL_URLS__ = ${JSON.stringify(shellUrls)};\n${swSource}`,
      })
    },
  }
}

// Bake the release version (single source of truth: tauri.conf.json,
// the same file build.sh/publish.sh stamp into solud) into the bundle
// so a long-lived page can detect that the daemon underneath it was
// swapped to a newer release and reload itself — see refreshUpdateStatus
// in store.ts.
function soluVersion(): string {
  try {
    const conf = JSON.parse(
      readFileSync(new URL('../desktop/src-tauri/tauri.conf.json', import.meta.url), 'utf8'),
    ) as { version?: unknown }
    return typeof conf.version === 'string' && conf.version ? conf.version : 'dev'
  } catch {
    return 'dev'
  }
}

export default defineConfig({
  plugins: [react(), serviceWorkerShellPrecache(), {
    name: 'solu-isolated-preview-fixtures',
    config(config) {
      return { define: { __SOLU_PREVIEW_FIXTURES__: JSON.stringify(
        config.base && config.base !== '/' ? buildPreviewFixtures(soluVersion()) : {},
      ) } }
    },
  }],
  define: {
    __SOLU_WEB_VERSION__: JSON.stringify(localPreview ? 'dev' : soluVersion()),
    __SOLU_LOCAL_PREVIEW__: JSON.stringify(localPreview),
    // Only the workflow engine's capture builds (workflow_screens.go and
    // workflow_evidence_capture.go) set this; release bundles compile it false.
    __SOLU_CAPTURE_HARNESS__: JSON.stringify(process.env.SOLU_CAPTURE_HARNESS === '1'),
  },
  server: {
    port: 5173,
    proxy: {
      '/api': {
        target: backend,
        changeOrigin: true,
        ws: true,
        configure(proxy) {
          proxy.on('proxyReq', (proxyReq) => {
            proxyReq.setHeader('Origin', backend)
          })
          proxy.on('proxyReqWs', (proxyReq) => {
            proxyReq.setHeader('Origin', backend)
          })
        },
      },
    },
  },
  build: {
    outDir: 'dist',
    emptyOutDir: true,
    rollupOptions: {
      output: {
        manualChunks(id) {
          if (!id.includes('node_modules')) return undefined
          if (id.includes('@xterm/')) return 'xterm'
          if (id.includes('@supabase/')) return 'supabase'
          if (id.includes('@tauri-apps/')) return 'tauri'
          if (
            id.includes('/react/') ||
            id.includes('/react-dom/') ||
            id.includes('/scheduler/') ||
            id.includes('/zustand/')
          ) {
            return 'react-vendor'
          }
          return 'vendor'
        },
      },
    },
  },
})
