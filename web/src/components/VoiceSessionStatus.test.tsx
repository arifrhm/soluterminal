import { fireEvent, render, screen } from '@testing-library/react';
import { expect, it, vi } from 'vitest';
import { VoiceSessionStatusView } from './VoiceSessionStatus';
import type { SessionProgress } from '../lib/voiceSession';
const state: SessionProgress = {
  sessionId: 'test',
  paneId: 'p1',
  phase: 'starting',
  capturedMs: 125000,
  completedSegments: 1,
  totalSegments: 3,
  retainedBytes: 1000,
  retainedDurationMs: 60000,
  partial: false,
};
it('shows accessible preparing, concurrent processing, and recovery controls', () => {
  const props = {
    state,
    stop: vi.fn(),
    cancel: vi.fn(),
    retry: vi.fn(),
    useAvailable: vi.fn(),
    download: vi.fn(),
  };
  const view = render(<VoiceSessionStatusView {...props} />);
  expect(screen.getByRole('status')).toHaveTextContent('Preparing microphone');
  fireEvent.click(screen.getByRole('button', { name: 'Stop' }));
  expect(props.stop).toHaveBeenCalledOnce();
  view.rerender(<VoiceSessionStatusView {...props} state={{ ...state, phase: 'recording' }} />);
  expect(screen.getByText(/recording continues/)).toBeTruthy();
  expect(screen.getByLabelText('Recorded duration')).toHaveAttribute('aria-live', 'off');
  view.rerender(
    <VoiceSessionStatusView
      {...props}
      state={{ ...state, phase: 'recovery', partial: true, stopReason: 'error', failedSegment: 2 }}
    />,
  );
  expect(screen.getByText(/until retry, discard, or page reload/)).toBeTruthy();
  expect(screen.getByText(/Partial result/)).toBeTruthy();
  for (const [label, callback] of [
    ['Retry remaining', props.retry],
    ['Use available text', props.useAvailable],
    ['Download retained audio', props.download],
    ['Discard audio', props.cancel],
  ] as const) {
    fireEvent.click(screen.getByRole('button', { name: label }));
    expect(callback).toHaveBeenCalledOnce();
  }
});

it('offers the normal connection flow without discarding unprocessed audio', () => {
  const connect = vi.fn();
  const cancel = vi.fn();
  render(<VoiceSessionStatusView state={{ ...state, phase: 'recovery', errorCode: 'not_connected' }} stop={vi.fn()} cancel={cancel} retry={vi.fn()} useAvailable={vi.fn()} download={vi.fn()} connect={connect} />);
  fireEvent.click(screen.getByRole('button', { name: 'Connect this computer' }));
  expect(connect).toHaveBeenCalledOnce();
  expect(cancel).not.toHaveBeenCalled();
});
it('shows per-part allowance use and the service-reported remaining count', () => {
  render(<VoiceSessionStatusView state={{ ...state, phase: 'recording', quotaRemaining: 19 }} stop={vi.fn()} cancel={vi.fn()} retry={vi.fn()} useAvailable={vi.fn()} download={vi.fn()} />);
  expect(screen.getByText(/one cloud transcription/)).toHaveTextContent('19 left today');
  expect(screen.getByText(/Up to 30 minutes/)).toHaveTextContent('daily allowance applies');
});
