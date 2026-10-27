import { act, renderHook } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { ApiError } from '../api';
import type { SystemStatus } from '../api';
import { useStore } from '../store';
import { SYSTEM_POLL_MS, getSystemStatusSnapshot, refreshSystemStatus, resetSystemStatusForTests, useSystemStatus } from './useSystemStatus';

const mocks=vi.hoisted(()=>({systemStatus:vi.fn()}));
vi.mock('../api',async(importOriginal)=>{const actual=await importOriginal<typeof import('../api')>();return {...actual,api:{...actual.api,systemStatus:mocks.systemStatus}};});
const status={sampledAt:'2026-01-01T00:00:00Z',level:{overall:'ok'}} as SystemStatus;
const flush=()=>act(async()=>{await vi.advanceTimersByTimeAsync(0)});
function visibility(value:DocumentVisibilityState){Object.defineProperty(document,'visibilityState',{configurable:true,get:()=>value});}

beforeEach(()=>{vi.useFakeTimers();resetSystemStatusForTests();mocks.systemStatus.mockReset().mockResolvedValue(status);visibility('visible');useStore.setState({updateStatus:null});});
afterEach(()=>{resetSystemStatusForTests();vi.useRealTimers();});

describe('useSystemStatus',()=>{
  it('polls once per shared cadence and stops after last unsubscribe',async()=>{const a=renderHook(()=>useSystemStatus());const b=renderHook(()=>useSystemStatus());await flush();expect(mocks.systemStatus).toHaveBeenCalledTimes(1);await act(async()=>{await vi.advanceTimersByTimeAsync(SYSTEM_POLL_MS)});expect(mocks.systemStatus).toHaveBeenCalledTimes(2);a.unmount();b.unmount();await act(async()=>{await vi.advanceTimersByTimeAsync(SYSTEM_POLL_MS)});expect(mocks.systemStatus).toHaveBeenCalledTimes(2);});
  it('pauses hidden and catches up when visible',async()=>{visibility('hidden');const hook=renderHook(()=>useSystemStatus());await flush();expect(mocks.systemStatus).not.toHaveBeenCalled();visibility('visible');document.dispatchEvent(new Event('visibilitychange'));await flush();expect(mocks.systemStatus).toHaveBeenCalledTimes(1);hook.unmount();});
  it('maps old and disabled daemons and preserves last data on network errors',async()=>{mocks.systemStatus.mockRejectedValueOnce(new ApiError(404,'old'));const hook=renderHook(()=>useSystemStatus());await flush();expect(getSystemStatusSnapshot().error).toBe('old-daemon');hook.unmount();resetSystemStatusForTests();mocks.systemStatus.mockResolvedValueOnce(status).mockRejectedValueOnce(new Error('offline'));const next=renderHook(()=>useSystemStatus());await flush();await act(async()=>{await vi.advanceTimersByTimeAsync(SYSTEM_POLL_MS)});expect(getSystemStatusSnapshot().status).toBe(status);expect(getSystemStatusSnapshot().error).toBe('network');next.unmount();});
  it('maps a disabled sampler response to unavailable',async()=>{
    mocks.systemStatus.mockRejectedValueOnce(new ApiError(503,'system sampler disabled'));
    const hook=renderHook(()=>useSystemStatus());
    await flush();
    expect(getSystemStatusSnapshot()).toMatchObject({status:null,error:'unavailable',polling:false});
    hook.unmount();
  });
  it('skips while update applies and retains the snapshot reference for identical data',async()=>{const hook=renderHook(()=>useSystemStatus());await flush();const first=getSystemStatusSnapshot();await act(async()=>{await vi.advanceTimersByTimeAsync(SYSTEM_POLL_MS)});expect(getSystemStatusSnapshot()).toBe(first);useStore.setState({updateStatus:{applying:true} as never});await act(async()=>{await vi.advanceTimersByTimeAsync(SYSTEM_POLL_MS)});expect(mocks.systemStatus).toHaveBeenCalledTimes(2);hook.unmount();});
  it('coalesces concurrent measure refreshes into one request',async()=>{
    let resolveRequest:(value:SystemStatus)=>void=()=>{};
    mocks.systemStatus.mockImplementation(()=>new Promise<SystemStatus>((resolve)=>{resolveRequest=resolve;}));
    const first=refreshSystemStatus({measure:true});
    const second=refreshSystemStatus({measure:true});
    expect(mocks.systemStatus).toHaveBeenCalledTimes(1);
    resolveRequest(status);
    await act(async()=>{await Promise.all([first,second]);await vi.advanceTimersByTimeAsync(0);});
    expect(mocks.systemStatus).toHaveBeenCalledTimes(1);
  });
});
