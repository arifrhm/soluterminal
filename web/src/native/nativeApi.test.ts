// Unit tests for the native allowlist API helpers. These guard the WIRE CONTRACT
// the daemon implements: GET /native/allowlist → {tools}, and POST
// /native/allowlist body {tool, action} → {tools} (updated list). The helpers
// are thin wrappers over the `api` object, so we mock `api` and assert the right
// method is called with the right arguments and the {tools} shape round-trips.

import { describe, it, expect, vi, beforeEach } from 'vitest';

const nativeAllowlist = vi.fn();
const nativeSetAllowlist = vi.fn();

vi.mock('../api', () => ({
  api: {
    nativeAllowlist: (...args: unknown[]) => nativeAllowlist(...args),
    nativeSetAllowlist: (...args: unknown[]) => nativeSetAllowlist(...args),
  },
}));

import { getAllowlist, setAllowlist } from './nativeApi';

describe('native allowlist API helpers', () => {
  beforeEach(() => {
    nativeAllowlist.mockReset();
    nativeSetAllowlist.mockReset();
  });

  it('getAllowlist reads the pane list and returns {tools}', async () => {
    nativeAllowlist.mockResolvedValue({ tools: ['Read', 'Grep'] });
    const res = await getAllowlist('pane-1');
    expect(nativeAllowlist).toHaveBeenCalledWith('pane-1');
    expect(res).toEqual({ tools: ['Read', 'Grep'] });
  });

  it('setAllowlist add posts {tool, action:"add"} and returns the updated list', async () => {
    nativeSetAllowlist.mockResolvedValue({ tools: ['Read', 'Write'] });
    const res = await setAllowlist('pane-1', 'Write', 'add');
    expect(nativeSetAllowlist).toHaveBeenCalledWith('pane-1', 'Write', 'add');
    expect(res.tools).toEqual(['Read', 'Write']);
  });

  it('setAllowlist remove posts {tool, action:"remove"} and returns the updated list', async () => {
    nativeSetAllowlist.mockResolvedValue({ tools: ['Read'] });
    const res = await setAllowlist('pane-1', 'Write', 'remove');
    expect(nativeSetAllowlist).toHaveBeenCalledWith('pane-1', 'Write', 'remove');
    expect(res.tools).toEqual(['Read']);
  });
});
