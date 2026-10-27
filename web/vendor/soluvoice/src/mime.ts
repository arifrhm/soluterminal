// Pick the first MediaRecorder MIME the browser supports. Chrome/Firefox
// prefer webm/opus; Safari only does mp4/aac. Groq Whisper accepts both.
export function pickMimeType(): { mimeType: string; ext: string } {
  const mr =
    typeof window !== 'undefined'
      ? (window as unknown as { MediaRecorder?: typeof MediaRecorder }).MediaRecorder
      : undefined;
  if (!mr || !mr.isTypeSupported) return { mimeType: '', ext: 'webm' };
  const candidates: Array<[string, string]> = [
    ['audio/webm;codecs=opus', 'webm'],
    ['audio/webm', 'webm'],
    ['audio/mp4', 'mp4'],
    ['audio/mpeg', 'mp3'],
  ];
  for (const [m, ext] of candidates) {
    if (mr.isTypeSupported(m)) return { mimeType: m, ext };
  }
  return { mimeType: '', ext: 'webm' };
}
