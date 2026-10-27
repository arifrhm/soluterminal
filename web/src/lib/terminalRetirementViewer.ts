// A browser profile/device ID is shared by tabs. Retirement evidence must
// identify one page's local drafts: a sibling tab or a reload cannot attest
// that the old page had no unsent input. Keep this in memory, never storage.
let viewerId: string | undefined;
export function terminalRetirementViewerId(): string {
  if (!viewerId) {
    viewerId =
      typeof crypto.randomUUID === "function"
        ? crypto.randomUUID()
        : Array.from(crypto.getRandomValues(new Uint8Array(16)), (byte) =>
            byte.toString(16).padStart(2, "0"),
          ).join("");
  }
  return viewerId;
}
