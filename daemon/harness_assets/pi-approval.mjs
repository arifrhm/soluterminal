/**
 * SoluTerminal managed approval gate for Pi.
 *
 * Pi ships no built-in tool-permission system (its own docs: "No permission
 * popups", and `docs/security.md` calls project trust an input-loading guard
 * rather than a sandbox). Unlike the DeepSeek client, which renders its own
 * approval menu, a managed Pi pane runs Pi's own TUI, so without this
 * extension SoluTerminal has nothing to approve — per-pane AutoMode, the
 * attention inbox, and the "auto" chip are all inert.
 *
 * This extension restores the missing gate. It intercepts every mutating
 * `tool_call` and renders the SAME numbered, ❯-focused menu shape the daemon
 * already recognizes for Claude, Codex, and DeepSeek
 * (`daemon/state.go` choicePromptRE + approvalOptionRE). That is the whole
 * contract: a bare Enter (option 1 focused) is a valid "Yes, allow once"
 * answer, so `always` AutoMode auto-approves and `cautious` AutoMode can send
 * its low-risk decision through the existing send_input path.
 *
 * Loading is explicit (`--extension <path>`) from the managed launcher only,
 * so a user's own `~/.pi` and unmanaged `pi` runs are untouched.
 */
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';

// Read-only built-ins never prompt: they cannot change state, and gating them
// would make Pi unusable. Everything else (bash/edit/write/powershell, MCP,
// custom tools) is a permission gate.
export const READ_ONLY_TOOLS = Object.freeze(['read', 'ls', 'find', 'grep']);
const READ_ONLY = new Set(READ_ONLY_TOOLS);

// The prompt's escape hatch and "grant" wording are load-bearing for the
// daemon classifier: option 1 must be numbered and start with "Yes," and at
// least one option must contain "No"/"cancel". Do not reword without updating
// daemon/state.go's approvalPromptMatches tests.
export const APPROVAL_TITLE_PREFIX = 'Tool approval:';

// The persisted "Always allow" set lives beside the managed Pi home, the same
// terminal-allow.json convention the DeepSeek client uses.
export function allowlistFile(env = process.env) {
  const home = String(env.PI_CODING_AGENT_DIR || '').trim() || path.join(os.homedir(), '.pi', 'agent');
  return path.join(home, 'terminal-allow.json');
}

export function readAllowlist(file = allowlistFile()) {
  try {
    const parsed = JSON.parse(fs.readFileSync(file, 'utf8'));
    if (!Array.isArray(parsed)) return [];
    return [...new Set(parsed.filter(entry => typeof entry === 'string' && entry.trim()).map(entry => entry.trim()))];
  } catch {
    return [];
  }
}

export function writeAllowlist(tools, file = allowlistFile()) {
  const unique = [...new Set((Array.isArray(tools) ? tools : [])
    .filter(entry => typeof entry === 'string' && entry.trim())
    .map(entry => entry.trim()))].sort();
  try {
    fs.mkdirSync(path.dirname(file), { recursive: true, mode: 0o700 });
    const temp = `${file}.${process.pid}.tmp`;
    fs.writeFileSync(temp, JSON.stringify(unique, null, 2) + '\n', { mode: 0o600 });
    fs.renameSync(temp, file);
  } catch {
    // Best effort: a failed persist degrades to prompting again, never to
    // silently allowing.
  }
  return unique;
}

// Per-pane durable session pointer — the Pi twin of the DeepSeek client's
// recordPaneSession. The daemon exports SOLU_PANE_ID into every pane, the
// managed launcher gives each row its own PI_CODING_AGENT_DIR, and this
// extension records the active Pi session id there. On a restart the daemon
// adopts it (harness-state/<row>/pane-sessions/<paneId>.json) and replays
// `--session <id>` via SOLU_HARNESS_RESUME_SESSION (launch.mjs piPlanFor), so a
// managed Pi pane resumes its conversation like Claude/Codex/DeepSeek.
export function paneSessionDir(env = process.env) {
  const home = String(env.PI_CODING_AGENT_DIR || '').trim();
  return home ? path.join(home, 'pane-sessions') : '';
}

