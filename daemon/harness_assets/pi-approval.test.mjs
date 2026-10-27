import test from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import soluTerminalApproval, {
  APPROVAL_OPTIONS,
  allowlistFile,
  approvalComponent,
  approvalMenuLines,
  approvalPromptText,
  approve,
  isGated,
  optionLabel,
  paneSessionDir,
  paneSessionFile,
  readAllowlist,
  recordPaneSession,
  writeAllowlist,
} from './pi-approval.mjs';

function tempFile(t) {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'solu-pi-approval-'));
  t.after(() => fs.rmSync(dir, { recursive: true, force: true }));
  return path.join(dir, 'terminal-allow.json');
}

test('the allowlist lives in the managed Pi home and round-trips', (t) => {
  const file = tempFile(t);
  assert.equal(allowlistFile({ PI_CODING_AGENT_DIR: '/data/harness-state/pi' }), '/data/harness-state/pi/terminal-allow.json');
  assert.deepEqual(readAllowlist(file), []);
  writeAllowlist(['write', 'bash', 'bash', ' '], file);
  assert.deepEqual(readAllowlist(file), ['bash', 'write']);
  assert.equal(fs.statSync(file).mode & 0o777, 0o600);
});

test('read-only tools are not gated; mutating and unknown tools are', () => {
  for (const tool of ['read', 'ls', 'find', 'grep']) assert.equal(isGated(tool), false, tool);
  for (const tool of ['bash', 'edit', 'write', 'powershell', 'mcp__server__tool']) assert.equal(isGated(tool), true, tool);
});

test('the menu renders the numbered approval shape SoluTerminal detects', () => {
  const lines = approvalMenuLines('bash');
  assert.deepEqual(lines, [
    'Tool approval: bash',
    '❯ 1. Yes, allow once',
    '  2. No, reject',
    '  3. Always allow bash',
    'Enter to approve · Esc to cancel',
  ]);
  // daemon/state.go genericMenuNeedles must not match our footer.
  const text = approvalPromptText('write').toLowerCase();
  for (const needle of ['enter to select', '↑/↓ to navigate', 'up/down to navigate', 'chat about this']) {
    assert.ok(!text.includes(needle), needle);
  }
  assert.equal(optionLabel('write', 0), 'Yes, allow once');
  assert.equal(optionLabel('write', 2), 'Always allow write');
  assert.equal(APPROVAL_OPTIONS.length, 3);
});

test('the custom component answers with keys and the daemon Enter write', () => {
  const decide = (keys) => {
    let answer;
    const component = approvalComponent('bash', (value) => { answer = value; });
    for (const key of keys) component.handleInput(key);
    return answer;
  };
  assert.equal(decide(['\r']), 'allow-once');      // focused option 1
  assert.equal(decide(['\n']), 'allow-once');
  assert.equal(decide(['1']), 'allow-once');
  assert.equal(decide(['2']), 'reject');
  assert.equal(decide(['n']), 'reject');
  assert.equal(decide(['\x1b']), 'reject');
  assert.equal(decide(['3']), 'allow-always');
  assert.equal(decide(['\x1b[B', '\x1b[B', '\r']), 'allow-always');
});

test('approve passes an allowlisted tool without a prompt', async (t) => {
  const file = tempFile(t);
  writeAllowlist(['bash'], file);
  let prompted = false;
  const result = await approve('bash', {
    hasUI: true,
    mode: 'tui',
    ui: { custom: async () => { prompted = true; return 'allow-once'; }, notify() {} },
  }, file);
  assert.equal(result.decision, 'allow-once');
  assert.equal(result.remembered, true);
  assert.equal(prompted, false);
});

test('approve fails closed without a UI', async (t) => {
  const result = await approve('bash', { hasUI: false, mode: 'print', ui: {} }, tempFile(t));
  assert.equal(result.decision, 'reject');
  assert.match(result.reason, /no UI is available/);
});

test('approve remembers an always-allow choice', async (t) => {
  const file = tempFile(t);
  const ctx = {
    hasUI: true,
    mode: 'tui',
    ui: {
      custom: async (factory) => new Promise((resolve) => {
        const component = factory({}, {}, {}, resolve);
        component.handleInput('3');
      }),
      notify() {},
    },
  };
  const result = await approve('edit', ctx, file);
  assert.equal(result.decision, 'allow-always');
  assert.deepEqual(readAllowlist(file), ['edit']);
});

