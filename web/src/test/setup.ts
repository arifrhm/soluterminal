import '@testing-library/jest-dom/vitest';

// jsdom has no ResizeObserver. TopbarPopover (portaled popovers) constructs one
// to remeasure on resize/trigger-move; a no-op stand-in is enough for tests —
// the panel still mounts and is queryable (position is computed from
// getBoundingClientRect, which jsdb returns zeros for).
class ResizeObserverMock {
  observe() {}
  unobserve() {}
  disconnect() {}
}
globalThis.ResizeObserver = ResizeObserverMock as unknown as typeof ResizeObserver;

