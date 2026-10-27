import { pruneLayout } from './lib/layout';
import { readFileSync } from 'node:fs';
import { describe, expect, it } from 'vitest';

describe('app layout styles', () => {
  it('keeps top-row toasts from widening the app past the viewport', () => {
    const styles = readFileSync('src/styles.css', 'utf8');
    const appBlock = styles.match(/\.app\s*\{([^}]*)\}/)?.[1];

    // Top-row toasts must not widen the app past the viewport.
    expect(appBlock).toContain('grid-template-columns: minmax(0, 1fr);');
  });

  it('keeps native composer geometry and suggestion metrics stable', () => {
    const styles = readFileSync('src/styles.css', 'utf8');
    const composer = readFileSync('src/components/composer/composer.css', 'utf8');
    const ghostBlock = composer.match(/\.composer-ghost\s*\{([^}]*)\}/)?.[1] ?? '';
    const slotBlock = composer.match(/\.composer-primary-slot\s*\{([^}]*)\}/)?.[1] ?? '';
    const nativeSlotBlock = styles.match(/\.native-pane \.composer-primary-slot\s*\{([^}]*)\}/)?.[1] ?? '';

    expect(ghostBlock).toContain('white-space: nowrap');
    expect(ghostBlock).toContain('text-overflow: ellipsis');
    expect(slotBlock).toContain('width: 40px');
    expect(nativeSlotBlock).toContain('width: 30px');
    expect(styles).toContain('--composer-font-size: calc(15px * var(--native-focus-scale');
  });
});

describe('retired Tasks layout migration', () => {
 it('collapses legacy Tasks tiles while preserving other panes', () => {
  const layout = {type:'split',direction:'horizontal',ratio:0.5,a:{type:'tasks',tasksId:'tasks_old',selectedTaskId:null,composing:false},b:{type:'pane',paneId:'p1'}} as const;
  expect(pruneLayout(layout,()=>true)).toEqual({type:'pane',paneId:'p1'});
  expect(pruneLayout(layout.a,()=>true)).toBeNull();
 });
});