test('simultaneous approvals are shown one at a time', async (t) => {
  const file = tempFile(t);
  let active = 0;
  let maxActive = 0;
  const ctx = {
    hasUI: true,
    mode: 'tui',
    ui: {
      custom: async (factory) => new Promise((resolve) => {
        active += 1;
        maxActive = Math.max(maxActive, active);
        const component = factory({}, {}, {}, (value) => {
          active -= 1;
          resolve(value);
        });
        setTimeout(() => component.handleInput('\r'), 5);
      }),
      notify() {},
    },
  };
  const [first, second] = await Promise.all([
    approve('bash', ctx, file),
    approve('edit', ctx, file),
  ]);
  assert.equal(first.decision, 'allow-once');
  assert.equal(second.decision, 'allow-once');
  assert.equal(maxActive, 1, 'approval menus overlapped');
});

const registered = (() => {
  let handler;
  let command;
  const pi = {
    on(name, fn) { if (name === 'tool_call') handler = fn; },
    registerCommand(name, options) { command = { name, options }; },
  };
  soluTerminalApproval(pi);
  return { handler, command };
})();

test('the extension registers a gate and an /allow command', () => {
  assert.equal(typeof registered.handler, 'function');
  assert.equal(registered.command.name, 'allow');
  assert.equal(typeof registered.command.options.handler, 'function');
});

test('the gate allows read-only calls and blocks unapproved mutating calls', async (t) => {
  const file = tempFile(t);
  const previous = process.env.PI_CODING_AGENT_DIR;
  process.env.PI_CODING_AGENT_DIR = path.dirname(file);
  t.after(() => {
    if (previous === undefined) delete process.env.PI_CODING_AGENT_DIR;
    else process.env.PI_CODING_AGENT_DIR = previous;
  });
  assert.equal(await registered.handler({ toolName: 'read' }, { hasUI: true }), undefined);
  const blocked = await registered.handler({ toolName: 'bash' }, { hasUI: false, mode: 'print', ui: {} });
  assert.equal(blocked.block, true);
  assert.match(blocked.reason, /SoluTerminal approval required/);
});

test('the extension records the per-pane session sidecar the daemon resumes from', (t) => {
  const root = fs.mkdtempSync(path.join(os.tmpdir(), 'solu-pi-sidecar-'));
  t.after(() => fs.rmSync(root, { recursive: true, force: true }));
  const env = { PI_CODING_AGENT_DIR: path.join(root, 'harness-state', 'pi'), SOLU_PANE_ID: 'pane_pi_1' };
  assert.equal(paneSessionDir(env), path.join(root, 'harness-state', 'pi', 'pane-sessions'));
  const file = recordPaneSession('sess-1', env);
  assert.equal(file, path.join(root, 'harness-state', 'pi', 'pane-sessions', 'pane_pi_1.json'));
  const saved = JSON.parse(fs.readFileSync(file, 'utf8'));
  assert.equal(saved.provider, 'pi');
  assert.equal(saved.sessionId, 'sess-1');
  assert.equal(fs.statSync(file).mode & 0o777, 0o600);
  // pi-glm derives its provider from the managed home basename.
  const glmFile = recordPaneSession('sess-2', { PI_CODING_AGENT_DIR: path.join(root, 'harness-state', 'pi-glm'), SOLU_PANE_ID: 'pane_pi_2' });
  assert.equal(JSON.parse(fs.readFileSync(glmFile, 'utf8')).provider, 'pi-glm');
  // No pane id / no session id → no record.
  assert.equal(recordPaneSession('', env), '');
  assert.equal(paneSessionFile('', env), '');
});

test('the session hook records the active Pi session id', (t) => {
  const root = fs.mkdtempSync(path.join(os.tmpdir(), 'solu-pi-sidecar-hook-'));
  const previousHome = process.env.PI_CODING_AGENT_DIR;
  const previousPane = process.env.SOLU_PANE_ID;
  process.env.PI_CODING_AGENT_DIR = path.join(root, 'harness-state', 'pi');
  process.env.SOLU_PANE_ID = 'pane_pi_hook';
  t.after(() => {
    fs.rmSync(root, { recursive: true, force: true });
    if (previousHome === undefined) delete process.env.PI_CODING_AGENT_DIR; else process.env.PI_CODING_AGENT_DIR = previousHome;
    if (previousPane === undefined) delete process.env.SOLU_PANE_ID; else process.env.SOLU_PANE_ID = previousPane;
  });
  const handlers = {};
  soluTerminalApproval({ on(name, fn) { handlers[name] = fn; }, registerCommand() {} });
  assert.equal(typeof handlers.session_start, 'function');
  handlers.session_start({}, { sessionManager: { getSessionId: () => 'sess-hook' } });
  const file = paneSessionFile('pane_pi_hook');
  assert.equal(JSON.parse(fs.readFileSync(file, 'utf8')).sessionId, 'sess-hook');
  // A later in-session switch rewrites the sidecar to the new id.
  handlers.turn_start({}, { sessionManager: { getSessionId: () => 'sess-next' } });
  assert.equal(JSON.parse(fs.readFileSync(file, 'utf8')).sessionId, 'sess-next');
});
