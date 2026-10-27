/**
 * Popover outside-close listens on `click`, NOT `mousedown`: a mousedown-based
 * close can unmount a portal between mousedown and mouseup, so the browser never
 * synthesizes the inner button's click. This caused VoiceHistoryButton's
 * "Insert" action to silently fail; its synthetic body click depends on this
 * ordering (see VoiceHistoryButton.tsx:75-81). With `click`, React runs the
 * inner button's synthetic handler before the event reaches the document or
 * window listener, even if a prior render made a containment check unreliable.
 * The `mousedown` option exists only to preserve existing semantics when later
 * consumers are migrated.
 */
import { useEffect, useRef } from 'react';
import type { RefObject } from 'react';

export interface UseOutsideClickOptions {
  enabled?: boolean;
  event?: 'click' | 'mousedown';
  capture?: boolean;
  target?: 'window' | 'document';
  inside: Array<RefObject<Element | null> | Element | null | undefined>;
  ignoreSelectors?: string[];
}

export interface UseDismissOptions extends UseOutsideClickOptions {
  escape?: boolean;
  escapeStopPropagation?: boolean;
}

function resolveInside(
  entry: RefObject<Element | null> | Element | null | undefined,
): Element | null | undefined {
  if (entry && !(entry instanceof Element) && 'current' in entry) return entry.current;
  return entry;
}

function eventTarget(target: 'window' | 'document'): Window | Document {
  return target === 'window' ? window : document;
}

export function useOutsideClick(onOutside: () => void, opts: UseOutsideClickOptions): void {
  const {
    enabled = true,
    event = 'click',
    capture = true,
    target = 'window',
    inside,
    ignoreSelectors = [],
  } = opts;
  const onOutsideRef = useRef(onOutside);
  const insideRef = useRef(inside);
  const ignoreSelectorsRef = useRef(ignoreSelectors);
  onOutsideRef.current = onOutside;
  insideRef.current = inside;
  ignoreSelectorsRef.current = ignoreSelectors;

  useEffect(() => {
    if (!enabled) return;
    const listenerTarget = eventTarget(target);
    const onPointer = (e: Event) => {
      const eventNode = e.target;
      if (!(eventNode instanceof Node)) return;
      if (insideRef.current.some((entry) => resolveInside(entry)?.contains(eventNode))) return;
      const eventElement = eventNode instanceof Element ? eventNode : eventNode.parentElement;
      if (
        eventElement
        && ignoreSelectorsRef.current.some((selector) => eventElement.closest(selector))
      ) return;
      onOutsideRef.current();
    };
    listenerTarget.addEventListener(event, onPointer, capture);
    return () => listenerTarget.removeEventListener(event, onPointer, capture);
  }, [enabled, event, capture, target]);
}

export function useDismiss(onDismiss: () => void, opts: UseDismissOptions): void {
  const {
    enabled = true,
    event = 'click',
    capture = true,
    target = 'window',
    escape = true,
    escapeStopPropagation = false,
  } = opts;
  const onDismissRef = useRef(onDismiss);
  onDismissRef.current = onDismiss;

  useOutsideClick(onDismiss, opts);

  useEffect(() => {
    if (!enabled || !escape) return;
    const listenerTarget = eventTarget(target);
    const onKeyDown = (e: Event) => {
      if (!(e instanceof KeyboardEvent) || e.key !== 'Escape') return;
      if (escapeStopPropagation) e.stopPropagation();
      onDismissRef.current();
    };
    listenerTarget.addEventListener('keydown', onKeyDown, capture);
    return () => listenerTarget.removeEventListener('keydown', onKeyDown, capture);
  }, [enabled, event, capture, target, escape, escapeStopPropagation]);
}
