import { useEffect, useRef, useState } from 'react';
import { api } from '../../api';
import type { VoiceLocalSettings } from '../../private/voice.api';
import { SettingsCheck, SettingsGroup } from './SettingsRow';
import { useStore } from '../../store';
import { VoiceWordsSetting } from './VoiceWordsSetting';

export const VOICE_LOCAL_LABEL = 'Clean up dictation with my Claude plan';
export const VOICE_KEEP_MIC_LABEL =
  'Keep the microphone ready for 2 minutes after dictating (faster start; your system’s mic light stays on)';
export const VOICE_KEEP_MIC_HINT =
  'Off: the mic opens when you press and closes when you finish. On: it stays open between ' +
  'dictations so your first word isn’t cut off. Nothing is recorded or sent while it waits, but ' +
  'each dictation then starts with a split second from just before you press.';
export const VOICE_READY_TICK_LABEL = 'Tick when the mic is listening';
export const VOICE_READY_TICK_HINT =
  'A soft tick the moment recording starts. After a pause the mic takes a moment to open; ' +
  'start speaking at the tick so your first word is kept.';
// Why the row is on but not in use right now. Each says what happens instead,
// so the user knows dictation still works.
const REASONS: Record<string, string> = {
  not_installed: 'Needs Claude Code on this computer. Until then SoluTerminal cleans your dictation.',
  not_signed_in: 'Sign in to Claude Code in a pane to use this. Until then SoluTerminal cleans your dictation.',
  paused: "Paused for a few minutes after Claude Code didn't answer. SoluTerminal is cleaning your dictation meanwhile.",
};

export function voiceLocalHint(s: VoiceLocalSettings): string {
  if (s.enabled && !s.available && s.reason && REASONS[s.reason]) return REASONS[s.reason];
  if (!s.enabled) {
    return 'Off: SoluTerminal cleans your dictation, which counts toward your daily voice allowance.';
  }
  return (
    'Claude Haiku tidies your dictation on your own Claude Code login, so the terminal text it ' +
    'reads goes only to Anthropic, and it doesn’t count toward your daily voice allowance.'
  );
}

// The daemon owns this choice (it runs the cleanup). A daemon without the
// endpoint answers 404 and the row stays hidden.
export function VoiceCleanupSetting() {
  const [settings, setSettings] = useState<VoiceLocalSettings | null>(null);
  const [pending, setPending] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const pendingRef = useRef(false);
  const keepWarmMs = useStore((s) => s.voiceKeepWarmMs);
  const setKeepWarm = useStore((s) => s.setVoiceKeepWarm);
  const readyTick = useStore((s) => s.voiceReadyTick);
  const setReadyTick = useStore((s) => s.setVoiceReadyTick);

  useEffect(() => {
    let mounted = true;
    void api
      .getVoiceLocal()
      .then((s) => {
        if (mounted && s) setSettings(s);
      })
      .catch(() => {
        /* older or unreachable daemon: keep the row hidden */
      });
    return () => {
      mounted = false;
    };
  }, []);

  const change = async (next: boolean) => {
    if (!settings || pendingRef.current) return;
    pendingRef.current = true;
    setPending(true);
    setError(null);
    const previous = settings;
    setSettings({ ...previous, enabled: next });
    try {
      setSettings(await api.setVoiceLocal(next));
    } catch (e) {
      setSettings(previous);
      setError(`Couldn't save the voice setting: ${String(e)}`);
    } finally {
      pendingRef.current = false;
      setPending(false);
    }
  };

  return (
    <SettingsGroup title="Voice">
      <SettingsCheck
        label={VOICE_KEEP_MIC_LABEL}
        checked={keepWarmMs > 0}
        onChange={setKeepWarm}
        hint={VOICE_KEEP_MIC_HINT}
      />
      <SettingsCheck
        label={VOICE_READY_TICK_LABEL}
        checked={readyTick}
        onChange={setReadyTick}
        hint={VOICE_READY_TICK_HINT}
      />
      {settings && (
        <SettingsCheck
          label={VOICE_LOCAL_LABEL}
          checked={settings.enabled}
          disabled={pending}
          onChange={(next) => void change(next)}
          hint={voiceLocalHint(settings)}
        />
      )}
      <VoiceWordsSetting />
      {error && <p className="settings-row-hint settings-error">{error}</p>}
    </SettingsGroup>
  );
}
