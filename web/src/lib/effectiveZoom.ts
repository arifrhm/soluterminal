// Elements under `zoom: var(--ui-scale)` report pointer coordinates in visual
// pixels while layout dimensions and CSS pixel values remain in layout pixels.
// offsetWidth/offsetHeight are layout sizes; getBoundingClientRect is visual,
// so their ratio is the element's live effective zoom for the chosen axis.
export function effectiveZoom(el: HTMLElement, axis: 'x' | 'y'): number {
  const layout = axis === 'x' ? el.offsetWidth : el.offsetHeight;
  if (!layout) return 1;
  const rect = el.getBoundingClientRect();
  const factor = (axis === 'x' ? rect.width : rect.height) / layout;
  return factor > 0.01 ? factor : 1;
}
