import { fireEvent, render, screen, waitFor } from '@testing-library/react';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { api } from '../../api';
import type { VoiceWordsSettings } from '../../private/voice.api';
import { VOICE_WORDS_LABEL, VoiceWordsSetting, parseVoiceWords } from './VoiceWordsSetting';

const base: VoiceWordsSettings = { words: ['Mara'], builtIn: ['SoluTerminal', 'Shinoque'], maxWords: 30 };

afterEach(() => {
  vi.restoreAllMocks();
});

describe('VoiceWordsSetting', () => {
  it('shows the saved words and the built-in list', async () => {
    vi.spyOn(api, 'getVoiceWords').mockResolvedValue(base);
    render(<VoiceWordsSetting />);
    expect(await screen.findByLabelText(VOICE_WORDS_LABEL)).toHaveValue('Mara');
    expect(screen.getByText(/Already included: SoluTerminal, Shinoque/)).toBeInTheDocument();
  });

  it('saves on blur and shows what the daemon kept', async () => {
    vi.spyOn(api, 'getVoiceWords').mockResolvedValue(base);
    const put = vi.spyOn(api, 'setVoiceWords').mockResolvedValue({ ...base, words: ['Mara', 'Ledger'] });
    render(<VoiceWordsSetting />);
    const box = await screen.findByLabelText(VOICE_WORDS_LABEL);
    fireEvent.change(box, { target: { value: 'Mara,  Ledger , ' } });
    fireEvent.blur(box);
    await waitFor(() => expect(put).toHaveBeenCalledWith(['Mara', 'Ledger']));
    await waitFor(() => expect(box).toHaveValue('Mara, Ledger'));
  });

  it("doesn't save when nothing changed", async () => {
    vi.spyOn(api, 'getVoiceWords').mockResolvedValue(base);
    const put = vi.spyOn(api, 'setVoiceWords');
    render(<VoiceWordsSetting />);
    fireEvent.blur(await screen.findByLabelText(VOICE_WORDS_LABEL));
    expect(put).not.toHaveBeenCalled();
  });

  it('stays hidden on an older daemon', async () => {
    vi.spyOn(api, 'getVoiceWords').mockResolvedValue(null);
    const { container } = render(<VoiceWordsSetting />);
    await waitFor(() => expect(container).toBeEmptyDOMElement());
  });

  it('parses commas, semicolons and new lines', () => {
    expect(parseVoiceWords('a, b;c\n  d  e ,')).toEqual(['a', 'b', 'c', 'd e']);
  });
});
