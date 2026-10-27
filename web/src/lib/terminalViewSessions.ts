import { useSyncExternalStore } from 'react';
import { abandonTerminalRetirement, terminalInputAllowed } from './terminalInteractionState';
import { currentRoutingScope, onRoutingScopeChange, routingPrincipalEpoch, routingPrincipalAllowed, type RoutingScope } from '../api';
import type { PaneInfo } from '../types';
import { getMachineAccessState, subscribeMachineAccess } from './machineAccess';
import { acquirePaneHost, isPaneHostParked, releasePaneHost } from './paneHosts';

export interface TerminalViewSession {
 key: string; scope: RoutingScope; principal: number; pane: PaneInfo;
 processIdentity: string; windowVisible?: boolean; generation: number; retained: boolean; invalid: boolean;
}
let desktopWindowVisible=true;
const sessions = new Map<string, TerminalViewSession>();
const placementHolds = new Map<TerminalViewSession, number>();
const closeGuards = new Map<string, () => void>();
export function registerTerminalCloseGuard(key: string, close: () => void): () => void {
 closeGuards.set(key, close);
 return () => { if (closeGuards.get(key) === close) closeGuards.delete(key); };
}
const portals = new Map<string, Set<symbol>>();
const listeners = new Set<() => void>();
let snapshot: TerminalViewSession[] = [];
const publish = () => { snapshot = [...sessions.values()]; listeners.forEach(fn => fn()); };
export function sessionKey(scope: RoutingScope, paneId: string) { return JSON.stringify([scope.key,paneId]); }
export function processIdentity(pane: PaneInfo) { return JSON.stringify([pane.pid,pane.socketPath,pane.createdAt,pane.kind ?? '',pane.role ?? '']); }
export function ensureTerminalSession(scope: RoutingScope, pane: PaneInfo, retain = false): TerminalViewSession {
 const key = sessionKey(scope,pane.id); const prior = sessions.get(key);
 if (prior && prior.processIdentity === processIdentity(pane) && !prior.invalid) {
  if (prior.pane !== pane || retain && !prior.retained) { prior.pane = pane; prior.retained ||= retain; publish(); }
  return prior;
 }
 if (prior) { closeGuards.get(key)?.(); abandonTerminalRetirement(key); prior.invalid = true; prior.generation++; }
 const next: TerminalViewSession = {key,scope,principal:routingPrincipalEpoch(),pane,processIdentity:processIdentity(pane),windowVisible:desktopWindowVisible,generation:(prior?.generation ?? 0)+1,retained:retain,invalid:false};
 sessions.set(key,next); acquirePaneHost(key); publish(); return next;
}
export function terminalSession(key: string): TerminalViewSession | undefined { return sessions.get(key); }
// A pane move may briefly have no layout slot (including the conservative
// workspace-switch blank phase). Keep its existing terminal core through that
// transaction even when the user has disabled opportunistic warm caching.
export function holdTerminalPlacement(scope: RoutingScope, paneId: string): () => void {
 const session = sessions.get(sessionKey(scope,paneId));
 if (!session) return () => {};
 placementHolds.set(session,(placementHolds.get(session) ?? 0)+1);
 let released=false;
 return () => {
  if(released)return; released=true;
  const count=(placementHolds.get(session) ?? 1)-1;
  if(count > 0)placementHolds.set(session,count);else placementHolds.delete(session);
 };
}
export function useTerminalSessions() { return useSyncExternalStore(fn => {listeners.add(fn); return () => listeners.delete(fn);},()=>snapshot); }
export function useTerminalWindowVisible() {
 return useSyncExternalStore(fn=>{listeners.add(fn);return()=>listeners.delete(fn);},()=>desktopWindowVisible && document.visibilityState !== 'hidden');
}
export function retainTerminalSessions(scope: RoutingScope, ids: string[]) {
 let changed = false;
 for (const id of ids) {const s = sessions.get(sessionKey(scope,id)); if (s && !s.retained) {s.retained = true; changed = true;} }
 if (changed) publish();
}
export function sessionVisible(session: TerminalViewSession): boolean {
 return session.windowVisible !== false && routingPrincipalAllowed() && !session.invalid && session.principal === routingPrincipalEpoch() && session.scope.key === currentRoutingScope().key
  && !isPaneHostParked(session.key) && document.visibilityState !== 'hidden';
}
export function captureSessionGesture(session: TerminalViewSession): () => boolean {
 const generation = session.generation; const identity = session.processIdentity;
 return () => generation === session.generation && identity === session.processIdentity && sessionVisible(session) && terminalInputAllowed(session.key);
}
export function invalidateSessionPlacement(key: string) { const s = sessions.get(key); if(s) s.generation++; }
export function forgetTerminalSession(key: string): void {
 const session = sessions.get(key); if (!session) return;
 closeGuards.get(key)?.(); abandonTerminalRetirement(key); session.invalid = true; session.generation++; sessions.delete(key); placementHolds.delete(session); publish();
 // A mounted portal releases only after its terminal core has been disposed.
 // Unmounted cache entries have no React cleanup and must release here.
 if (!portals.get(key)?.size) releasePaneHost(key);
}
export function registerTerminalPortal(key: string): () => void {
 const token = Symbol(key);
 const owners = portals.get(key) ?? new Set<symbol>();
 owners.add(token); portals.set(key,owners);
 return () => {
  owners.delete(token);
  if (!owners.size && portals.get(key) === owners) portals.delete(key);
  // Child passive cleanup disposes xterm against a connected host first.
  setTimeout(() => {
   if (!sessions.has(key) && !portals.get(key)?.size) releasePaneHost(key);
  },0);
 };
}
export function reconcileTerminalSessions(scope: RoutingScope, visible: Set<string>, opportunistic: Set<string>) {
 for (const session of sessions.values()) {
  if (session.scope.key === scope.key && !session.retained && !placementHolds.has(session) && !visible.has(session.pane.id) && !opportunistic.has(session.pane.id)) forgetTerminalSession(session.key);
 }
}
onRoutingScopeChange(() => {
 for (const session of sessions.values()) {
  session.generation++;
  if (session.principal !== routingPrincipalEpoch()) forgetTerminalSession(session.key);
 }
});
subscribeMachineAccess(() => {
 const access = getMachineAccessState(); if (!access.blocked || !access.machineId) return;
 for (const s of sessions.values()) if (s.scope.prefix === `/m/${encodeURIComponent(access.machineId)}` || s.scope.prefix === `/m/${access.machineId}`) forgetTerminalSession(s.key);
});
// Only call after a complete successful snapshot for this exact routing scope.
// Partial aggregate responses and failed requests cannot prove disappearance.
export function reconcileConfirmedTerminalPanes(scope: RoutingScope, panes: PaneInfo[]) {
 const live = new Set(panes.map(pane=>pane.id));
 for(const session of sessions.values()) {
  if(session.scope.key === scope.key && !live.has(session.pane.id)) forgetTerminalSession(session.key);
 }
}
export function terminalSessionCount(): number { return sessions.size; }
export function clearTerminalSessions() { for(const key of [...sessions.keys()]) forgetTerminalSession(key); }
if (typeof window !== 'undefined') window.addEventListener('pagehide',clearTerminalSessions);