export function paneSessionFile(paneId, env = process.env) {
  const dir = paneSessionDir(env);
  const id = String(paneId || '').trim();
  if (!dir || !id) return '';
  const safe = id.replace(/[^A-Za-z0-9_-]/g, '_').slice(0, 80);
  return safe ? path.join(dir, `${safe}.json`) : '';
}

export function recordPaneSession(sessionId, env = process.env) {
  const paneId = String(env.SOLU_PANE_ID || '').trim();
  const file = paneSessionFile(paneId, env);
  const id = String(sessionId || '').trim();
  if (!file || !id) return '';
  // 'pi' or 'pi-glm' — matches the daemon's harnessSessionDirs provider key.
  const provider = path.basename(String(env.PI_CODING_AGENT_DIR || '').trim()) || 'pi';
  try {
    fs.mkdirSync(path.dirname(file), { recursive: true, mode: 0o700 });
    const temp = `${file}.${process.pid}.tmp`;
    fs.writeFileSync(temp, JSON.stringify({ provider, sessionId: id, cwd: process.cwd(), updatedAt: Date.now() }), { mode: 0o600 });
    fs.renameSync(temp, file);
  } catch {
    // Best effort: a failed persist degrades to no resume, never a broken start.
  }
  return file;
}

export function isGated(toolName) {
  return !READ_ONLY.has(String(toolName ?? ''));
}

export const APPROVAL_OPTIONS = Object.freeze(['allow-once', 'reject', 'allow-always']);

export function optionLabel(toolName, index) {
  switch (index) {
    case 0: return 'Yes, allow once';
    case 1: return 'No, reject';
    default: return `Always allow ${toolName}`;
  }
}

// approvalMenuLines is the exact text the pane shows. The daemon scrapes this
// from the terminal grid, so it must stay parseable: focused option 1 starts
// with "❯ 1.", every option is "N. label", and the footer deliberately avoids
// the daemon's generic-menu needles ("Enter to select", "↑/↓ to navigate").
export function approvalMenuLines(toolName, focusedIndex = 0) {
  const lines = [`${APPROVAL_TITLE_PREFIX} ${toolName}`];
  for (let i = 0; i < APPROVAL_OPTIONS.length; i++) {
    lines.push(`${i === focusedIndex ? '❯' : ' '} ${i + 1}. ${optionLabel(toolName, i)}`);
  }
  lines.push('Enter to approve · Esc to cancel');
  return lines;
}

export function approvalPromptText(toolName) {
  return approvalMenuLines(toolName).join('\n');
}

// approvalComponent is a dependency-free ctx.ui.custom component: render()
// returns the parseable menu, handleInput() maps keys (and the daemon's PTY
// writes) to a decision.
export function approvalComponent(toolName, done) {
  let focused = 0;
  let cached = null;
  const finish = decision => done(decision);
  return {
    render() {
      if (!cached) cached = approvalMenuLines(toolName, focused);
      return cached;
    },
    invalidate() { cached = null; },
    handleInput(data) {
      if (data === '\x1b[A' || data === 'k') { focused = Math.max(0, focused - 1); cached = null; return; }
      if (data === '\x1b[B' || data === 'j') { focused = Math.min(APPROVAL_OPTIONS.length - 1, focused + 1); cached = null; return; }
      if (data === '\r' || data === '\n') { finish(APPROVAL_OPTIONS[focused]); return; }
      if (data === '\x1b') { finish('reject'); return; }
      if (data === '1') { finish('allow-once'); return; }
      if (data === '2' || data === 'n' || data === 'N') { finish('reject'); return; }
      if (data === '3' || data === 'a' || data === 'A') { finish('allow-always'); return; }
    },
  };
}

// Parallel tool calls from one assistant message would otherwise open several
// menus over each other. Chain the interactive prompts so they are shown one
// at a time (the DeepSeek client queues its simultaneous approvals the same
// way). The allowlist fast path stays outside the lock.
let promptChain = Promise.resolve();
function withPromptLock(task) {
  const run = promptChain.then(task, task);
  promptChain = run.then(() => undefined, () => undefined);
  return run;
}

