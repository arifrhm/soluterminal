import { create } from 'zustand';
import { readSettingsFlag } from './lib/settingsFlags';
import { recordSettingsEvent } from './lib/settingsTelemetry';
import {
  LEGACY_SECTION_TARGETS,
  LEGACY_TAB_TARGETS,
  SETTINGS_CATEGORIES,
  resolveSettingsTarget,
  type LegacySettingsTab,
  type SettingsCategory,
  type SettingsTarget,
} from './settingsRegistry';

// Global open-state for the unified Settings dialog. Decoupled from the
// dialog's mount point (App.tsx) so any trigger — the topbar gear, the
// sidebar section right-click, the credentials "Manage team pool…" link —
// can open it on a specific tab without prop-drilling. The gear opens with
// no tab argument, which lands on the last-viewed tab (Appearance at first).
//
// The canonical category/section/control destination is adapted back to the
// legacy tab and delayed-section fields while callers migrate. The
// legacyRedirects flag provides a reversible retirement boundary for that
// adapter without coupling this store to the dialog implementation.

/** @deprecated Use SettingsCategory. */
export type SettingsTab = LegacySettingsTab;
export type { LegacySettingsTab, SettingsCategory, SettingsTarget };

export type SettingsSection = 'tasks.resourceLimits.maxHeavyOps' | 'tasks.modelsFallbacks';

export const SETTINGS_SECTION_IDS: Record<SettingsSection, string> = {
  'tasks.resourceLimits.maxHeavyOps': 'settings-row-maxHeavyOps',
  'tasks.modelsFallbacks': 'settings-section-models-fallbacks',
};

const FALLBACK_TABS: Record<SettingsCategory, SettingsTab> = {
  appearance: 'appearance',
  workspace: 'panes',
  sessions: 'claude',
  workflows: 'tasks',
  agents: 'warden',
  credentials: 'credentials',
  team: 'team',
  devices: 'devices',
};

function readInitialCategory(): SettingsCategory {
  try {
    const saved = localStorage.getItem('solu.settings.lastCategory');
    const match = SETTINGS_CATEGORIES.find((category) => category.id === saved);
    if (match) return match.id;
  } catch {
    // Storage can be unavailable in privacy-restricted windows.
  }
  return 'appearance';
}

function persistCategory(category: SettingsCategory): void {
  try {
    localStorage.setItem('solu.settings.lastCategory', category);
  } catch {
    // The in-memory destination remains authoritative when storage is blocked.
  }
}

function sameTarget(left: SettingsTarget, right: SettingsTarget): boolean {
  return left.category === right.category
    && left.section === right.section
    && left.control === right.control
    && left.modelRole === right.modelRole
    && left.search === right.search;
}

function legacyTabFor(target: SettingsTarget): SettingsTab {
  const entries = Object.entries(LEGACY_TAB_TARGETS) as Array<[SettingsTab, SettingsTarget]>;
  if (target.section) {
    const exact = entries.find(([, candidate]) => (
      candidate.category === target.category && candidate.section === target.section
    ));
    if (exact) return exact[0];
  }
  const categoryOnly = entries.find(([, candidate]) => (
    candidate.category === target.category && candidate.section === undefined
  ));
  return categoryOnly?.[0] ?? FALLBACK_TABS[target.category];
}

function legacySectionFor(target: SettingsTarget): SettingsSection | null {
  const match = (Object.entries(LEGACY_SECTION_TARGETS) as Array<[SettingsSection, SettingsTarget]>).find(
    ([, candidate]) => candidate.section === target.section && candidate.control === target.control,
  );
  return match?.[0] ?? null;
}

function resolveStoreTarget(input: string | SettingsTarget): ReturnType<typeof resolveSettingsTarget> {
  const resolved = resolveSettingsTarget(input);
  if (resolved.legacy && !readSettingsFlag('legacyRedirects', true)) {
    return { target: { category: 'appearance' }, legacy: false };
  }
  return resolved;
}

