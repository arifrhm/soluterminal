import type { TranscribeFn, TranscribeResult } from './types.js';

export interface EndpointTranscriberOptions {
  /** Absolute or app-relative URL of the transcription endpoint. */
  url: string;
  /**
   * Extra headers per request (auth tokens etc.). A function so tokens
   * can rotate without rebuilding the transcriber. Don't set
   * content-type — fetch fills in the multipart boundary itself.
   */
  getHeaders?: () => Record<string, string> | Promise<Record<string, string>>;
  /** Override fetch (tests, custom transports). Defaults to global fetch. */
  fetchImpl?: typeof fetch;
  /**
   * Extra multipart fields appended to every request — e.g.
   * `{ domain: 'code' }` to opt a coding app into code-tuned cleanup on a
   * shared backend. Static per app; per-request hints stay in TranscribeOpts.
   */
  fields?: Record<string, string>;
}

/**
 * Builds a TranscribeFn that POSTs multipart form data to an endpoint
 * implementing the soluvoice contract:
 *
 *   POST <url>
 *     audio:    <blob>            (required)
 *     language: "en"              (optional Whisper hint)
 *     context:  "..."             (optional cleanup-LLM context)
 *   → 200 {"text": "...", "cleanup": "ok|disabled|failed|skipped"}
 *
 * Keys stay server-side: the endpoint owns the Whisper/cleanup API
 * keys and any per-user quota. soluterminal's daemon (/api/transcribe)
 * and cloud both implement this contract.
 */
export function createEndpointTranscriber(opts: EndpointTranscriberOptions): TranscribeFn {
  const doFetch = opts.fetchImpl ?? fetch;
  return async (audio, t) => {
    const fd = new FormData();
    fd.append('audio', audio, t.filename);
    if (t.language) fd.append('language', t.language);
    if (t.context) fd.append('context', t.context);
    if (opts.fields) {
      for (const [k, v] of Object.entries(opts.fields)) fd.append(k, v);
    }
    const headers = opts.getHeaders ? await opts.getHeaders() : undefined;
    const resp = await doFetch(opts.url, {
      method: 'POST',
      body: fd,
      headers,
      signal: t.signal,
    });
    if (!resp.ok) {
      const body = await resp.text().catch(() => '');
      throw new Error(`transcribe ${resp.status}: ${body.slice(0, 300)}`);
    }
    return (await resp.json()) as TranscribeResult;
  };
}
