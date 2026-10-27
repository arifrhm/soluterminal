import { useEffect, useRef, useState } from 'react';
import { api } from '../../api';
import type { VoiceWordsSettings } from '../../private/voice.api';
import { SettingsRow } from './SettingsRow';

export const VOICE_WORDS_LABEL = 'Words to recognise';

/** Splits what the user typed into words, the same way the daemon does. */
export function parseVoiceWords(text: string): string[] {
  return text
    .split(/[,;\n]/)
    .map((w) => w.trim().replace(/\s+/g, ' '))
    .filter(Boolean);
}

export function voiceWordsHint(s: VoiceWordsSettings): string {
  return (
    `Names and terms you say often, separated by commas (up to ${s.maxWords}). They're sent with ` +
    `your recording so they're spelled right. Already included: ${s.builtIn.join(', ')}.`
  );
}

// Saved when the box loses focus or on Enter, like every other live setting.
// A daemon without the endpoint answers 404 and the row stays hidden.
export function VoiceWordsSetting() {
  const [settings, setSettings] = useState<VoiceWordsSettings | null>(null);
  const [draft, setDraft] = useState('');
  const [error, setError] = useState<string | null>(null);
  const savedRef = useRef('');

  useEffect(() => {
    let mounted = true;
    void api
      .getVoiceWords()
      .then((s) => {
        if (!mounted || !s) return;
        setSettings(s);
        savedRef.current = s.words.join(', ');
        setDraft(savedRef.current);
      })
      .catch(() => {
        /* older or unreachable daemon: keep the row hidden */
      });
    return () => {
      mounted = false;
    };
  }, []);

  const save = async () => {
    if (!settings || draft.trim() === savedRef.current) return;
    setError(null);
    try {
      const next = await api.setVoiceWords(parseVoiceWords(draft));
      setSettings(next);
      savedRef.current = next.words.join(', ');
      setDraft(savedRef.current);
    } catch (e) {
      setError(`Couldn't save your words: ${String(e)}`);
    }
  };

  if (!settings) return null;
  return (
    <SettingsRow label={VOICE_WORDS_LABEL} hint={error ?? voiceWordsHint(settings)}>
      <input
        className="settings-input"
        aria-label={VOICE_WORDS_LABEL}
        placeholder="e.g. Mara Ellison, Ledger"
        value={draft}
        onChange={(e) => setDraft(e.target.value)}
        onBlur={() => void save()}
        onKeyDown={(e) => {
          if (e.key === 'Enter') {
            e.preventDefault();
            void save();
          }
        }}
      />
    </SettingsRow>
  );
}
