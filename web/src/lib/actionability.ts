// Actionability heuristics for ARIA observations / pane tails.
//
// Pulled out of the (since removed) ARIA panel so the unified attention selector
// (useAttentionItems in AttentionInbox.tsx) can run the SAME check
// the panel uses to decide "should this row render?". Without this,
// the topbar bell and the ARIA panel disagree on the count: the
// selector returns every pane in attention state, but the panel's
// ObservationRow returns null for tails it judges non-actionable
// (Claude idle, just-finished prose, no real prompt). Running the
// check at the selector level gives both surfaces the same number.

// extractQuickReplyDigits scans the tail for Claude Code's numbered-
// menu pattern. Returns single-digit strings in the order they appear.
// Safety checks to avoid prose false positives:
//   - Each candidate line must START with the digit (optionally after
//     a focus marker ❯/›/>), not have it mid-paragraph.
//   - Line must be reasonably short (<200 chars after cleanup).
//   - Digits must be CONSECUTIVE STARTING FROM 1 (1,2 or 1,2,3,…) —
//     real Claude Code menus never skip numbers; prose numbered
//     lists like "1. Take a screenshot ... 6. Last step" fail this.
//   - At least one line must have the ❯ focus marker on it —
//     Claude always highlights the currently-selected option, prose
//     numbered lists don't.
export function extractQuickReplyDigits(tail: string): string[] {
  const seen = new Set<string>();
  const out: string[] = [];
  const boxChars = /[╭╮╰╯─│┤├┬┴┐┌└┘━┃┏┓┗┛═║╔╗╚╝]/g;
  const lines = tail
    .split('\n')
    .map((l) => l.replace(boxChars, ' ').replace(/\s+/g, ' ').trim())
    .filter((l) => l.length > 0 && l.length < 200);
  const pattern = /^[❯❭›▶>]?\s*(\d)\s*[\.:]\s+\S/;
  const focusPattern = /^[❯❭›▶>]\s*\d\s*[\.:]/;
  let hasFocusMarker = false;
  for (const l of lines) {
    if (focusPattern.test(l)) hasFocusMarker = true;
    const m = l.match(pattern);
    if (m && !seen.has(m[1])) {
      seen.add(m[1]);
      out.push(m[1]);
    }
  }
  if (!hasFocusMarker) return [];
  if (out.length < 2) return [];
  const sorted = [...out].sort((a, b) => Number(a) - Number(b));
  for (let i = 0; i < sorted.length; i++) {
    if (sorted[i] !== String(i + 1)) return [];
  }
  return out;
}

function truncate(s: string, n: number): string {
  return s.length <= n ? s : s.slice(0, n - 1) + '…';
}

// isChromeLine detects TUI status-footer text that should be filtered
// out of previews. Uses keyword presence (2+) rather than \s-separated
// phrases so it still works when Claude's ANSI-positioned text arrives
// with no spaces between words.
function isChromeLine(l: string): boolean {
  const footerKeywords = /shortcuts|cancel|amend|explain|shift[+\-]?tab|accept\s*edits|cycle/gi;
  const footerMatches = l.match(footerKeywords);
  if (footerMatches && footerMatches.length >= 2) return true;

  if (/\blimit\b/i.test(l) && /\breset/i.test(l)) return true;
  if (/\bctx\s*:\s*\d+\s*%/i.test(l)) return true;
  if (/new\s*task\?/i.test(l) && /\/clear/i.test(l)) return true;
  if (/How\s*is\s*Claude\s*doing/i.test(l)) return true;
  if (/\bBad\b/.test(l) && /\bFine\b/.test(l) && /\bGood\b/.test(l) && /\bDismiss\b/i.test(l)) return true;
  if (/\bAuto[-\s]*updating/i.test(l)) return true;
  if (l.includes('✻')) return true;
  if (/↓\s*[\d.]+\s*k?\s*tokens/i.test(l)) return true;
  if (/^[A-Z][a-z]+(ed|ing|zed|zing|ling|ting|ning|ming)…/.test(l)) return true;
  if (/\(?\s*ctrl\s*[+\-]?\s*[a-z]\s*(to\s*)?(expand|collapse)\s*\)?/i.test(l)) return true;
  if (/\b(searched|read|edited|wrote|ran|called)\b.*\b\d+\b.*\b(pattern|file|time|command)s?\b/i.test(l)) return true;
  return false;
}

// pickPreviewLine returns a short glance-friendly summary from the
// captured tail. See git history (AriaPanel.tsx) for the full strategy
// rationale; in short, walks backward accumulating meaningful content
// up to a character budget.
export function pickPreviewLine(tail: string): string {
  const boxChars = /[╭╮╰╯─│┤├┬┴┐┌└┘━┃┏┓┗┛═║╔╗╚╝]/g;
  const decorative = /^[\s•·\-_=*<>]+$/;
  const cleaned = tail
    .split('\n')
    .map((l) => l.replace(boxChars, ' ').replace(/\s+/g, ' ').trim())
    .filter((l) => l.length >= 3 && !decorative.test(l) && !isChromeLine(l));

  if (cleaned.length === 0) return '';

  const taken: string[] = [];
  let budget = 140;
  for (let i = cleaned.length - 1; i >= 0 && taken.length < 5; i--) {
    const l = cleaned[i].replace(/^[>❯›]\s*/, '');
    if (l.length === 0) continue;
    const cost = l.length + (taken.length > 0 ? 3 : 0);
    if (cost > budget) {
      if (taken.length === 0) {
        taken.unshift(truncate(l, budget));
      }
      break;
    }
    taken.unshift(l);
    budget -= cost;
  }
  return taken.join(' · ');
}

// isActionable is the unified "should this attention item surface?"
// check. An item is actionable if its tail has either an explicit
// question mark in the preview OR a numbered-menu shape — the same
// two conditions ObservationRow uses to decide whether to render.
//
// A pane in attention state with no tail yet (sub-second window
// before SSE delivers) is treated as actionable so the bell still
// surfaces it; the panel hides it via its own preview === '' check.
export function isActionable(tailText: string | undefined): boolean {
  if (!tailText) return true;
  // Native-pane observations are structured summaries built by the daemon
  // (nativeObservationText: "Permission request: Bash — git status"), not
  // scraped TUI text. They carry neither a "?" nor a numbered menu but are
  // always a real approval waiting on someone — actionable by construction.
  if (/^Permission request:/m.test(tailText)) return true;
  const preview = pickPreviewLine(tailText);
  if (!preview) return false;
  if (preview.includes('?')) return true;
  if (extractQuickReplyDigits(tailText).length > 0) return true;
  return false;
}
