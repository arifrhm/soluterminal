// How long an event-stream rebuild loop waits after the stream gave up.
// Consecutive failures back off exponentially (2 s, 4 s, 8 s … 30 s, as the
// private relay channel does), so a computer that refuses this device
// (device_not_approved / 401) or is down is asked a few times a minute at
// most, not every 2 s. A stream that opens resets it.

export const REBUILD_BASE_MS = 2_000;
export const REBUILD_MAX_MS = 30_000;

export class RebuildBackoff {
  private failures = 0;

  /** Records a failure; returns the delay before the next rebuild. */
  fail(): number {
    this.failures++;
    return Math.min(REBUILD_MAX_MS, REBUILD_BASE_MS * 2 ** (this.failures - 1));
  }

  /** The stream opened. */
  reset(): void {
    this.failures = 0;
  }
}
