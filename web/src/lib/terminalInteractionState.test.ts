import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import {
  abandonTerminalRetirement,
  beginTerminalInteraction,
  beginTerminalRetirement,
  cancelTerminalRetirement,
  clearTerminalRecoveredInput,
  holdTerminalInput,
  releaseTerminalRetirement,
  terminalHasPendingInteraction,
  terminalInputAllowed,
  terminalRecoveredInput,
} from "./terminalInteractionState";
import {
  beginQuickReplyAttachment,
  quickReplyHasPendingInteraction,
  setQuickReplyDraft,
} from "./quickReplyDrafts";
const key = '["local","owner1"]';
const encoder = new TextEncoder();
function probe(extra = {}) {
  return {
    token: "unique-token",
    expiresAtMs: Date.now() + 3000,
    valid: () => true,
    pending: () => false,
    cancel: vi.fn(),
    flush: vi.fn((_bytes: Uint8Array) => true),
    ...extra,
  };
}
beforeEach(() => {
  vi.useFakeTimers();
  vi.setSystemTime(100_000);
});
afterEach(() => {
  abandonTerminalRetirement(key);
  clearTerminalRecoveredInput(key);
  setQuickReplyDraft(key, "process", "");
  vi.useRealTimers();
});
describe("owner retirement view lease", () => {
  it("copying an older recovery snapshot does not discard newer queued input", () => {
    beginTerminalRetirement(key, probe());
    holdTerminalInput(key, encoder.encode("first"));
    abandonTerminalRetirement(key);
    clearTerminalRecoveredInput(key, "older text");
    expect(terminalRecoveredInput(key)).toBe("first");
    clearTerminalRecoveredInput(key, "first");
    expect(terminalRecoveredInput(key)).toBe("");
  });

  it("rechecks recovered bytes when a replacement probe invalidates the prior socket", () => {
    let priorLive = true;
    beginTerminalRetirement(key, probe({ valid: () => priorLive }));
    holdTerminalInput(key, encoder.encode("unsent"));
    priorLive = false;
    expect(beginTerminalRetirement(key, probe({ token: "new-socket" }))).toBe(
      false,
    );
    expect(terminalRecoveredInput(key)).toBe("unsent");
  });

  it("keeps recovered input private to the original principal", () => {
    const options = probe({ recoveryKey: "original-principal" });
    beginTerminalRetirement(key, options);
    holdTerminalInput(key, encoder.encode("private draft"));
    abandonTerminalRetirement(key);
    expect(terminalRecoveredInput(key)).toBe("");
    expect(terminalRecoveredInput("new-principal")).toBe("");
    expect(terminalRecoveredInput("original-principal")).toBe("private draft");
    expect(
      beginTerminalRetirement(
        key,
        probe({ recoveryKey: "original-principal" }),
      ),
    ).toBe(false);
    clearTerminalRecoveredInput("original-principal");
  });

  it("rejects pending asynchronous input on exact session, with independent concurrent leases", () => {
    const doneA = beginTerminalInteraction(key)!;
    const doneB = beginTerminalInteraction(key)!;
    expect(beginTerminalRetirement(key, probe())).toBe(false);
    doneA();
    doneA();
    expect(terminalHasPendingInteraction(key)).toBe(true);
    doneB();
    expect(beginTerminalRetirement(key, probe())).toBe(true);
  });
  it("does not flush input until exact release and cancels once without duplicate re-ack", () => {
    const options = probe();
    expect(beginTerminalRetirement(key, options)).toBe(true);
    expect(holdTerminalInput(key, encoder.encode("hello"))).toBe(true);
    expect(holdTerminalInput(key, encoder.encode("\r"))).toBe(true);
    expect(options.cancel).toHaveBeenCalledTimes(1);
    expect(beginTerminalRetirement(key, options)).toBe(false);
    releaseTerminalRetirement(key, "other-token");
    expect(options.flush).not.toHaveBeenCalled();
    releaseTerminalRetirement(key, options.token);
    expect(
      options.flush.mock.calls.map(([b]) => new TextDecoder().decode(b)),
    ).toEqual(["hello", "\r"]);
    expect(terminalInputAllowed(key)).toBe(true);
  });
  it("retains raced input across disconnect or identity change, never replays into successor", () => {
    let live = true;
    const options = probe({ valid: () => live });
    expect(beginTerminalRetirement(key, options)).toBe(true);
    const split = encoder.encode("大");
    holdTerminalInput(key, split.slice(0, 1));
    holdTerminalInput(key, split.slice(1));
    live = false;
    releaseTerminalRetirement(key, options.token);
    expect(options.flush).not.toHaveBeenCalled();
    expect(terminalRecoveredInput(key)).toBe("大");
    expect(beginTerminalRetirement(key, probe())).toBe(false);
    clearTerminalRecoveredInput(key);
    expect(beginTerminalRetirement(key, probe())).toBe(true);
  });
  it("synchronously expires throttled timers and preserves unconfirmed bytes", () => {
    const options = probe();
    beginTerminalRetirement(key, options);
    holdTerminalInput(key, encoder.encode("draft"));
    vi.setSystemTime(options.expiresAtMs + 1);
    expect(terminalInputAllowed(key)).toBe(true);
    expect(terminalRecoveredInput(key)).toBe("draft");
    expect(options.flush).not.toHaveBeenCalled();
  });
  it("uses bounded relative leases despite server clock skew without extending repeats", () => {
    const options = probe({ leaseMs: 5000, expiresAtMs: Date.now() - 60_000 });
    expect(beginTerminalRetirement(key, options)).toBe(true);
    vi.setSystemTime(Date.now() + 4000);
    expect(beginTerminalRetirement(key, options)).toBe(true);
    vi.setSystemTime(Date.now() + 1001);
    expect(terminalInputAllowed(key)).toBe(true);
    expect(
      beginTerminalRetirement(
        key,
        probe({ leaseMs: 5000, expiresAtMs: Date.now() + 60_000 }),
      ),
    ).toBe(true);
  });
  it("rejects malformed, long, expired and >5 second leases", () => {
    for (const fields of [
      { leaseMs: 0 },
      { leaseMs: -1 },
      { leaseMs: NaN },
      { leaseMs: 5001 },
      { token: "" },
      { token: "x".repeat(257) },
      { expiresAtMs: NaN },
      { expiresAtMs: Date.now() },
      { expiresAtMs: Date.now() + 5001 },
      { valid: () => false },
    ]) {
      expect(beginTerminalRetirement(key, probe(fields))).toBe(false);
    }
  });
  it("declines new async actions; hidden composer work cancels without dropping draft", () => {
    const options = probe();
    beginTerminalRetirement(key, options);
    expect(beginTerminalInteraction(key)).toBeNull();
    setQuickReplyDraft(key, "process", "keep this draft");
    expect(quickReplyHasPendingInteraction(key)).toBe(true);
    expect(options.cancel).toHaveBeenCalledTimes(1);
    releaseTerminalRetirement(key, options.token);
    expect(
      beginTerminalRetirement(
        key,
        probe({ pending: () => quickReplyHasPendingInteraction(key) }),
      ),
    ).toBe(false);
    setQuickReplyDraft(key, "process", "");
    const done = beginQuickReplyAttachment(key);
    expect(
      beginTerminalRetirement(
        key,
        probe({ pending: () => quickReplyHasPendingInteraction(key) }),
      ),
    ).toBe(false);
    done();
    expect(
      beginTerminalRetirement(
        key,
        probe({ pending: () => quickReplyHasPendingInteraction(key) }),
      ),
    ).toBe(true);
    cancelTerminalRetirement(key);
  });
});
