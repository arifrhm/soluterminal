import { expect, it, vi } from "vitest";
it("keeps reconnect identity within one page, never reuses a profile or duplicated tab identity", async () => {
  localStorage.setItem("solu.device-id", "shared-browser-profile");
  sessionStorage.setItem("solu.retirement-viewer", "copied-by-duplicate-tab");
  const first = await import("./terminalRetirementViewer");
  const id = first.terminalRetirementViewerId();
  expect(first.terminalRetirementViewerId()).toBe(id);
  expect(id).not.toBe("shared-browser-profile");
  expect(id).not.toBe("copied-by-duplicate-tab");
  vi.resetModules(); // A new page's module lifetime, with identical browser storage.
  const second = await import("./terminalRetirementViewer");
  expect(second.terminalRetirementViewerId()).not.toBe(id);
  expect(localStorage.getItem("solu.device-id")).toBe("shared-browser-profile");
});
