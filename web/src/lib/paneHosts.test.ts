import { afterEach, describe, expect, it } from 'vitest';
import { acquirePaneHost, claimPaneHostPlacement, parkPaneHost, releasePaneHost } from './paneHosts';

function parkEvents() {
  const seen: string[] = [];
  const on = (e: Event) => seen.push((e as CustomEvent<{ paneId: string }>).detail.paneId);
  window.addEventListener('solu:pane-park', on);
  return { seen, stop: () => window.removeEventListener('solu:pane-park', on) };
}

afterEach(() => {
  releasePaneHost('p1');
  document.body.innerHTML = '';
});

describe('parkPaneHost', () => {
  it('reports a pane that stays parked', async () => {
    const slot = document.body.appendChild(document.createElement('div'));
    slot.appendChild(acquirePaneHost('p1'));
    const events = parkEvents();
    parkPaneHost('p1', claimPaneHostPlacement('p1'));
    await Promise.resolve();
    events.stop();
    expect(events.seen).toEqual(['p1']);
  });

  // Opening or closing a tile re-nests the layout: the old slot parks the
  // host and the new slot adopts it in the same commit. Reporting that as
  // off-screen made the daemon resize the PTY to another viewer and back.
  it('says nothing when the host is re-adopted in the same commit', async () => {
    const before = document.body.appendChild(document.createElement('div'));
    const after = document.body.appendChild(document.createElement('div'));
    const host = acquirePaneHost('p1');
    before.appendChild(host);
    const events = parkEvents();
    parkPaneHost('p1', claimPaneHostPlacement('p1'));
    after.appendChild(host);
    await Promise.resolve();
    events.stop();
    expect(events.seen).toEqual([]);
  });
});
