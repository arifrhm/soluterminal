import { useState } from 'react';
import { IconShare } from './ui/icons';
import { useStore } from '../store';
import { useCloud } from '../cloud';

// Notification test & banner-setup tool. Opened on demand from the
// 📤 button next to the bell — never auto-popped. We deliberately
// don't assume the user wants heads-up banners; for some users,
// silent tray delivery is what they want. The modal's role is to
// make the two things those who DO want banners need — (a) verify
// delivery works, (b) know how to raise Android's channel
// importance — easy to reach when they care, invisible when they
// don't.
//
// Tauri never sees this modal; the 📤 button is hidden on Tauri
// because OS-native notifications are handled by Windows' own
// permission + priority flow, not by Chrome's channel model.

export function BannerSetupModal({ onClose }: { onClose: () => void }) {
  const toast = useStore((s) => s.toast);
  const [sending, setSending] = useState(false);
  const [lastResult, setLastResult] = useState<string | null>(null);
  const [lastOk, setLastOk] = useState<boolean | null>(null);

  async function sendTest() {
    const bearer = useCloud.getState().session?.access_token;
    if (!bearer) {
      toast('Not signed in — can\'t test push', { kind: 'warn', icon: '⚠' });
      return;
    }
    setSending(true);
    setLastResult(null);
    setLastOk(null);
    try {
      const res = await fetch('/api/push/test', {
        method: 'POST',
        headers: { Authorization: `Bearer ${bearer}` },
      });
      if (!res.ok) {
        setLastResult(`HTTP ${res.status}`);
        setLastOk(false);
        return;
      }
      const body = await res.json() as {
        subscriptionsFound: number;
        pushesSent: number;
        errors: string[];
      };
      if (body.pushesSent > 0) {
        const extra = body.errors.length > 0 ? ` (${body.errors.length} stale subscription${body.errors.length === 1 ? '' : 's'} pruned)` : '';
        setLastResult(`Sent to ${body.pushesSent} device${body.pushesSent === 1 ? '' : 's'}${extra} — check your phone.`);
        setLastOk(true);
      } else if (body.subscriptionsFound === 0) {
        setLastResult('No subscriptions stored — toggle the bell off then on, then retry.');
        setLastOk(false);
      } else {
        setLastResult(`Cloud rejected every push: ${body.errors.join('; ')}`);
        setLastOk(false);
      }
    } catch (e) {
      setLastResult(`Request failed: ${(e as Error).message}`);
      setLastOk(false);
    } finally {
      setSending(false);
    }
  }

  return (
    <div className="banner-setup-scrim" onClick={onClose}>
      <div
        className="banner-setup-card"
        role="dialog"
        aria-modal="true"
        aria-labelledby="banner-setup-title"
        onClick={(e) => e.stopPropagation()}
      >
        <h2 id="banner-setup-title">Test &amp; configure notifications</h2>
        <p className="banner-setup-intro">
          Verify push delivery is reaching your phone, and optionally turn
          on banner-style alerts (the notification slides down from the
          top instead of silently landing in the tray).
        </p>

        <div className="banner-setup-testrow">
          <button
            type="button"
            className="banner-setup-test-btn"
            onClick={sendTest}
            disabled={sending}
          >
            {sending ? 'Sending…' : <><IconShare size={13} style={{ verticalAlign: '-2px' }} /> Send test notification</>}
          </button>
          {lastResult && (
            <div className={`banner-setup-result ${lastOk ? 'ok' : 'warn'}`}>
              {lastResult}
            </div>
          )}
        </div>

        <div className="banner-setup-section-head">
          If you want banners instead of silent tray notifications:
        </div>
        <ol className="banner-setup-steps">
          <li>Pull down the notification shade on your phone.</li>
          <li><b>Long-press</b> any soluterminal notification there.</li>
          <li>
            Tap the gear <b>⚙</b> icon (or <i>"Notification settings"</i>).
            You'll land on this app's notification-channel settings.
          </li>
          <li>
            Turn on the toggle that makes notifications pop up. The exact label
            varies by phone:
            <ul className="banner-setup-variants">
              <li><b>Pixel / stock Android:</b> enable <b>"Pop on screen"</b> (and <b>"Vibrate"</b> if you want haptic).</li>
              <li><b>Samsung (One UI):</b> set <b>Importance</b> to <b>"Urgent"</b> (or <b>"Alerts"</b>).</li>
              <li><b>Xiaomi / Oppo / other OEMs:</b> set <b>Importance</b> to <b>"High"</b>, <b>"Urgent"</b>, or enable <b>"Floating notifications"</b>.</li>
              <li>
                If you see multiple options like <i>Default</i> vs <i>Silent</i>,
                pick whichever includes sound and/or pop-up behavior.
              </li>
            </ul>
          </li>
          <li>
            Come back and tap <b>Send test notification</b> again. The next
            alert should appear as a banner at the top of your screen.
          </li>
        </ol>

        <p className="banner-setup-aside">
          This is a one-time Android setting. Once enabled, all future
          notifications show as banners automatically. Nothing else to
          configure.
        </p>

        <div className="banner-setup-actions">
          <button type="button" className="banner-setup-dismiss" onClick={onClose}>
            Close
          </button>
        </div>
      </div>
    </div>
  );
}
