import { useState } from 'react';
import { useCloud } from '../cloud';

// "How did you hear about SoluTerminal?" on the new-account card: optional,
// one tap, asked once per account. It catches word of mouth that first-touch
// UTM tags can't. The answer (or "skipped") goes into the account's
// user_metadata.heard_about, next to first_touch, so any device sees it was
// asked. A local mark covers a save that failed. Sign-up itself never waits
// for it: the card shows only after the account exists.

export const HEARD_ABOUT_QUESTION = 'How did you hear about SoluTerminal?';

export const HEARD_ABOUT_OPTIONS = [
  { value: 'hacker_news', label: 'Hacker News' },
  { value: 'reddit', label: 'Reddit' },
  { value: 'x', label: 'X' },
  { value: 'friend', label: 'A friend' },
  { value: 'search', label: 'Search' },
  { value: 'other', label: 'Other' },
] as const;

export const HEARD_ABOUT_SKIPPED = 'skipped';

const DONE_KEY_PREFIX = 'solu.heardAboutDone.';

function markedDone(userId: string): boolean {
  try {
    return localStorage.getItem(`${DONE_KEY_PREFIX}${userId}`) === '1';
  } catch {
    return false;
  }
}

function markDone(userId: string) {
  try {
    localStorage.setItem(`${DONE_KEY_PREFIX}${userId}`, '1');
  } catch {
    /* storage blocked: the saved answer still hides it next time */
  }
}

export function HeardAboutPrompt() {
  const userId = useCloud((s) => s.session?.user?.id ?? null);
  const answered = useCloud((s) => {
    const meta = s.session?.user?.user_metadata as Record<string, unknown> | undefined;
    return typeof meta?.heard_about === 'string' && meta.heard_about !== '';
  });
  const saveHeardAbout = useCloud((s) => s.saveHeardAbout);
  const [done, setDone] = useState(false);

  if (!userId || answered || done || markedDone(userId)) return null;

  const pick = (answer: string) => {
    markDone(userId);
    setDone(true);
    // Fire and forget: the user has moved on, and a failed save only means
    // this device's mark keeps the question away.
    void saveHeardAbout(answer).catch(() => {});
  };

  return (
    <div className="cloud-gate-heard" role="group" aria-label={HEARD_ABOUT_QUESTION}>
      <span className="cloud-gate-heard-question">{HEARD_ABOUT_QUESTION}</span>
      <div className="cloud-gate-heard-options">
        {HEARD_ABOUT_OPTIONS.map((o) => (
          <button key={o.value} type="button" className="cloud-gate-heard-option" onClick={() => pick(o.value)}>
            {o.label}
          </button>
        ))}
        <button type="button" className="cloud-gate-link" onClick={() => pick(HEARD_ABOUT_SKIPPED)}>
          Skip
        </button>
      </div>
    </div>
  );
}
