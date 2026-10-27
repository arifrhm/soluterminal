import { useRef } from 'react';
import type { ReactNode } from 'react';
import { createPortal } from 'react-dom';
import { useDismiss } from './useDismiss';

// Shared shell for popovers anchored to status-bar chips: portal to body,
// placement up, viewport clamp, outside-close on `click` not mousedown, Esc.
// Extracted from the VersionPopover → AgentsPopover clone chain so the panes
// and ports popovers don't copy the same forty lines a third and fourth time.
// Renders with the .version-popover base class so the established panel
// styling (and .vp-section / .vp-section-title) applies unchanged.
export function FooterPopover({
  anchor,
  onClose,
  ariaLabel,
  className,
  width = 320,
  children,
}: {
  anchor: HTMLElement;
  onClose: () => void;
  ariaLabel: string;
  className?: string;
  width?: number;
  children: ReactNode;
}) {
  const popRef = useRef<HTMLDivElement | null>(null);

  // The click-not-mousedown ordering guarantee is documented in useDismiss.ts.
  useDismiss(onClose, {
    inside: [popRef, anchor],
    // Dialogs opened by this popover remain part of its interaction flow;
    // closing underneath one loses the user's place after confirmation.
    ignoreSelectors: ['.app-dialog-backdrop'],
    escapeStopPropagation: true,
  });

  const rect = anchor.getBoundingClientRect();
  const margin = 8;
  let left = rect.right - width;
  if (left < margin) left = margin;
  if (left + width > window.innerWidth - margin) {
    left = window.innerWidth - width - margin;
  }
  const bottom = window.innerHeight - rect.top + 6;

  return createPortal(
    <div
      ref={popRef}
      className={['version-popover', className].filter(Boolean).join(' ')}
      style={{ left, bottom, width }}
      role="dialog"
      aria-label={ariaLabel}
    >
      {children}
    </div>,
    document.body,
  );
}
