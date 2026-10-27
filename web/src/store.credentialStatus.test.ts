import { afterEach, expect, it, vi } from 'vitest';
import { api } from './api';
import { useStore } from './store';
import type { PaneInfo } from './types';


afterEach(() => vi.restoreAllMocks());
it('updates credential adoption on an otherwise unchanged idle pane', async () => {
 const before = {id:'pane_auth',cwd:'/repo',cmd:['codex'],title:'Codex',status:'running',state:'idle',codexAuth:{state:'waiting-for-idle'}} as PaneInfo;
 useStore.setState({panes:{[before.id]:before},layout:null,parkedPaneIds:[]});
 const after = {...before,codexAuth:{state:'live',account:'new'}};
 vi.spyOn(api,'listPanes').mockResolvedValue([after]);
 await useStore.getState().refreshPanes();
 expect(useStore.getState().panes[before.id].codexAuth).toEqual(after.codexAuth);
 const stable=useStore.getState().panes[before.id];
 await useStore.getState().refreshPanes();
 expect(useStore.getState().panes[before.id]).toBe(stable);
});
