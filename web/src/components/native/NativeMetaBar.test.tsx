import { render } from '@testing-library/react';
import { describe, expect, it } from 'vitest';
import type { NativeEvent } from '../../native/nativeEvents';
import type { NativeMetaBarFields } from '../../native/metaBarFields';
import { NativeMetaBar } from './NativeEventViews';

const usage: NativeEvent = {
  seq: 1,
  paneId: 'p-codex',
  type: 'usage',
  fromReplay: false,
  inputTokens: 12_345,
  cachedInputTokens: 8_000,
  outputTokens: 345,
};

const rateLimit: NativeEvent = {
  seq: 2,
  paneId: 'p-codex',
  type: 'rate_limit',
  fromReplay: false,
  resetsAt: Math.floor(Date.now() / 1000) + 7 * 24 * 60 * 60,
  rateLimitKind: 'Codex weekly limit',
};

const allFields: NativeMetaBarFields = {
  model: true,
  context: true,
  tokens: true,
  cost: true,
  cache: true,
  resets: true,
  cwd: true,
};

function renderMetaBar(fields: NativeMetaBarFields) {
  return render(
    <NativeMetaBar
      phase="idle"
      label=""
      startedAt={null}
      usage={usage}
      rateLimit={rateLimit}
      fields={fields}
      model="GPT 5.6-sol"
      contextPct={0.42}
      cost={0.18}
      cwd="…/project"
      cwdFull="/work/project"
    />,
  );
}

describe('NativeMetaBar provider-neutral field settings', () => {
  it('hides every supplied vital when the user disables every field', () => {
    const { container } = renderMetaBar({
      model: false,
      context: false,
      tokens: false,
      cost: false,
      cache: false,
      resets: false,
      cwd: false,
    });
    expect(container.textContent).toBe('');
  });

  it('shows real Codex-compatible values in the shared configured order', () => {
    const { container } = renderMetaBar(allFields);
    const text = container.textContent || '';
    expect(text).toContain('GPT 5.6-sol');
    expect(text).toContain('ctx 42%');
    expect(text).toContain('↑12k');
    expect(text).toContain('↓345');
    expect(text).toContain('⚡8k');
    expect(text).toContain('$0.18');
    expect(text).toContain('resets ');
    expect(text).toContain('…/project');
    expect(text.indexOf('GPT 5.6-sol')).toBeLessThan(text.indexOf('ctx 42%'));
    expect(text.indexOf('ctx 42%')).toBeLessThan(text.indexOf('↑12k'));
  });

  it('holds the context slot with ctx — only while a post-compact reading is pending', () => {
    const { container, rerender } = render(
      <NativeMetaBar
        phase="idle"
        label=""
        startedAt={null}
        fields={allFields}
        contextMeasuring
      />,
    );
    const measuring = container.querySelector('.native-metabar-ctx');
    expect(measuring).toHaveTextContent('ctx —');
    expect(measuring).toHaveClass('is-measuring');

    rerender(
      <NativeMetaBar phase="idle" label="" startedAt={null} fields={allFields} />,
    );
    expect(container.querySelector('.native-metabar-ctx')).toBeNull();

    rerender(
      <NativeMetaBar phase="idle" label="" startedAt={null} fields={allFields} contextPct={0.12} />,
    );
    expect(container.querySelector('.native-metabar-ctx')).toHaveTextContent('ctx 12%');
  });
});