async function showApproval(toolName, ctx) {
  if (ctx.mode === 'tui' && typeof ctx.ui?.custom === 'function') {
    return ctx.ui.custom((_tui, _theme, _keybindings, done) => approvalComponent(toolName, done));
  }
  const choice = await ctx.ui.select(approvalPromptText(toolName), APPROVAL_OPTIONS.map((_, i) => optionLabel(toolName, i)));
  return choice === optionLabel(toolName, 0) ? 'allow-once'
    : choice === optionLabel(toolName, 2) ? 'allow-always'
      : 'reject';
}

// approve resolves one gated call. Shared by the tool_call handler and tests:
// allowlisted tools pass, an interactive pane gets the menu, and anything
// without a UI fails closed.
export async function approve(toolName, ctx, file = allowlistFile()) {
  if (readAllowlist(file).includes(toolName)) {
    safeNotify(ctx, `✔ ${toolName} allowed automatically (SoluTerminal)`);
    return { decision: 'allow-once', remembered: true };
  }
  if (!ctx?.hasUI) {
    return { decision: 'reject', reason: `SoluTerminal approval required for ${toolName}, but no UI is available` };
  }
  const decision = await withPromptLock(() => showApproval(toolName, ctx));
  if (decision === 'allow-always') {
    writeAllowlist([...readAllowlist(file), toolName], file);
    return { decision: 'allow-always', remembered: false };
  }
  return { decision: decision === 'allow-once' ? 'allow-once' : 'reject', remembered: false };
}

function safeNotify(ctx, message, type = 'info') {
  try { ctx?.ui?.notify?.(message, type); } catch { /* notification is best-effort */ }
}

export default function soluTerminalApproval(pi) {
  // Record this pane's Pi session id so a pane-agent restart can resume it.
  // session_start covers new/resume/fork; turn_start is the safety net for a
  // session switch that never announced a start. Deduped so a long session does
  // not rewrite the sidecar every turn.
  let lastSessionId = '';
  const rememberSession = ctx => {
    try {
      const id = String(ctx?.sessionManager?.getSessionId?.() || '').trim();
      if (!id || id === lastSessionId) return;
      lastSessionId = id;
      recordPaneSession(id);
    } catch { /* best-effort; never block a turn */ }
  };
  pi.on('session_start', (_event, ctx) => { rememberSession(ctx); });
  pi.on('session_info_changed', (_event, ctx) => { rememberSession(ctx); });
  pi.on('turn_start', (_event, ctx) => { rememberSession(ctx); });

  pi.on('tool_call', async (event, ctx) => {
    const toolName = String(event?.toolName ?? '').trim();
    if (!toolName || !isGated(toolName)) return undefined;
    let result;
    try {
      result = await approve(toolName, ctx);
    } catch (error) {
      return { block: true, reason: `SoluTerminal approval gate failed closed for ${toolName}: ${String(error?.message ?? error)}` };
    }
    if (result.decision === 'allow-once' || result.decision === 'allow-always') return undefined;
    return { block: true, reason: result.reason || `Blocked in SoluTerminal: ${toolName} was not approved` };
  });

  // Parity with the DeepSeek client's /allow. Lists the remembered tools or
  // clears them.
  pi.registerCommand?.('allow', {
    description: 'List or clear SoluTerminal always-allow tools (/allow clear)',
    handler: async (args, ctx) => {
      const arg = String(args ?? '').trim().toLowerCase();
      if (arg === 'clear' || arg === 'reset') {
        writeAllowlist([]);
        safeNotify(ctx, 'SoluTerminal always-allow list cleared');
        return;
      }
      const tools = readAllowlist();
      safeNotify(ctx, tools.length ? `Always allow: ${tools.join(', ')}` : 'No always-allow tools yet. Choose "3. Always allow" at an approval.');
    },
  });
}
