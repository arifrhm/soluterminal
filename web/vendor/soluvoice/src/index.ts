export { useVoice } from './useVoice.js';
export type { UseVoiceOptions, Voice, VoiceInfo, HoldHandlers } from './useVoice.js';
export { VoiceButton, cleanupTooltip } from './VoiceButton.js';
export type { VoiceButtonProps } from './VoiceButton.js';
export { createEndpointTranscriber } from './transcriber.js';
export type { EndpointTranscriberOptions } from './transcriber.js';
export { insertIntoTextarea } from './insertIntoTextarea.js';
export { pickMimeType } from './mime.js';
export type {
  VoicePhase,
  CleanupStatus,
  TranscribeFn,
  TranscribeOpts,
  TranscribeResult,
} from './types.js';

export * from './continuousCapture.js';
export type {
  CaptureProgress,
  CaptureSegment,
  CaptureStopReason,
  VoiceDiagnostics,
} from './types.js';
