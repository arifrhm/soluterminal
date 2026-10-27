import { describe, expect, it } from 'vitest';
import { paneAgentRssMB } from './agentMemory';

describe('pane agent memory compatibility', () => {
  it('uses provider-neutral memory for Codex panes', () => {
    expect(paneAgentRssMB({ agentRssMB: 384, claudeRssMB: 99 })).toBe(384);
  });

  it('falls back to the legacy Claude-named wire field', () => {
    expect(paneAgentRssMB({ claudeRssMB: 512 })).toBe(512);
    expect(paneAgentRssMB({})).toBe(0);
  });
});
