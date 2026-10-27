// Composer icon set — small, dependency-free inline SVGs (lucide-style,
// currentColor stroke) so the voice-first chat composer renders crisp,
// platform-consistent glyphs. Lifted from the ARIA comms composer
// (aria/desktop/src/components/composer/icons.tsx) — the reference design we
// agreed to reuse. The buttons that use these carry their own aria-label/title,
// so every icon is aria-hidden + focusable=false.

import type { SVGProps } from 'react';

type IconProps = SVGProps<SVGSVGElement> & { size?: number };

function svgProps({ size = 20, ...rest }: IconProps): SVGProps<SVGSVGElement> {
  return {
    width: size,
    height: size,
    viewBox: '0 0 24 24',
    fill: 'none',
    stroke: 'currentColor',
    strokeWidth: 2,
    strokeLinecap: 'round',
    strokeLinejoin: 'round',
    'aria-hidden': true,
    focusable: false,
    ...rest,
  };
}

/** Attach (📎). */
export function IconPaperclip(p: IconProps): React.ReactElement {
  return (
    <svg {...svgProps(p)}>
      <path d="m21.44 11.05-9.19 9.19a6 6 0 0 1-8.49-8.49l8.57-8.57A4 4 0 1 1 18 8.84l-8.59 8.57a2 2 0 0 1-2.83-2.83l8.49-8.48" />
    </svg>
  );
}

/** Send paper-plane — the has-content primary button. */
export function IconSend(p: IconProps): React.ReactElement {
  return (
    <svg {...svgProps(p)}>
      <path d="M22 2 11 13" />
      <path d="M22 2 15 22l-4-9-9-4 20-7z" />
    </svg>
  );
}

/** Stop — shown while a turn is in flight (interrupt) / hands-free recording. */
export function IconStop(p: IconProps): React.ReactElement {
  return (
    <svg {...svgProps({ ...p, fill: 'currentColor', stroke: 'none' })}>
      <rect x="6" y="6" width="12" height="12" rx="2.5" />
    </svg>
  );
}
