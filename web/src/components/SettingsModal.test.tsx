import { act, fireEvent, render, screen } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

const taskTabState = vi.hoisted(() => ({ shouldThrow: false }));

vi.mock('./sidebar/SidebarCustomizer', () => ({
  SidebarCustomizer: () => <div>SIDEBAR_CUSTOMIZER</div>,
}));
vi.mock('./settings/ServicesTab', () => ({
  ServicesTab: () => {
    if (taskTabState.shouldThrow) throw new Error('boom');
    return <div>SERVICES_TAB</div>;
  },
}));

import { useSettings } from '../settings';
import { SettingsModal } from './SettingsModal';
import { useEntitlements } from '../entitlements';
import { setExtensionsForTests } from '../extensions';

describe('SettingsModal', () => {
  beforeEach(() => {
    taskTabState.shouldThrow = false;
    useEntitlements.setState({ loaded: true, team: true, operator: true, teamCredentials: true });
    useSettings.setState({ open: false, tab: 'appearance', section: null, modelRole: null });
  });
  afterEach(() => {
    setExtensionsForTests(null);
    useSettings.setState({ open: false, tab: 'appearance', section: null, modelRole: null });
  });

  it('renders nothing while closed', () => {
    render(<SettingsModal />);
    expect(screen.queryByText('Settings')).not.toBeInTheDocument();
  });

  it('opens on Appearance by default with the full nav rail', () => {
    render(<SettingsModal />);
    act(() => useSettings.getState().openSettings());
    expect(screen.getByRole('tab', { name: 'Appearance' })).toHaveAttribute(
      'aria-selected',
      'true',
    );
    for (const label of ['Appearance', 'Terminal & panes', 'Sidebar', 'Devices', 'Warden', 'Services', 'Aria agent']) {
      expect(screen.getByRole('tab', { name: label })).toBeInTheDocument();
    }
    expect(screen.getByText('Color theme')).toBeInTheDocument();
    expect(screen.queryByRole('tab', { name: 'Claude sessions' })).toBeNull();
  });

  it('lands old Claude sessions links on Terminal & panes', () => {
    render(<SettingsModal />);
    act(() => useSettings.getState().openSettings('claude'));
    expect(screen.getByRole('tab', { name: 'Terminal & panes' })).toHaveAttribute('aria-selected', 'true');
    expect(screen.getByText('After a restart')).toBeInTheDocument();
  });

  it('hides the Tasks, Aria agent, Credentials and Warden tabs for a public install', () => {
    useEntitlements.setState({ loaded: true, team: false, operator: false, teamCredentials: false });
    render(<SettingsModal />);
    act(() => useSettings.getState().openSettings('tasks', 'tasks.modelsFallbacks'));
    for (const label of ['Services', 'Aria agent', 'Credentials', 'Team pool', 'Warden']) {
      expect(screen.queryByRole('tab', { name: label })).toBeNull();
    }
    // A deep link to a hidden tab lands on the first visible one.
    expect(screen.getByRole('tab', { name: 'Appearance' })).toHaveAttribute('aria-selected', 'true');
    expect(screen.queryByText('SERVICES_TAB')).toBeNull();
  });

  it('deep-links straight to the requested tab', () => {
    render(<SettingsModal />);
    act(() => useSettings.getState().openSettings('sidebar'));
    expect(screen.getByRole('tab', { name: 'Sidebar' })).toHaveAttribute('aria-selected', 'true');
    expect(screen.getByText('SIDEBAR_CUSTOMIZER')).toBeInTheDocument();
  });

  it('places a registered feature tab after its core tab, while it is visible', () => {
    const visible = { on: true };
    setExtensionsForTests([{
      settingsTabs: [{
        key: 'team', label: 'Feature tab', after: 'sidebar',
        useVisible: () => visible.on, Component: () => <div>FEATURE_TAB</div>,
      }],
    }]);
    const { unmount } = render(<SettingsModal />);
    act(() => useSettings.getState().openSettings());
    const labels = screen.getAllByRole('tab').map((t) => t.textContent);
    expect(labels.indexOf('Feature tab')).toBe(labels.indexOf('Sidebar') + 1);
    fireEvent.click(screen.getByRole('tab', { name: 'Feature tab' }));
    expect(screen.getByText('FEATURE_TAB')).toBeInTheDocument();
    unmount();

    visible.on = false;
    render(<SettingsModal />);
    expect(screen.queryByRole('tab', { name: 'Feature tab' })).toBeNull();
    expect(screen.queryByText('FEATURE_TAB')).toBeNull();
  });

  it('remembers the last-viewed tab across close/reopen', () => {
    render(<SettingsModal />);
    act(() => useSettings.getState().openSettings());
    fireEvent.click(screen.getByRole('tab', { name: 'Terminal & panes' }));
    fireEvent.keyDown(window, { key: 'Escape' });
    act(() => useSettings.getState().openSettings());
    expect(screen.getByRole('tab', { name: 'Terminal & panes' })).toHaveAttribute(
      'aria-selected',
      'true',
    );
  });

  it('closes on Escape', () => {
    render(<SettingsModal />);
    act(() => useSettings.getState().openSettings());
    expect(screen.getByText('Settings')).toBeInTheDocument();
    fireEvent.keyDown(window, { key: 'Escape' });
    expect(screen.queryByText('Settings')).not.toBeInTheDocument();
  });

  it('contains a throwing tab section and recovers when switching tabs', () => {
    const spy = vi.spyOn(console, 'error').mockImplementation(() => {});
    taskTabState.shouldThrow = true;
    useSettings.setState({ open: true, tab: 'tasks' });

    render(<SettingsModal />);

    expect(screen.getByText('This settings section failed to load')).toBeInTheDocument();
    expect(screen.getByRole('dialog', { name: 'Settings' })).toBeInTheDocument();
    expect(screen.getByRole('tab', { name: 'Services' })).toBeInTheDocument();

    fireEvent.click(screen.getByRole('tab', { name: 'Appearance' }));
    expect(screen.queryByText('This settings section failed to load')).not.toBeInTheDocument();
    expect(screen.getByText('Color theme')).toBeInTheDocument();
    spy.mockRestore();
  });

  it('drags by the header, tracking the pointer 1:1', () => {
    const { container } = render(<SettingsModal />);
    act(() => useSettings.getState().openSettings());
    const header = container.querySelector('.settings-drag') as HTMLElement;
    const card = container.querySelector('.settings-modal') as HTMLElement;
    // jsdom rects are all-zero, so the card pins at (0,0); off = (100,100).
    fireEvent.pointerDown(header, { button: 0, pointerId: 1, clientX: 100, clientY: 100 });
    fireEvent.pointerMove(header, { pointerId: 1, clientX: 150, clientY: 140 });
    expect(card.style.left).toBe('50px');
    expect(card.style.top).toBe('40px');
    fireEvent.pointerUp(header, { pointerId: 1 });
    // After release, further moves must not drag.
    fireEvent.pointerMove(header, { pointerId: 1, clientX: 300, clientY: 300 });
    expect(card.style.left).toBe('50px');
  });

  it('does not start a drag from the close button', () => {
    const { container } = render(<SettingsModal />);
    act(() => useSettings.getState().openSettings());
    const closeBtn = screen.getByRole('button', { name: 'Close' });
    const card = container.querySelector('.settings-modal') as HTMLElement;
    fireEvent.pointerDown(closeBtn, { button: 0, pointerId: 2, clientX: 100, clientY: 100 });
    fireEvent.pointerMove(closeBtn, { pointerId: 2, clientX: 200, clientY: 200 });
    expect(card.style.left).toBe('0px');
    expect(card.style.top).toBe('0px');
  });
});
