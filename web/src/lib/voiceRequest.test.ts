import { describe, expect, it } from 'vitest';
import { isNoSpeech, trimVoiceContext, voiceErrorFromResponse, voiceQuotaFromHeaders } from './voiceRequest';

describe('voiceRequest', () => {
  it('treats punctuation-only transcripts as no speech', () => {
    for (const t of ['', '.', ' … ', '?!', undefined]) expect(isNoSpeech(t)).toBe(true);
    for (const t of ['ok', '1', '好']) expect(isNoSpeech(t)).toBe(false);
  });

  it('keeps the tail of the context within 4000 characters', () => {
    expect(trimVoiceContext(undefined)).toBeUndefined();
    expect(trimVoiceContext('  short  ')).toBe('short');
    const long = 'a'.repeat(5000) + 'END';
    const out = trimVoiceContext(long) ?? '';
    expect(out.length).toBe(4000);
    expect(out.endsWith('END')).toBe(true);
  });

  it('maps a not-connected daemon answer to an actionable error', async () => {
    const err = await voiceErrorFromResponse(new Response(JSON.stringify({ error: 'voice_not_connected', message: 'x' }), { status: 503 }));
    expect(err.code).toBe('not_connected');
    expect(err.message).toMatch(/connected to your free account/);
  });

  it('turns the daily limit into plain words', async () => {
    const mine = await voiceErrorFromResponse(new Response('daily transcription limit reached — try again in 17h', { status: 429 }));
    expect(mine.code).toBe('quota');
    expect(mine.message).toBe("You've used today's voice dictations. They reset in 17h.");
    const everyone = await voiceErrorFromResponse(new Response('service-wide daily transcription limit reached — try again in 3h', { status: 429 }));
    expect(everyone.message).toMatch(/limit for everyone/);
  });

  it('names an over-long recording and a busy service', async () => {
    expect((await voiceErrorFromResponse(new Response('too big', { status: 413 }))).code).toBe('too_long');
    expect((await voiceErrorFromResponse(new Response('voice is busy right now', { status: 503 }))).code).toBe('busy');
  });

  it('reads the allowance headers', () => {
    const h = new Headers({ 'X-Transcribe-Remaining': '17', 'X-Transcribe-Limit': '20' });
    expect(voiceQuotaFromHeaders(h)).toEqual({ remaining: 17, limit: 20 });
    expect(voiceQuotaFromHeaders(new Headers({ 'X-Transcribe-Remaining': '5' }))).toEqual({ remaining: 5, limit: null });
    expect(voiceQuotaFromHeaders(new Headers())).toBeNull();
  });
});

it('bounds actual WAV and UTF-8 multipart bytes before upload', async () => {
  const { validateVoiceUpload, trimVoiceContext, utf8Bytes } = await import('./voiceRequest');
  const wav = new Blob([new Uint8Array(1920044)]);
  expect(() => validateVoiceUpload(wav, { language: 'en', context: '日本語' }, 'audio.wav')).not.toThrow();
  expect(utf8Bytes(trimVoiceContext('界'.repeat(4000))!)).toBeLessThanOrEqual(4000);
  expect(() => validateVoiceUpload(new Blob([new Uint8Array(3*1024*1024)]), {}, 'audio.wav')).not.toThrow();
  expect(() => validateVoiceUpload(new Blob([new Uint8Array(3*1024*1024+1)]), {}, 'audio.wav')).toThrow(/too large/);
  expect(() => validateVoiceUpload(wav, { context: '界'.repeat(22000) }, 'audio.wav')).toThrow(/too large/);
  expect(() => validateVoiceUpload(wav, {}, 'audio\r\n.wav')).toThrow(/filename/);
});
