import { describe, expect, it } from 'vitest';
import { blocksVoiceShortcut } from './voice.keys';

describe('blocksVoiceShortcut', () => {
  it('allows both pane-owned typing surfaces to invoke voice', () => {
    const xterm = document.createElement('textarea');
    xterm.className = 'xterm-helper-textarea';

    const nativePane = document.createElement('div');
    nativePane.className = 'native-pane';
    const composer = document.createElement('textarea');
    composer.className = 'composer-input';
    nativePane.appendChild(composer);

    expect(blocksVoiceShortcut(xterm)).toBe(false);
    expect(blocksVoiceShortcut(composer)).toBe(false);
  });

  it('still blocks dialog/rename inputs and unrelated composers', () => {
    const rename = document.createElement('input');
    const dialog = document.createElement('textarea');
    const unrelatedComposer = document.createElement('textarea');
    unrelatedComposer.className = 'composer-input';

    expect(blocksVoiceShortcut(rename)).toBe(true);
    expect(blocksVoiceShortcut(dialog)).toBe(true);
    expect(blocksVoiceShortcut(unrelatedComposer)).toBe(true);
    expect(blocksVoiceShortcut(document.createElement('div'))).toBe(false);
  });
});
