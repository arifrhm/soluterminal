import { PortPanel } from './PortPanel';
import { FooterPopover } from './FooterPopover';

// Ports popover for the status bar's dev-servers chip — hosts the SAME
// PortPanel the sidebar's Ports section renders (filter toggle, open-in-tab
// links, ⊕ open-as-tile), so the footer entry point works even when that
// sidebar section is collapsed or hidden by the user's layout prefs.
export function PortsPopover({
  anchor,
  onClose,
}: {
  anchor: HTMLElement;
  onClose: () => void;
}) {
  return (
    <FooterPopover anchor={anchor} onClose={onClose} ariaLabel="Services" className="ports-popover">
      <div className="vp-section">
        <div className="vp-section-title">Services</div>
        <PortPanel />
      </div>
    </FooterPopover>
  );
}
