// Request-side helpers for dictation: no-speech detection, the context size
// sent for cleanup, the daily allowance headers and plain-language errors.

/** Whisper answers silence with "." or similar; no letters or digits = no speech. */
export function isNoSpeech(text: string | null | undefined): boolean {
  return !/[\p{L}\p{N}]/u.test(text ?? '');
}

/** Byte caps include Unicode and multipart headers, not JavaScript characters. */
export const VOICE_CONTEXT_MAX_CHARS = 4000; // legacy export
export const VOICE_AUDIO_UPLOAD_BYTES = 3 * 1024 * 1024;
export const VOICE_MULTIPART_RESERVE = 64 * 1024;
export const VOICE_BODY_BYTES = 4 * 1024 * 1024;
export const utf8Bytes = (value: string): number => new TextEncoder().encode(value).length;
export function trimVoiceContext(context: string | undefined): string | undefined {
  if (!context) return undefined;
  const value = context.trim();
  const bytes = new TextEncoder().encode(value);
  if (bytes.length <= VOICE_CONTEXT_MAX_CHARS) return value || undefined;
  let start = bytes.length - VOICE_CONTEXT_MAX_CHARS;
  while ((bytes[start] & 0xc0) === 0x80) start++;
  return new TextDecoder().decode(bytes.subarray(start)) || undefined;
}
export function validateVoiceUpload(
  audio: Blob,
  fields: Record<string, string | undefined>,
  filename: string,
): void {
  if (!/^[a-zA-Z0-9_.-]{1,80}$/.test(filename))
    throw new VoiceRequestError('Invalid recording filename.', 'other');
  const overhead =
    4096 +
    utf8Bytes(filename) +
    Object.entries(fields).reduce(
      (n, [key, value]) => n + utf8Bytes(key) + utf8Bytes(value ?? ''),
      0,
    );
  if (
    audio.size > VOICE_AUDIO_UPLOAD_BYTES ||
    overhead > VOICE_MULTIPART_RESERVE ||
    audio.size + overhead >= VOICE_BODY_BYTES
  ) {
    throw new VoiceRequestError(
      'This recording part is too large to upload. Your recording is kept for review.',
      'too_long',
    );
  }
  if (fields.language && !/^[a-z]{2,3}(-[A-Za-z]{2,8})?$/.test(fields.language))
    throw new VoiceRequestError('Invalid voice language.', 'other');
}

// --- errors -------------------------------------------------------------------

/** A transcribe failure the UI can act on, not just print. */
export class VoiceRequestError extends Error {
  constructor(
    message: string,
    readonly code: 'not_connected' | 'quota' | 'too_long' | 'busy' | 'other',
  ) {
    super(message);
    this.name = 'VoiceRequestError';
  }
}

/** Maps a failed /api/transcribe response to a plain-language error. */
export async function voiceErrorFromResponse(r: Response): Promise<VoiceRequestError> {
  const text = (await r.text().catch(() => '')).trim();
  let code = '';
  let message = '';
  try {
    const body = JSON.parse(text) as { error?: string; message?: string };
    code = body.error ?? '';
    message = body.message ?? '';
  } catch {
    message = text;
  }
  if (code === 'voice_not_connected') {
    return new VoiceRequestError(
      'Voice needs this computer connected to your free account.',
      'not_connected',
    );
  }
  if (r.status === 429) {
    const wait = /try again in (\S+)/.exec(message)?.[1];
    const service = /service-wide/.test(message);
    if (service)
      return new VoiceRequestError(
        `Voice is at today's limit for everyone. Try again${wait ? ` in ${wait}` : ' later'}.`,
        'quota',
      );
    return new VoiceRequestError(
      `You've used today's voice dictations. They reset${wait ? ` in ${wait}` : ' tomorrow'}.`,
      'quota',
    );
  }
  if (r.status === 413)
    return new VoiceRequestError(
      'This recording part is too large to upload. Your available recording is kept for review.',
      'too_long',
    );
  if (r.status === 503 && /busy/.test(message))
    return new VoiceRequestError('Voice is busy right now. Try again in a few seconds.', 'busy');
  return new VoiceRequestError(message || `Voice failed (${r.status}).`, 'other');
}

/** Reads the daily allowance headers from a transcribe response. */
export function voiceQuotaFromHeaders(
  h: Headers,
): { remaining: number; limit: number | null } | null {
  const remaining = Number.parseInt(h.get('X-Transcribe-Remaining') ?? '', 10);
  if (!Number.isFinite(remaining)) return null;
  const limit = Number.parseInt(h.get('X-Transcribe-Limit') ?? '', 10);
  return { remaining, limit: Number.isFinite(limit) ? limit : null };
}

/** Bound an untrusted JSON response before decoding it into transcript strings. */
export async function readVoiceJSON<T>(response: Response): Promise<T> {
  const reader = response.body?.getReader();
  if (!reader) {
    const text = await response.text();
    if (utf8Bytes(text) > 40 * 1024)
      throw new VoiceRequestError('Transcript response is too large. Recording is kept.', 'other');
    return JSON.parse(text) as T;
  }
  const chunks: Uint8Array[] = [];
  let size = 0;
  try {
    for (;;) {
      const { value, done } = await reader.read();
      if (done) break;
      size += value.byteLength;
      if (size > 40 * 1024) {
        await reader.cancel();
        throw new VoiceRequestError(
          'Transcript response is too large. Recording is kept.',
          'other',
        );
      }
      chunks.push(value);
    }
  } finally {
    reader.releaseLock();
  }
  const bytes = new Uint8Array(size);
  let offset = 0;
  for (const chunk of chunks) {
    bytes.set(chunk, offset);
    offset += chunk.length;
  }
  return JSON.parse(new TextDecoder().decode(bytes)) as T;
}
