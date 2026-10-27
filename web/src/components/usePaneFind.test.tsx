import { act, renderHook } from '@testing-library/react';
import { afterEach, expect, it, vi } from 'vitest';
import type { SearchAddon } from '@xterm/addon-search';
import type { Terminal } from '@xterm/xterm';
import { usePaneFind } from './usePaneFind';

afterEach(()=>{vi.useRealTimers();vi.unstubAllGlobals();});
it('only the visible scoped viewer handles a coincident pane ID, including delayed reveals',()=>{
 vi.useFakeTimers();
 const frames:FrameRequestCallback[]=[];vi.stubGlobal('requestAnimationFrame',(callback:FrameRequestCallback)=>{frames.push(callback);return frames.length;});
 let active='wsl',generation=0;
 const make=(scope:string)=>{
  const search={findPrevious:vi.fn(),findNext:vi.fn()};const terminal={focus:vi.fn(),clearSelection:vi.fn()};
  const hook=renderHook(()=>usePaneFind({paneId:'same',searchAddonRef:{current:search as unknown as SearchAddon},termRef:{current:terminal as unknown as Terminal},actionGuard:()=>active===scope,captureGuard:()=>{const captured=generation;return()=>active===scope&&generation===captured;}}));
  return {hook,search,terminal};
 };
 const wsl=make('wsl'),win=make('win');
 act(()=>window.dispatchEvent(new CustomEvent('solu:pane-find-open',{detail:{paneId:'same'}})));
 expect(wsl.hook.result.current.open).toBe(true);expect(win.hook.result.current.open).toBe(false);
 const input=document.createElement('input');document.body.appendChild(input);wsl.hook.result.current.inputRef.current=input;
 const focus=vi.spyOn(input,'focus');active='win';generation++;
 act(()=>{frames.forEach(fn=>fn(0));wsl.hook.result.current.close();});expect(focus).not.toHaveBeenCalled();expect(wsl.terminal.focus).not.toHaveBeenCalled();
 act(()=>window.dispatchEvent(new CustomEvent('solu:pane-search-reveal',{detail:{paneId:'same',query:'current output'}})));
 active='wsl';generation++;active='win';generation++;
 act(()=>vi.advanceTimersByTime(60));expect(win.search.findPrevious).not.toHaveBeenCalled();expect(wsl.search.findPrevious).not.toHaveBeenCalled();
 act(()=>{win.hook.result.current.setQuery('valid output');});expect(win.search.findPrevious).toHaveBeenCalledWith('valid output',expect.anything());
 wsl.hook.unmount();win.hook.unmount();input.remove();
});