// Retained cores share one transport liveness clock. No active header timers.
const keepalives = new Map<string,()=>void>();
let keepaliveTimer: ReturnType<typeof setInterval> | undefined;
export function registerSessionKeepalive(key: string, callback: ()=>void): ()=>void {
 keepalives.set(key,callback);
 if (!keepaliveTimer) keepaliveTimer = setInterval(()=>{for(const fn of keepalives.values()) fn();},25000);
 return ()=>{if(keepalives.get(key) === callback) keepalives.delete(key); if(!keepalives.size && keepaliveTimer) {clearInterval(keepaliveTimer);keepaliveTimer=undefined;} };
}
export function terminalSessionResources() {return {cores:sessions.size, keepaliveCallbacks:keepalives.size,keepaliveTimers:keepaliveTimer ? 1 : 0};}

// One visibility coordinator invalidates gestures across hide-and-return even
// when a desktop WebView does not change document.visibilityState on minimize.
function invalidateWindowGestures(visible?:boolean){
 if(visible !== undefined)desktopWindowVisible=visible;
 for(const session of sessions.values()){session.generation++;if(visible !== undefined)session.windowVisible=visible;}
 publish();
}
if(typeof window !== 'undefined'){
 window.addEventListener('solu:desktop-will-background',()=>invalidateWindowGestures(false));
 // The desktop shell also sends "restored" on every focus return (alt-tab, the
 // OS mic prompt, a screenshot). Focus alone isn't a hide-and-return: invalidating
 // then dropped in-flight dictation into recovery ("destination changed"). Only a
 // shell that announces its own native hides (hideTracked) can be trusted here.
 window.addEventListener('solu:desktop-restored',(e)=>{
  const detail=(e as CustomEvent<{reason?:string;hideTracked?:boolean}>).detail;
  if(detail?.reason === 'focused' && detail.hideTracked === true && desktopWindowVisible)return;
  invalidateWindowGestures(true);
 });
 document.addEventListener('visibilitychange',()=>invalidateWindowGestures());
}
