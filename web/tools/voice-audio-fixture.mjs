import { writeFile } from 'node:fs/promises';
/** Deterministic voiced bands plus quiet windows; no recognition claim. */
export async function writeVoiceFixture(path, { seconds = 240, rate = 48000, quiet = true } = {}) {
  const frames = seconds * rate;
  const wav = Buffer.alloc(44 + frames * 2);
  wav.write('RIFF', 0);
  wav.writeUInt32LE(wav.length - 8, 4);
  wav.write('WAVEfmt ', 8);
  wav.writeUInt32LE(16, 16);
  wav.writeUInt16LE(1, 20);
  wav.writeUInt16LE(1, 22);
  wav.writeUInt32LE(rate, 24);
  wav.writeUInt32LE(rate * 2, 28);
  wav.writeUInt16LE(2, 32);
  wav.writeUInt16LE(16, 34);
  wav.write('data', 36);
  wav.writeUInt32LE(frames * 2, 40);
  for (let i = 0; i < frames; i++) {
    const t = i / rate;
    const silence = quiet && t % 60 >= 56 && t % 60 < 56.3;
    const marker = Math.floor(t / 60) % 3;
    const signal =
      0.22 * Math.sin(2 * Math.PI * 220 * t) +
      0.12 * Math.sin(2 * Math.PI * (440 + marker * 110) * t);
    wav.writeInt16LE(silence ? 0 : Math.round(signal * 32767), 44 + i * 2);
  }
  await writeFile(path, wav);
  return { rate, frames, seconds };
}
export function decodeVoiceUpload(bytes) {
  const wav = Buffer.from(bytes);
  if (
    wav.toString('ascii', 0, 4) !== 'RIFF' ||
    wav.toString('ascii', 8, 12) !== 'WAVE' ||
    wav.readUInt32LE(4) + 8 !== wav.length ||
    wav.readUInt16LE(20) !== 1 ||
    wav.readUInt16LE(22) !== 1 ||
    wav.readUInt32LE(24) !== 16000 ||
    wav.readUInt32LE(40) + 44 !== wav.length
  )
    throw Error('Incomplete independent PCM WAV');
  return {
    frames: (wav.length - 44) / 2,
    durationMs: (wav.length - 44) / 32,
    pcm: wav.subarray(44),
  };
}
export function multipartVoiceUpload(body, type) {
  const boundary = /boundary=(?:"([^"]+)"|([^;]+))/.exec(type);
  if (!boundary) throw Error('missing multipart boundary');
  const delimiter = Buffer.from(`--${boundary[1] || boundary[2]}`);
  const fields = {};
  let audio;
  let cursor = body.indexOf(delimiter) + delimiter.length + 2;
  for (;;) {
    const headerEnd = body.indexOf(Buffer.from('\r\n\r\n'), cursor);
    if (headerEnd < 0) break;
    const header = body.toString('utf8', cursor, headerEnd);
    const end = body.indexOf(delimiter, headerEnd + 4);
    if (end < 0) throw Error('missing multipart end');
    const value = body.subarray(headerEnd + 4, end - 2);
    const name = /name="([^"]+)"/.exec(header)?.[1];
    if (name === 'audio') audio = value;
    else if (name) fields[name] = value.toString();
    cursor = end + delimiter.length + 2;
  }
  if (!audio) throw Error('missing audio');
  return { audio, fields, ...decodeVoiceUpload(audio) };
}
