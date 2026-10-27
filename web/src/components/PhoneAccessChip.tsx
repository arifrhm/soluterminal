import { useStore } from '../store';
import { usePhoneAccessAllowed } from '../entitlements';
import { usePhoneAccessStatus, chipStateFor, isDesktopShell } from '../cloudPair';
import { IconSmartphone } from './ui/icons';

// Phone-access status for the bottom StatusBar. Silent-when-idle, icon-first,
// matching the healthy AgentsChip precedent:
//   not set up   → NOTHING (return null) — discovery lives in Preferences, the
//                  first-run banner and the setup checklist; the footer shouldn't
//                  carry a permanent affordance for a feature ~90% never opt into.
//   online       → a calm green phone glyph, no word   ("Phone can control this…")
//   connecting   → spinner + "Pairing…"
//   signin       → amber phone glyph + "Phone sign-in"
// Only the transient/attention states spend a word; connected is a quiet glyph.
// (It used to be a dot + the word "Phone" — itself renamed from "Cloud" to avoid
// colliding with the hosting connection chip.) Click opens the pair panel.
// Desktop-shell only, and renders its own leading separator so the footer has no
// dangling "·" when it's hidden.
export function PhoneAccessChip() {
  const setPhoneAccessOpen = useStore((s) => s.setPhoneAccessOpen);
  // Re-poke the poller while the pair panel is open so the glyph flips to
  // "connected" in lockstep with the panel's success state.
  const panelOpen = useStore((s) => s.phoneAccessOpen);
  const { status } = usePhoneAccessStatus({ pokeKey: panelOpen });
  const allowed = usePhoneAccessAllowed();

  if (!isDesktopShell() || !allowed) return null;

  const chip = chipStateFor(status);
  if (chip.kind === 'not-set-up') return null;

  const label = chip.kind === 'connecting'
    ? 'Pairing…'
    : chip.kind === 'signin'
      ? 'Phone sign-in'
      : null;
  const title =
    chip.kind === 'online'
      ? 'Phone can control this computer'
      : chip.kind === 'connecting'
        ? 'Connecting your phone…'
        : 'Phone access: sign-in needed';

  return (
    <>
      <span className="statusbar-sep">·</span>
      <button
        type="button"
        className={`statusbar-phone ${chip.kind}`}
        onClick={() => setPhoneAccessOpen(true)}
        title={title}
        aria-label={title}
        data-tauri-drag-region={false}
      >
        {chip.kind === 'connecting'
          ? <span className="statusbar-phone-spinner" aria-hidden />
          : <IconSmartphone size={13} />}
        {label && <span>{label}</span>}
      </button>
    </>
  );
}
