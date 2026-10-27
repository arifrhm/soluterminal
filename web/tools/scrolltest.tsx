// Dev-only harness for tools/pane-autoscroll.proof.mjs: mounts the REAL <Pane>
// (real xterm, real usePaneReaderMode, real healViewportSync) with one seeded
// terminal pane. The pane WebSocket is intercepted by the Playwright driver.
// Lives under tools/ so it is outside src/ and never shipped by the build.
import { createRoot } from 'react-dom/client';
import '../src/styles.css';
import { Pane } from '../src/components/Pane';
import { useStore } from '../src/store';
import type { PaneInfo } from '../src/types';

const pane = {
  id: 'pane_test', cwd: '/home/user', cmd: ['node', 'deepseek-terminal.mjs'],
  title: 'deepseek', status: 'running', createdAt: new Date().toISOString(),
  cols: 120, rows: 32, pid: 4242, lastActivityMs: Date.now(), state: 'idle',
  hasClaude: false, hasCodex: false, hasMuse: false, hasPi: false, hasDeepseek: true,
  backend: 'wsl', lastPrompts: [],
} as unknown as PaneInfo;

useStore.setState({
  panes: { [pane.id]: pane },
  layout: { type: 'pane', paneId: pane.id },
  focusedPaneId: pane.id,
  zoomedPaneId: undefined,
} as never);

createRoot(document.getElementById('root')!).render(
  <div style={{ width: '100%', height: '100%', display: 'flex' }}>
    <Pane pane={pane} paneIndex={1} />
  </div>,
);
