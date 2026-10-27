import type { CSSProperties, ReactElement } from 'react';
import {
  IconAlertTriangle,
  IconCheck,
  IconCircle,
  IconClock,
  IconDotFilled,
  IconPause,
  IconX,
} from '../ui/icons';

/** Shared visual vocabulary for run-like states across product surfaces. */
export type RunVisual = 'ok' | 'running' | 'attention' | 'error' | 'paused' | 'queued' | 'neutral';

export const RUN_VISUAL_META: Record<RunVisual, {
  Icon: (p: { size?: number; style?: CSSProperties }) => ReactElement;
  color: string;
}> = {
  ok: { Icon: IconCheck, color: 'var(--ok)' },
  running: { Icon: IconDotFilled, color: 'var(--accent)' },
  attention: { Icon: IconAlertTriangle, color: 'var(--warn)' },
  error: { Icon: IconX, color: 'var(--danger)' },
  paused: { Icon: IconPause, color: 'var(--fg-dim)' },
  queued: { Icon: IconClock, color: 'var(--fg-dim)' },
  neutral: { Icon: IconCircle, color: 'var(--fg-dim)' },
};

export function runVisualColor(visual: RunVisual): string {
  return RUN_VISUAL_META[visual].color;
}

export function scheduleRunVisual(status: string | undefined): RunVisual {
  if (status === 'fired') return 'ok';
  if (status === 'skipped') return 'paused';
  if (status === 'error') return 'error';
  if (status === 'quota_paused') return 'paused';
  if (status === 'credential_paused') return 'attention';
  return 'neutral';
}

export function agentToneVisual(tone: 'ok' | 'warn' | 'err' | 'off' | 'idle'): RunVisual {
  if (tone === 'ok') return 'ok';
  if (tone === 'warn') return 'attention';
  if (tone === 'err') return 'error';
  if (tone === 'off') return 'paused';
  return 'neutral';
}

export function taskStatusVisual(status: string): RunVisual {
  // 'resolved' is closure, not an error or a shrug: a human recorded that the
  // work reached the repository even though the task itself failed.
  if (status === 'completed' || status === 'resolved') return 'ok';
  if (status === 'running') return 'running';
  if (status === 'awaiting_approval' || status === 'needs_merge' || status === 'land_blocked') return 'attention';
  if (status === 'failed') return 'error';
  if (status === 'paused_for_collaboration' || status === 'paused_for_quota' || status === 'paused_for_budget' || status === 'paused_for_fallback' || status === 'paused_for_environment') return 'paused';
  if (status === 'recovering' || status === 'reconciliation') return 'queued';
  if (status === 'pending') return 'queued';
  return 'neutral';
}

/** Inline status glyph; hosts may opt out of color to inherit their own tone. */
export function RunStatusGlyph({
  visual,
  size = 12,
  colored = true,
  style,
  title,
}: {
  visual: RunVisual;
  size?: number;
  colored?: boolean;
  style?: CSSProperties;
  title?: string;
}) {
  const { Icon } = RUN_VISUAL_META[visual];
  return (
    <span
      title={title}
      style={{
        verticalAlign: '-2px',
        ...(colored ? { color: runVisualColor(visual) } : {}),
        ...style,
      }}
    >
      <Icon size={size} />
    </span>
  );
}

/** Ambient status dot used by silent-when-healthy chrome. */
export function RunStatusDot({
  visual,
  size = 7,
  title,
}: {
  visual: RunVisual;
  size?: number;
  title?: string;
}) {
  return (
    <span
      title={title}
      style={{
        width: size,
        height: size,
        borderRadius: '50%',
        background: runVisualColor(visual),
        display: 'inline-block',
        flexShrink: 0,
      }}
    />
  );
}
