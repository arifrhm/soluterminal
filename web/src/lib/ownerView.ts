import type { RoutingScope } from '../api';
import type { PaneInfo } from '../types';
export type MainPresentation = { kind: 'workspace' } | {
 kind: 'owner'; taskId: string; scope: RoutingScope; history: boolean;
 loading: boolean; error?: string; pane?: PaneInfo;
 reopenedConversation?: string;
 retirementHandoffFrom?: { paneId: string; conversationId: string };
};
export interface WorkspaceReturnContext {
 scope: RoutingScope; workspace: string; focusedPaneId: string | null;
 lastUserFocusId: string | null; zoomedPaneId: string | null; element: HTMLElement | null;
 visiblePaneIds?: string[];
}

export function clearOwnerURL(push = true) {
 if(typeof window === 'undefined')return;
 const url=new URL(window.location.href),hash=new URLSearchParams(url.hash.slice(1));
 hash.delete('ownerTask');hash.delete('ownerHistory');url.hash=hash.toString();
 window.history[push?'pushState':'replaceState'](null,'',`${url.pathname}${url.search}${url.hash}`);
}
