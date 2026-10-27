// Unit tests for the prompt-suggestion derivation (the composer ghost-text
// source). derivePromptSuggestion is a PURE function over the event ring: it
// returns the most-recent LIVE (not fromReplay) prompt_suggestion that is newer
// than the last turn boundary (turn_end / user_text / error) and newer than the
// consumed seq (Tab-accepted). We drive it directly via __useNativePaneTest
// (rendering the hook pulls the whole store/cloud graph into React).

import { describe, expect, it } from 'vitest';
import { __useNativePaneTest as T } from './useNativePane';
import type { NativeEvent } from './nativeEvents';

const derive = T.derivePromptSuggestion;

function ev(seq: number, type: NativeEvent['type'], extra: Partial<NativeEvent> = {}): NativeEvent {
  return { seq, paneId: 'p1', type, fromReplay: false, ...extra } as NativeEvent;
}
const turnEnd = (seq: number) => ev(seq, 'turn_end');
const userText = (seq: number) => ev(seq, 'user_text', { text: 'next' });
const err = (seq: number) => ev(seq, 'error', { message: 'boom' });
const suggestion = (seq: number, text: string, fromReplay = false) =>
  ev(seq, 'prompt_suggestion', { text, fromReplay });

describe('derivePromptSuggestion', () => {
  it('returns the suggestion that arrived after the last turn_end', () => {
    const events = [turnEnd(1), suggestion(2, 'write a helper')];
    expect(derive(events, 0)?.text).toBe('write a helper');
  });

  it('returns null when there is no suggestion', () => {
    expect(derive([turnEnd(1), userText(2)], 0)).toBeNull();
  });

  it('suppresses a suggestion superseded by a later user_text (new turn)', () => {
    const events = [turnEnd(1), suggestion(2, 'old'), userText(3)];
    expect(derive(events, 0)).toBeNull();
  });

  it('suppresses a suggestion superseded by a later turn_end (another full turn)', () => {
    const events = [turnEnd(1), suggestion(2, 'stale'), turnEnd(3)];
    expect(derive(events, 0)).toBeNull();
  });

  it('never derives a fromReplay (transcript-seed) suggestion', () => {
    const events = [turnEnd(1), suggestion(2, 'seed', true)];
    expect(derive(events, 0)).toBeNull();
  });

  it('honors the consumed seq (Tab-accepted suggestion stops deriving)', () => {
    const events = [turnEnd(1), suggestion(2, 'accepted')];
    expect(derive(events, 2)).toBeNull(); // consumedSeq == its seq
  });

  it('keeps the LATEST qualifying suggestion after several turns', () => {
    const events = [
      turnEnd(1),
      suggestion(2, 'first'),
      userText(3),
      turnEnd(4),
      suggestion(5, 'latest'),
    ];
    expect(derive(events, 0)?.text).toBe('latest');
  });

  it('treats a later error as a turn boundary (suppresses)', () => {
    const events = [turnEnd(1), suggestion(2, 'before-error'), err(3)];
    expect(derive(events, 0)).toBeNull();
  });

  it('ignores suggestions at or below the consumed seq but keeps a newer one', () => {
    const events = [turnEnd(1), suggestion(2, 'consumed'), suggestion(3, 'fresh')];
    expect(derive(events, 2)?.text).toBe('fresh');
  });
});
