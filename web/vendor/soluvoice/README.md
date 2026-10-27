# soluvoice

Hold-to-talk voice dictation for React apps. Extracted from
[SoluTerminal](https://github.com/soluterminal/soluterminal)'s
battle-tested voice feature.

**What's in the box:**

- `useVoice()` — MediaRecorder lifecycle: hold-to-record, release-to-transcribe,
  slide-off-to-cancel, push-to-talk via imperative `start()`/`stop()`,
  30s stall watchdog, accidental-tap filtering, Safari/Chrome MIME handling,
  mobile keyboard suppression, the released-key-before-mic-ready race fix.
- `<VoiceButton>` — a ready-made mic button driven by a `useVoice` instance.
- `createEndpointTranscriber()` — POSTs audio to your transcription endpoint
  (keys stay server-side).
- `insertIntoTextarea()` — inserts text into a React-controlled textarea so
  `onChange` actually fires.

**What's deliberately NOT in the box:** delivery. The package hands you the
final transcript via `onText` and your app decides what to do with it —
insert into an editor, submit a chat message, write to a terminal. All the
app-specific glue (target pinning rules, auto-send, submit timing) lives in
your app.

## Install

SoluTerminal vendors this package as `web/vendor/soluvoice-<version>.tgz`.
After changing the source here, rebuild the tarball from `web/` with
`npm run vendor:soluvoice` (`npm run vendor:soluvoice:check` verifies it).

## Usage

```tsx
import { useVoice, VoiceButton, createEndpointTranscriber } from 'soluvoice';
import 'soluvoice/styles.css';

const transcribe = createEndpointTranscriber({
  url: '/api/transcribe',
  getHeaders: () => ({ authorization: `Bearer ${getToken()}` }),
});

function MyEditorToolbar() {
  const voice = useVoice({
    transcribe,
    language: 'en', // or '' / undefined for auto-detect
    // Pin the destination at record-start. Return null to refuse
    // (e.g. nothing focused). Whatever you return comes back in
    // onText's info.target even if focus moved meanwhile.
    acquireTarget: () => getFocusedEditor() ?? null,
    getContext: (editor) => editor.surroundingText(), // optional LLM-cleanup context
    onText: (text, { target }) => target.insert(text),
    onError: (msg, kind) => toast(msg, kind),
    // Drive a "🎤 Listening… / release to cancel" chip:
    onSession: (s) => setVoiceChip(s.phase === 'idle' ? null : s),
  });

  return <VoiceButton voice={voice} hasTarget={!!getFocusedEditor()} />;
}
```

Push-to-talk hotkey (e.g. hold Alt+S): call `voice.start()` on keydown and
`voice.stop()` on keyup — the hook already handles the
"key released before the mic finished initializing" race.

Custom UI instead of `<VoiceButton>`: spread `voice.holdHandlers` onto any
element and read `voice.phase` / `voice.aboutToCancel` /
`voice.cleanupStatus` yourself. Give the element `touch-action: none` and
`user-select: none` so holds aren't hijacked by scrolling/selection.

## Server contract

The package is frontend-only; transcription keys and quota live on your
server. Implement one endpoint:

```
POST /api/transcribe          multipart/form-data
  audio:    <blob>            required — webm/opus, mp4/aac, or mp3
  language: "en"              optional Whisper hint
  context:  "..."             optional context for LLM cleanup

200 → {"text": "...", "cleanup": "ok" | "disabled" | "failed" | "skipped"}
```

`cleanup` reports whether an LLM polish pass ran (`ok`), was unconfigured
(`disabled`), errored (`failed`), or produced rejected output (`skipped`) —
in the last three cases `text` is raw Whisper output. Omitting the field is
treated as `disabled`.

Reference implementations: soluterminal's `daemon/voice.go` (Groq
`whisper-large-v3-turbo`, ~300ms) and `cloud/voice.go` (adds per-user
quota + Claude Haiku cleanup). A minimal server is ~40 lines: forward the
multipart body to `https://api.groq.com/openai/v1/audio/transcriptions`
with your `GROQ_API_KEY` and return `{"text": ...}`.

## Field notes baked into the defaults

- **250ms minimum recording** — shorter clips are accidental taps; Whisper
  hallucinates "thank you" from its priors on near-empty audio.
- **30s transcribe watchdog** — without it, one stalled fetch pins the
  button at "transcribing" until a page reload.
- **80px slide-to-cancel** — the WhatsApp gesture; far enough that jitter
  won't arm it, sliding back under disarms.
- **Explicit getUserMedia constraints** (echoCancellation, noiseSuppression,
  autoGainControl) — `audio: true` often leaves these off on mobile and
  Whisper accuracy drops materially.
- **Target pinned at record-start** — transcription takes seconds; users
  navigate away mid-wait, and the text must land where they started.
- If transcripts turn into stock-sentence hallucinations ("thanks for
  watching"), suspect the mic before the model.

## License

Apache-2.0. See [LICENSE](LICENSE).
