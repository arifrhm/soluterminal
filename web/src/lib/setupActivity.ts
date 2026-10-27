// When the user last started an agent setup step: an install or sign-in pane
// from the checklist, the banner or a Start button, or an agent started before
// it was signed in. Kept apart from the flows that record it so the install
// helpers and the consent question (agentIntegration.ts) can share it without
// importing each other.

let lastSetupStartMs = 0;

export function markSetupStarted(now: number = Date.now()): void {
  lastSetupStartMs = now;
}

export function setupStartedWithin(windowMs: number, now: number = Date.now()): boolean {
  return lastSetupStartMs > 0 && now - lastSetupStartMs < windowMs;
}

export function resetSetupActivityForTests(): void {
  lastSetupStartMs = 0;
}
