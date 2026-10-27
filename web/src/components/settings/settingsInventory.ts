import type { SettingsCategory } from '../../settingsRegistry';

/**
 * Hand-written snapshot of visible labels in the historical nine-tab dialog.
 * This must not be derived from settingsRegistry: registry refactors must not
 * be able to make the zero-setting-loss check pass by construction.
 */
export const SETTINGS_INVENTORY: Record<SettingsCategory, string[]> = {
  appearance: [
    'App theme', 'Color theme', 'Font size', 'Line height', 'Focus text offset', 'Sidebar text size',
  ],
  workspace: [
    'Pane layout', 'Arrange', 'Cols', 'Rows', 'Grid', 'Left+stack', 'Copy on select',
    'Middle-click paste', 'Name new agent panes with AI', 'Sidebar sections', 'Todos', 'Files', 'Localhost ports',
    'Detached sessions', 'Started by agents', 'Credentials', 'Automations', 'Agents', 'MCP servers', 'Reset to default',
  ],
  sessions: [
    'New Claude sessions', 'After a restart', 'Suggest next prompt (Focus)',
    'Show prompt history strip', 'Focus vitals', 'Model', 'Context %', 'Tokens', 'Cost',
    'Cache', 'Resets', 'Working dir',
  ],
  workflows: [
    'Workflow reasoning effort', 'Review credentials', 'Advanced role routing',
    'Local OpenAI-compatible', 'Ask before a task switches model mid-run (beta)',
    'Default fallback policy', 'Consented routes', 'Auto cost ratio limit',
    'Circuit breaker (3 typed failures in 10 minutes benches a runtime until a probe succeeds)',
    'Needs-attention digest', 'Escalation max wait (minutes)',
    'Only announce informational updates with your next message', 'Live status narration',
    'Narrate after quiet minutes', 'Approval level', 'Veto window (minutes)', 'Hourly limit',
    'Proposal autopilot', 'Proposal veto window (minutes)', 'Proposal hourly limit',
    'Gate policies', 'Stop everything', 'Enable board janitor',
    'Auto-resolve superseded failures', 'Repoint dependents to refiles',
    'Proposed cancels with veto window', 'Cancel veto window (minutes)',
    'Auto-publish on cadence (release train)', 'Functional landings before a train',
    'Max unreleased age (hours)', 'Only depart while the engine is quiet',
    'Enable automatic triage recommendations', 'Max turns per triage call',
    'Max automatic triage runs per task', 'Auto-apply safe triage recommendations',
    'Max automatic resumes per task', 'Max automatic actions per 10 minutes',
    'Transient infrastructure failures (connection lost, timeouts)', 'Quota-parked resume',
    'Re-land after dirty-tree failure',
    'Spec-stage spawn failures (worker died at startup) — auto-retry once',
    'Provider safeguard refusal — retry same prompt once',
    'Escalate the executor on repeated repair rounds', 'Adaptive heavy controller',
    'Heavy cap floor', 'Adaptive controller evidence', 'Max concurrent tasks',
    'Max heavy operations', 'Max concurrent test suites', 'Max agent runs',
    'Reserved bookkeeping runs', 'Memory floor', 'Disk floor', 'Host disk floor (WSL)',
    'Host disk floor %', 'Host disk hysteresis', 'Browser host reserve', 'Current storage',
    'Finished-task data', 'Last sweep', 'Automatically clean up terminal task worktrees',
    'Failure grace period', 'Finished-task retention', 'Manual cleanup', 'Finished-task cleanup',
  ],
  agents: [
    'Enable the Warden', 'Automatic pane flags', 'Model', 'Triage after', 'Max runs / hour', 'Run a fleet brief now',
    'Agent key', 'Answer mentions from this machine', 'Permission', 'Answer with', 'Remove',
    'Add agent',
  ],
  credentials: [
    'Provider accounts', 'Runtime API keys', 'Auto-switch', 'Team pool health',
    'Hide identities (screen share)',
  ],
  team: ['Team pool'],
  devices: [
    'Desktop app', 'Phone access', 'Launch on startup', 'Automatic updates',
    'Desktop app restarts', 'Send an anonymous daily check-in',
    'Let Claude Code and Codex use SoluTerminal',
  ],
};
