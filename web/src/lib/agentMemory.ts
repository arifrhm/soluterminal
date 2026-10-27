import type { PaneInfo } from '../types';

type PaneMemoryFields = Pick<PaneInfo, 'agentRssMB' | 'claudeRssMB'>;

// New daemons report provider-neutral agentRssMB. Fall back to the historical
// Claude-named field so a freshly loaded UI remains compatible during daemon
// updates and with older remote backends.
export function paneAgentRssMB(pane: PaneMemoryFields): number {
  return pane.agentRssMB ?? pane.claudeRssMB ?? 0;
}
