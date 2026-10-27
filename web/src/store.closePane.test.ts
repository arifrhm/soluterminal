import { afterEach, beforeEach, expect, it, vi } from 'vitest';
import { useStore } from './store';

beforeEach(() => vi.useFakeTimers());
afterEach(() => { vi.clearAllTimers(); vi.useRealTimers(); });

it.each([true, false])('closing an externally removed zoomed pane reveals its survivor (focused=%s)', (focused) => {
  useStore.setState({
    panes: {}, // external deletion already removed the pane record
    layout: { type: 'split', direction: 'horizontal', ratio: .5,
      a: { type: 'pane', paneId: 'dead' }, b: { type: 'pane', paneId: 'alive' } },
    focusedPaneId: focused ? 'dead' : 'alive', zoomedPaneId: 'dead', lastUserFocusId: 'dead',
  });
  useStore.getState().closePaneInLayout('dead');
  expect(useStore.getState()).toMatchObject({
    layout: { type: 'pane', paneId: 'alive' }, focusedPaneId: 'alive', zoomedPaneId: null, lastUserFocusId: null,
  });
});

it('closing an unzoomed pane preserves zoom on its survivor', () => {
  useStore.setState({ layout: { type:'split', direction:'horizontal', ratio:.5,
    a:{type:'pane', paneId:'dead'}, b:{type:'pane', paneId:'alive'} },
    focusedPaneId:'alive', zoomedPaneId:'alive', lastUserFocusId:'alive' });
  useStore.getState().closePaneInLayout('dead');
  expect(useStore.getState()).toMatchObject({ focusedPaneId:'alive', zoomedPaneId:'alive', lastUserFocusId:'alive' });
});
