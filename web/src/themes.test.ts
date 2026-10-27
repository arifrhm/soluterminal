import { describe, expect, it } from 'vitest';
import { readFileSync } from 'node:fs';
import { resolveTerminalTheme } from './store';
import {
  applyTerminalTheme,
  DEFAULT_LIGHT_THEME_NAME,
  DEFAULT_THEME_NAME,
  findTheme,
  TERMINAL_BACKGROUND_CSS_PROPERTY,
  type CompleteXtermTheme,
} from './themes';

const VISUAL_ROLES: Array<keyof CompleteXtermTheme> = [
  'background', 'foreground', 'cursor', 'cursorAccent', 'selectionBackground',
  'black', 'red', 'green', 'yellow', 'blue', 'magenta', 'cyan', 'white',
  'brightBlack', 'brightRed', 'brightGreen', 'brightYellow', 'brightBlue',
  'brightMagenta', 'brightCyan', 'brightWhite',
];

describe('terminal theme resolution', () => {
  it('lets an explicit terminal scheme win regardless of app appearance', () => {
    for (const appThemeLight of [false, true]) {
      expect(resolveTerminalTheme({
        terminalThemeName: 'Solarized Dark',
        terminalThemeExplicit: true,
        appThemeLight,
      }).name).toBe('Solarized Dark');
    }
  });

  it('makes the untouched default follow effective appearance', () => {
    expect(resolveTerminalTheme({
      terminalThemeName: DEFAULT_THEME_NAME,
      terminalThemeExplicit: false,
      appThemeLight: false,
    }).name).toBe(DEFAULT_THEME_NAME);
    expect(resolveTerminalTheme({
      terminalThemeName: DEFAULT_THEME_NAME,
      terminalThemeExplicit: false,
      appThemeLight: true,
    }).name).toBe(DEFAULT_LIGHT_THEME_NAME);
  });
});

describe('atomic terminal theme application', () => {
  it('live-flips every visual role and the backing surface in both directions', () => {
    const terminal: { options: { theme?: CompleteXtermTheme } } = { options: {} };
    const host = document.createElement('div');
    const dark = findTheme(DEFAULT_THEME_NAME);
    const light = findTheme(DEFAULT_LIGHT_THEME_NAME);

    for (const scheme of [dark, light, dark]) {
      applyTerminalTheme(terminal, host, scheme);

      expect(terminal.options.theme).not.toBe(scheme.xterm);
      expect(host.style.getPropertyValue(TERMINAL_BACKGROUND_CSS_PROPERTY))
        .toBe(terminal.options.theme?.background);
      for (const role of VISUAL_ROLES) {
        expect(terminal.options.theme?.[role], role).toBe(scheme.xterm[role]);
      }
    }
  });

  it('does not allow pane CSS to pin the xterm viewport to a stale color', () => {
    const styles = readFileSync('src/styles.css', 'utf8');
    expect(styles).toContain(
      'background: var(--terminal-background, var(--terminal-background-default)) !important;',
    );
    expect(styles).not.toMatch(/\.pane-body \.xterm-viewport\s*\{[^}]*background:\s*#000\b/s);
  });
});
