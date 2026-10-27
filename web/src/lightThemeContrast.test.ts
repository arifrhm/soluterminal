import { readFileSync } from 'node:fs';
import { describe, expect, it } from 'vitest';

// Light theme readability: every text colour token used on light surfaces
// must clear WCAG AA (4.5:1) on the main light surfaces, and the terminal
// surface must never fall back to black in light.

const css = readFileSync('src/styles.css', 'utf8');

function block(selector: string): Record<string, string> {
  const start = css.indexOf(`${selector} {`);
  expect(start, selector).toBeGreaterThanOrEqual(0);
  const end = css.indexOf('\n}', start);
  const body = css.slice(start, end).replace(/\/\*[\s\S]*?\*\//g, '');
  const out: Record<string, string> = {};
  for (const m of body.matchAll(/(--[\w-]+)\s*:\s*(#[0-9a-fA-F]{6})\s*;/g)) out[m[1]] = m[2];
  return out;
}

function luminance(hex: string): number {
  const c = [1, 3, 5].map((i) => parseInt(hex.slice(i, i + 2), 16) / 255)
    .map((v) => (v <= 0.03928 ? v / 12.92 : ((v + 0.055) / 1.055) ** 2.4));
  return 0.2126 * c[0] + 0.7152 * c[1] + 0.0722 * c[2];
}
function contrast(a: string, b: string): number {
  const [hi, lo] = [luminance(a), luminance(b)].sort((x, y) => y - x);
  return (hi + 0.05) / (lo + 0.05);
}

const light = block(":root[data-theme='light']");

const TEXT_TOKENS = [
  '--fg', '--fg-dim', '--muted', '--accent', '--accent-2', '--danger', '--ok', '--warn', '--warn-dim',
  '--magenta', '--syn-str', '--syn-num', '--syn-key', '--syn-kw', '--syn-bool', '--syn-type',
  '--kb-recording-fg', '--kb-conflict-fg', '--update-shell-fg', '--mcp-sse-fg', '--mcp-stdio-fg',
  '--status-needs-info-fg', '--status-fixed-fg', '--status-progress-fg',
];

describe('light theme contrast', () => {
  for (const token of TEXT_TOKENS) {
    it(`${token} clears 4.5:1 on --bg and --bg-1`, () => {
      const fg = light[token];
      expect(fg, `${token} must have a light value`).toBeDefined();
      for (const surface of ['--bg', '--bg-1']) {
        expect(contrast(fg, light[surface]), `${token} on ${surface}`).toBeGreaterThanOrEqual(4.5);
      }
    });
  }

  it('white primary-button text clears 4.5:1 on --accent-2', () => {
    expect(contrast('#ffffff', light['--accent-2'])).toBeGreaterThanOrEqual(4.5);
  });

  it('formerly hard-coded colours now come from theme tokens', () => {
    expect(css).toMatch(/\.keybindings-recording\s*\{[^}]*color:\s*var\(--kb-recording-fg\)/);
    expect(css).toMatch(/\.keybindings-conflict\s*\{[^}]*color:\s*var\(--kb-conflict-fg\)/);
    expect(css).toMatch(/\.update-badge--shell\s*\{[^}]*color:\s*var\(--update-shell-fg\)/);
    expect(css).toContain('.mcp-type-sse { color: var(--mcp-sse-fg); }');
    expect(css).toContain('.mcp-type-stdio { color: var(--mcp-stdio-fg); }');
  });

  it('the terminal surface and its veils follow the theme, never a fixed black', () => {
    expect(light['--terminal-background-default']).toBe('#ffffff');
    expect(css).toMatch(/\.pane-catchup\s*\{[^}]*background:\s*var\(--terminal-background, var\(--terminal-background-default\)\)/);
    expect(css).toMatch(/\.pane-settling\s*\{[^}]*background:\s*var\(--terminal-background, var\(--terminal-background-default\)\)/);
    expect(css).not.toMatch(/\.pane-(catchup|settling)\s*\{[^}]*background:\s*#000/);
  });
});
