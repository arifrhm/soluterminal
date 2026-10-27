import { create } from 'zustand';
import type { PortScope } from './ports';

const key = 'solu.ports.view.v3';
function initial(): { scope: PortScope; includeOther: boolean } {
  try {
    const value = JSON.parse(localStorage.getItem(key) ?? 'null');
    if (value && ['workspace', 'panes', 'all'].includes(value.scope))
      return { scope: value.scope, includeOther: value.includeOther === true };
    const old = localStorage.getItem('solu.ports.onlyPanes.v2');
    if (old === '0') return { scope: 'all', includeOther: true };
    if (old === '1' || localStorage.getItem('solu.ports.onlyPanes') === '1')
      return { scope: 'panes', includeOther: false };
  } catch {
    /* storage may be unavailable */
  }
  return { scope: 'workspace', includeOther: false };
}
export const usePortPreferences = create<
  ReturnType<typeof initial> & {
    setView: (next: Partial<ReturnType<typeof initial>>) => void;
  }
>((set) => ({
  ...initial(),
  setView: (next) =>
    set((state) => {
      const value = { ...state, ...next };
      try {
        localStorage.setItem(
          key,
          JSON.stringify({ scope: value.scope, includeOther: value.includeOther }),
        );
      } catch {
        /* session preference still works */
      }
      return next;
    }),
}));
