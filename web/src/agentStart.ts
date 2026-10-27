import { create } from 'zustand';
import { api } from './api';
import { useStore } from './store';
import type { Health } from './types';
import { getDaemonHealthState, refreshDaemonHealth } from './lib/useDaemonHealth';
import { activeBackendFromUrl, isRouterMode } from './dualBackend';
import { markSetupStarted } from './lib/setupActivity';

// "Start Claude Code" / "Start Codex" from the empty window and the Ctrl+K
// palette. They check first: an agent that isn't installed opens its install
// card (AgentInstallPrompt) instead of a pane that dies on "command not found".
// While the daemon hasn't answered yet they start as before, so a slow first
// probe never blocks anyone.

export type StartableAgent = 'claude' | 'codex';
export type AgentReadiness = 'unknown' | 'missing' | 'signed-out' | 'ready';

export const AGENT_NAMES: Record<StartableAgent, string> = { claude: 'Claude Code', codex: 'Codex' };

// How long a fresh probe may take before the cached verdict stands.
export const START_RECHECK_TIMEOUT_MS = 4_000;

export function agentReadiness(
  agent: StartableAgent,
  health: Health | null,
  activeBackend: string | null = null,
): AgentReadiness {
  if (!health) return 'unknown';
  // Dual mode: the shared health can briefly hold the other side's answer.
  if (activeBackend && health.backendID && health.backendID !== activeBackend) return 'unknown';
  const checked = agent === 'claude' ? health.claudeChecked : health.codexChecked;
  const installed = agent === 'claude' ? health.claudeInstalled : health.codexInstalled;
  const authed = agent === 'claude' ? health.claudeAuthed : health.codexAuthed;
  if (checked !== true) return 'unknown';
  if (installed === false) return 'missing';
  if (authed === false) return 'signed-out';
  return 'ready';
}

function currentBackend(): string | null {
  if (!isRouterMode()) return null;
  return useStore.getState().activeBackend ?? activeBackendFromUrl();
}

// The cached probe can be minutes old, so a "missing" verdict is re-checked
// once: someone who just installed the agent in a terminal shouldn't be sent
// to the installer. Older daemons and the phone relay keep the cached verdict.
async function freshlyMissing(agent: StartableAgent): Promise<boolean> {
  const recheck = agent === 'claude'
    ? api.claudeRecheck().then((s) => s.claudeChecked && !s.claudeInstalled)
    : api.codexRecheck().then((s) => s.codexChecked && !s.codexInstalled);
  const timeout = new Promise<boolean>((resolve) => window.setTimeout(() => resolve(true), START_RECHECK_TIMEOUT_MS));
  try {
    const missing = await Promise.race([recheck, timeout]);
    if (!missing) refreshDaemonHealth();
    return missing;
  } catch {
    return true;
  }
}

interface AgentInstallPromptState {
  agent: StartableAgent | null;
  show: (agent: StartableAgent) => void;
  close: () => void;
}

export const useAgentInstallPrompt = create<AgentInstallPromptState>((set) => ({
  agent: null,
  show: (agent) => set({ agent }),
  close: () => set({ agent: null }),
}));

export function agentPaneOptions(agent: StartableAgent) {
  return agent === 'codex'
    ? { cwd: '', cmd: ['codex'], agent: 'codex' as const, title: 'codex' }
    : { cwd: '', cmd: ['claude'], title: 'claude' };
}

/**
 * Starts the agent in a new pane, or opens its install card when it isn't
 * installed. Rejects when the pane can't be created (callers toast it).
 */
export async function startAgent(agent: StartableAgent): Promise<'started' | 'install'> {
  const readiness = agentReadiness(agent, getDaemonHealthState().health, currentBackend());
  if (readiness === 'missing' && (await freshlyMissing(agent))) {
    useAgentInstallPrompt.getState().show(agent);
    return 'install';
  }
  // The agent's own sign-in runs in the pane. Counts as a setup step so the
  // one-time consent question waits until it's done.
  if (readiness === 'signed-out') markSetupStarted();
  await useStore.getState().createPane(agentPaneOptions(agent));
  if (readiness === 'signed-out') {
    useStore.getState().toast(`${AGENT_NAMES[agent]} will ask you to sign in first. Follow the steps in the new pane.`, {
      kind: 'info',
      ttlMs: 9000,
    });
  }
  return 'started';
}