interface SettingsState {
  open: boolean;
  category: SettingsCategory;
  /** @deprecated Use category. */
  tab: SettingsTab;
  pendingTarget: SettingsTarget | null;
  currentTarget: SettingsTarget;
  /** @deprecated Delayed workflow sections still consume this value. */
  section: SettingsSection | null;
  modelRole: string | null;
  query: string;
  history: SettingsTarget[];
  openSettings: (
    target?: SettingsTarget | SettingsCategory | LegacySettingsTab | string,
    section?: SettingsSection | string,
    modelRole?: string,
  ) => void;
  consumeSection: () => SettingsSection | null;
  consumeModelRole: () => string | null;
  consumeTarget: () => SettingsTarget | null;
  closeSettings: () => void;
  setCategory: (category: SettingsCategory) => void;
  /** @deprecated Use setCategory. */
  setTab: (tab: SettingsTab) => void;
  setQuery: (query: string) => void;
  goBack: () => void;
}

const initialCategory = readInitialCategory();

export const useSettings = create<SettingsState>((set) => ({
  open: false,
  category: initialCategory,
  tab: legacyTabFor({ category: initialCategory }),
  pendingTarget: null,
  currentTarget: { category: initialCategory },
  section: null,
  modelRole: null,
  query: '',
  history: [],
  openSettings: (input, legacySection, modelRole) => set((state) => {
    const inputResolution = input === undefined
      ? { target: { category: state.category } as SettingsTarget, legacy: false }
      : resolveStoreTarget(input);
    let target = { ...inputResolution.target };

    if (legacySection !== undefined) {
      target = { ...resolveStoreTarget(legacySection).target };
      if (modelRole !== undefined) target.modelRole = modelRole;
      recordSettingsEvent('legacySection', legacySection);
    } else {
      if (modelRole !== undefined) target.modelRole = modelRole;
      if (inputResolution.legacy && typeof input === 'string') {
        if (Object.prototype.hasOwnProperty.call(LEGACY_SECTION_TARGETS, input)) {
          recordSettingsEvent('legacySection', input);
        } else if (Object.prototype.hasOwnProperty.call(LEGACY_TAB_TARGETS, input)) {
          recordSettingsEvent('legacyTab', input);
        }
      }
    }

    const history = state.open && !sameTarget(state.currentTarget, target)
      ? [...state.history, state.currentTarget].slice(-20)
      : state.history;
    persistCategory(target.category);
    return {
      open: true,
      category: target.category,
      tab: legacyTabFor(target),
      pendingTarget: target,
      currentTarget: target,
      section: legacySectionFor(target),
      modelRole: target.modelRole ?? null,
      history,
    };
  }),
  consumeSection: () => {
    let requested: SettingsSection | null = null;
    set((state) => {
      requested = state.section;
      return state.section === null ? state : { section: null };
    });
    return requested;
  },
  consumeModelRole: () => {
    let requested: string | null = null;
    set((state) => {
      requested = state.modelRole;
      return state.modelRole === null ? state : { modelRole: null };
    });
    return requested;
  },
  consumeTarget: () => {
    let requested: SettingsTarget | null = null;
    set((state) => {
      requested = state.pendingTarget;
      return state.pendingTarget === null ? state : { pendingTarget: null };
    });
    return requested;
  },
  closeSettings: () => set({
    open: false,
    pendingTarget: null,
    section: null,
    modelRole: null,
    query: '',
    history: [],
  }),
  setCategory: (category) => set((state) => {
    const target: SettingsTarget = { category };
    persistCategory(category);
    return {
      category,
      tab: legacyTabFor(target),
      pendingTarget: target,
      currentTarget: target,
      section: null,
      modelRole: null,
      history: sameTarget(state.currentTarget, target)
        ? state.history
        : [...state.history, state.currentTarget].slice(-20),
    };
  }),
  setTab: (tab) => set((state) => {
    const target = { ...LEGACY_TAB_TARGETS[tab] };
    persistCategory(target.category);
    return {
      category: target.category,
      tab,
      pendingTarget: target,
      currentTarget: target,
      section: null,
      modelRole: null,
      history: sameTarget(state.currentTarget, target)
        ? state.history
        : [...state.history, state.currentTarget].slice(-20),
    };
  }),
  setQuery: (query) => set({ query }),
  goBack: () => set((state) => {
    const target = state.history.at(-1);
    if (!target) return state;
    persistCategory(target.category);
    return {
      category: target.category,
      tab: legacyTabFor(target),
      pendingTarget: target,
      currentTarget: target,
      section: legacySectionFor(target),
      modelRole: target.modelRole ?? null,
      history: state.history.slice(0, -1),
    };
  }),
}));
