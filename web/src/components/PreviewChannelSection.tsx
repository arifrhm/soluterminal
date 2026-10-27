// Preview / Stable channel switch inside the version popover. Renders nothing
// unless this machine can build previews (it has a soluterminal source
// checkout) and the viewer is the machine's owner — locally, via the owner
// token, or through the owner-only cloud relay (the desktop app in cloud
// mode). See daemon/preview_channel.go.
import { useState } from 'react';
import { useStore } from '../store';
import { switchPreviewChannel, usePreviewChannel } from '../lib/previewChannel';
import { openInbox } from './inbox/InboxPanel';
import type { PreviewChannelStatus } from '../types';

function plural(n: number, word: string) {
  return `${n} ${word}${n === 1 ? '' : 's'}`;
}

// follow-main prints "==> step" progress lines and "follow-main: reason"
// failures; show them without the markers.
function cleanLine(line: string) {
  return line.replace(/^==>\s*/, '').replace(/^follow-main:\s*/, '');
}

export function PreviewChannelSection() {
  if (typeof __SOLU_LOCAL_PREVIEW__ !== 'undefined' && __SOLU_LOCAL_PREVIEW__) {
    return <div className="vp-section vp-channel" aria-label="Local preview">
      <div className="vp-section-title">Local development preview</div>
      <div className="vp-summary">Changes appear here as you edit. This preview has its own workspace.</div>
      <div className="vp-summary">Your everyday app stays separate. Live Preview is a verified update to that app.</div>
    </div>;
  }
  return <LivePreviewChannelSection />;
}

function LivePreviewChannelSection() {
  const st = usePreviewChannel();
  const toast = useStore((s) => s.toast);
  const [pending, setPending] = useState<'preview' | 'stable' | null>(null);
  if (!st?.available) return null;

  const onPreview = st.channel === 'preview';
  const building = !!st.job;
  const ci = st.main.ci;
  const lastFailed = !building && !!st.jobLine?.startsWith('follow-main:');

  async function go(target: 'preview' | 'stable') {
    setPending(target);
    try {
      await switchPreviewChannel(target);
    } catch (e) {
      toast(`Couldn't start the switch: ${e instanceof Error ? e.message : String(e)}`, { kind: 'warn' });
    } finally {
      setPending(null);
    }
  }

  return (
    <div className="vp-section vp-channel" aria-label="Release channel">
      <div className="vp-section-title">
        Channel <span className={`vp-channel-pill${onPreview ? ' is-preview' : ''}`}>{onPreview ? 'Preview' : 'Stable'}</span>
      </div>
      <div className="vp-summary">
        {onPreview
          ? `Preview ${st.running} — ${plural(st.ahead, 'change')} ahead of Stable ${st.stable}`
          : `Stable ${st.running} — the version everyone gets`}
      </div>
      <div className="vp-summary vp-channel-dim">
        This updates your running app after active work finishes. Local development preview opens separately.
      </div>

      {building ? (
        <div className="vp-summary vp-channel-progress" role="status">
          {st.job === 'building-preview' ? 'Building preview on this PC…' : 'Switching back to Stable…'}
          {st.jobLine && <div className="vp-channel-line">{cleanLine(st.jobLine)}</div>}
          <div className="vp-channel-line">Your panes stay open; the app reconnects when it's done.</div>
        </div>
      ) : (
        <>
          {st.newChanges > 0 && (
            <div className="vp-summary">
              Latest preview {st.main.mainVersion}: {plural(st.newChanges, 'new change')}, tested by GitHub
            </div>
          )}
          {st.newChanges === 0 && ci === 'pending' && (
            <div className="vp-summary vp-channel-dim">Newest merged changes are still being tested by GitHub.</div>
          )}
          {st.newChanges === 0 && ci === 'failed' && (
            <div className="vp-summary vp-channel-dim">Newest merged changes failed GitHub's tests — not offered until fixed.</div>
          )}
          {st.main.error && <div className="vp-summary vp-channel-dim">{st.main.error}</div>}
          {lastFailed && <div className="vp-summary vp-channel-warn">Last switch: {cleanLine(st.jobLine || '')}</div>}
          <UnreleasedList st={st} onPreview={onPreview} />
          <div className="vp-channel-actions">
            {st.newChanges > 0 && (
              <button type="button" className="vp-apply" disabled={!!pending} onClick={() => void go('preview')}>
                {onPreview ? 'Update to latest preview' : 'Switch live app to Preview'}
              </button>
            )}
            {onPreview && (
              <button type="button" className="vp-skip" disabled={!!pending} onClick={() => void go('stable')}>
                Switch back to Stable
              </button>
            )}
          </div>
        </>
      )}
    </div>
  );
}

// "Not released yet": what Preview brings before everyone gets it, and how to
// try each change, so a release is published only after the owner has used
// it on their own app (AGENTS.md "Before a release").
function UnreleasedList({ st, onPreview }: { st: PreviewChannelStatus; onPreview: boolean }) {
  const list = st.unreleased ?? [];
  if (list.length === 0) return null;
  const toTry = list.filter((c) => c.howToTry).length;
  return (
    <details className="vp-unreleased" open={onPreview && toTry > 0}>
      <summary>
        Not released yet: {plural(list.length, 'change')}
        {toTry > 0 ? ` · ${toTry} to try` : ''}
      </summary>
      <ul className="vp-unreleased-list">
        {list.map((c) => (
          <li key={c.sha}>
            <div className="vp-unreleased-title">
              {c.pr ? <span className="vp-unreleased-pr">#{c.pr}</span> : null} {c.title}
              <span className={`vp-unreleased-tag${c.installed ? ' is-on' : ''}`}>
                {c.installed ? 'on this app' : onPreview ? 'next preview' : 'in Preview'}
              </span>
            </div>
            {c.howToTry && <div className="vp-unreleased-try">{c.howToTry}</div>}
          </li>
        ))}
      </ul>
      <div className="vp-summary vp-channel-dim">
        Something wrong after trying these?{' '}
        <button type="button" className="vp-link" onClick={() => openInbox('compose')}>Report a problem</button>
        {' '}before the release goes out.
      </div>
    </details>
  );
}
