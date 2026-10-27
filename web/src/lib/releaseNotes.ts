import curatedNotes from './releaseNotes.json';
import { parseNotes, type NoteLine } from './updateNotes';

// Release notes written for users, by version. Add an entry to
// releaseNotes.json for every release: publish.sh signs that same text into
// update.json's public "notes" field and refuses to publish a version without
// one, so the app and the manifest can never drift apart or fall back to raw
// commit subjects ("Record the entitlements publish as a non-starting boot
// step"), which mean nothing to a public beta user.
const CURATED: Record<string, string[]> = curatedNotes;

const GENERIC: NoteLine[] = [{ text: 'Improvements and fixes.', kind: 'other' }];

// Notes to show for a version. Curated notes win; team installs fall back to
// the raw commit list (they know what it means), everyone else to a generic
// line rather than commit subjects.
export function userFacingNotes(
  version: string | undefined | null,
  rawNotes: string | undefined | null,
  team: boolean,
): NoteLine[] {
  const curated = version ? CURATED[version.replace(/^v/, '')] : undefined;
  if (curated) return curated.map((text) => ({ text, kind: 'other' }));
  if (team) return parseNotes(rawNotes);
  return GENERIC;
}
