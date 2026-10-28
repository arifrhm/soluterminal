
import { useEffect } from 'react';
import { useSettings, type SettingsTab } from '../settings';
import { useOperatorFeatures } from '../entitlements';
import { slotList } from '../extensions';
import { useDraggableDialog } from './useDraggableDialog';
import { useFocusTrap } from './useFocusTrap';
import { useIsMobileDevice } from './useIsMobileView';
import { SidebarCustomizer } from './sidebar/SidebarCustomizer';
import { AppearanceTab } from './settings/AppearanceTab';
import { PanesTab } from './settings/PanesTab';
import { DevicesTab } from './settings/DevicesTab';
import { PhoneEncryptionSetting, privateRelayCandidateInView } from './settings/RemoteDevicesSetting';
import { WardenTab } from './settings/WardenTab';
import { ServicesTab } from './settings/ServicesTab';
import { ErrorBoundary } from './ErrorBoundary';

// THE Settings dialog — every settings surface lives here as a tab on the
// left nav rail, and every entry point (topbar gear, sidebar right-click
// "Customize sidebar…", credentials "Manage team pool…") deep-links to its
// tab via useSettings.openSettings(tab). All controls apply live; the window
// drags by its header and resizes from the corner grip (useDraggableDialog
// pins it to fixed coordinates so both track the cursor 1:1).
// Voice and Alerts stay as small anchored topbar popovers — quick toggles,
// not settings pages. HotkeyHelp stays a reference overlay.

const TABS: { key: SettingsTab; label: string }[] = [
  { key: 'appearance', label: 'Appearance' },
  { key: 'panes', label: 'Terminal & panes' },
  { key: 'sidebar', label: 'Sidebar' },
  { key: 'devices', label: 'Devices' },
  { key: 'warden', label: 'Warden' },
  { key: 'tasks', label: 'Services' },
];

// Tabs for team-only surfaces: the Tasks engine and the Warden caretaker.
// Feature modules add their own tabs (extensions.ts).
const TEAM_ONLY_TABS = new Set<SettingsTab>(['tasks', 'warden']);

export function SettingsModal() {
  const open = useSettings((s) => s.open);
  const tab = useSettings((s) => s.tab);
  const setTab = useSettings((s) => s.setTab);
  const close = useSettings((s) => s.closeSettings);
  const isMobile = useIsMobileDevice();
  const team = useOperatorFeatures();
  // Tabs registered by optional features (extensions.ts), each placed after
  // a core tab. The list is fixed for the page's lifetime, so the hook order
  // is stable.
  const featureTabs = slotList('settingsTabs');
  const featureVisible = featureTabs.map((t) => t.useVisible());

  const { cardRef, cardStyle, dragHandleProps } = useDraggableDialog(open);
  useFocusTrap(cardRef, open);

  useEffect(() => {
    if (!open) return;
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape') close();
    };
    window.addEventListener('keydown', onKey);
    return () => window.removeEventListener('keydown', onKey);
  }, [open, close]);

  if (!open) return null;
  // Devices holds the desktop-installer / phone-pairing rows; neither applies
  // on a phone, so there the tab shows only this browser's end-to-end
  // encryption switch, and only when that applies.
  const phoneEncryption = isMobile && privateRelayCandidateInView() !== null;
  const coreTabs = TABS.filter((t) => (t.key !== 'devices' || !isMobile || phoneEncryption)
    && (team || !TEAM_ONLY_TABS.has(t.key)));
  const tabs: { key: SettingsTab; label: string }[] = [];
  const placed = new Set<number>();
  // A feature tab follows its anchor, which may be another feature's tab;
  // one whose anchor isn't shown goes at the end.
  const placeAfter = (key: SettingsTab) => {
    featureTabs.forEach((f, i) => {
      if (f.after !== key || !featureVisible[i] || placed.has(i)) return;
      placed.add(i);
      tabs.push(f);
      placeAfter(f.key);
    });
  };
  for (const t of TABS) {
    if (!coreTabs.includes(t)) continue;
    tabs.push(t);
    placeAfter(t.key);
  }
  featureTabs.forEach((f, i) => { if (featureVisible[i] && !placed.has(i)) { placed.add(i); tabs.push(f); } });
  // A deep link to a hidden tab lands on the first visible one instead of an
  // empty panel.
  // The old two-row "Claude sessions" tab now lives at the bottom of
  // Terminal & panes, so its deep links land there.
  const wanted = tab === 'claude' ? 'panes' : tab;
  const activeTab = tabs.some((t) => t.key === wanted) ? wanted : tabs[0].key;

  return (
    <div className="dialog-backdrop" onMouseDown={close}>
      <div
        ref={cardRef}
        style={cardStyle}
        className="dialog settings-modal"
        role="dialog"
        aria-label="Settings"
        onMouseDown={(e) => e.stopPropagation()}
      >
        <div className="dialog-header settings-drag" {...dragHandleProps}>
          <h2 className="dialog-title">Settings</h2>
          <button onClick={close} title="Close (Esc)" aria-label="Close">×</button>
        </div>
        <div className="settings-layout">
          <nav className="settings-nav" role="tablist" aria-orientation="vertical">
            {tabs.map((t) => (
              <button
                key={t.key}
                type="button"
                role="tab"
                aria-selected={activeTab === t.key}
                className={`settings-nav-item${activeTab === t.key ? ' active' : ''}`}
                onClick={() => setTab(t.key)}
              >
                {t.label}
              </button>
            ))}
          </nav>
          <div className="settings-body" role="tabpanel">
            <ErrorBoundary key={activeTab} label="settings" title="This settings section failed to load">
              {activeTab === 'appearance' && <AppearanceTab />}
              {activeTab === 'panes' && <PanesTab />}
              {activeTab === 'sidebar' && <SidebarCustomizer />}
              {activeTab === 'devices' && !isMobile && <DevicesTab />}
              {activeTab === 'devices' && isMobile && <PhoneEncryptionSetting />}
              {activeTab === 'warden' && <WardenTab />}
              {activeTab === 'tasks' && <ServicesTab />}
              {featureTabs.map((f, i) => activeTab === f.key && featureVisible[i] && <f.Component key={f.key} />)}
            </ErrorBoundary>
          </div>
        </div>
      </div>
    </div>
  );
}
