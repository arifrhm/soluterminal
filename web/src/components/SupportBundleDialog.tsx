import { useCallback, useEffect, useRef, useState } from 'react';
import { api } from '../api';
import {
  SupportBundleRemoteError,
  bundleSelectionBytes,
  defaultSelection,
  formatBundleBytes,
  renderSupportBundle,
  type SupportBundleResponse,
} from '../lib/supportBundle';
import { useStore } from '../store';
import { useFocusTrap } from './useFocusTrap';

type LoadState = 'loading' | 'loaded' | 'error' | 'remote';

export function SupportBundleDialog({ open, onClose }: { open: boolean; onClose: () => void }) {
  const focusedPaneId = useStore((state) => state.focusedPaneId);
  const [loadState, setLoadState] = useState<LoadState>('loading');
  const [response, setResponse] = useState<SupportBundleResponse | null>(null);
  const [selected, setSelected] = useState<Set<string>>(new Set());
  const [expanded, setExpanded] = useState<Set<string>>(new Set());
  const [error, setError] = useState('');
  const requestVersion = useRef(0);
  const paneAtOpen = useRef<string | null>(null);
  const dialogRef = useRef<HTMLDivElement>(null);
  const initialFocusRef = useRef<HTMLButtonElement>(null);

  useFocusTrap(dialogRef, open);

  useEffect(() => {
    if (open) initialFocusRef.current?.focus();
  }, [open]);

  const collect = useCallback(async (paneId: string | null) => {
    const version = ++requestVersion.current;
    setLoadState('loading');
    setResponse(null);
    setError('');
    try {
      const result = await api.supportBundle(paneId ?? undefined);
      if (requestVersion.current !== version) return;
      setResponse(result);
      setSelected(defaultSelection(result));
      setExpanded(new Set());
      setLoadState('loaded');
    } catch (cause) {
      if (requestVersion.current !== version) return;
      if (cause instanceof SupportBundleRemoteError) {
        setLoadState('remote');
        return;
      }
      setError(cause instanceof Error ? cause.message : String(cause));
      setLoadState('error');
    }
  }, []);

  useEffect(() => {
    if (!open) return;
    paneAtOpen.current = focusedPaneId;
    void collect(paneAtOpen.current);
    return () => {
      requestVersion.current++;
    };
    // Collection is intentionally once per open. Changing focus while the
    // preview is open must not silently replace the bytes being reviewed.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [open, collect]);

  useEffect(() => {
    if (!open) return;
    const onKeyDown = (event: KeyboardEvent) => {
      if (event.key === 'Escape') onClose();
    };
    window.addEventListener('keydown', onKeyDown);
    return () => window.removeEventListener('keydown', onKeyDown);
  }, [open, onClose]);

  if (!open) return null;

  const allEmpty = response?.sections.every((section) => section.empty) ?? false;
  const canSave = loadState === 'loaded' && !!response && !allEmpty && selected.size > 0;

  const toggleSelected = (id: string) => {
    setSelected((current) => {
      const next = new Set(current);
      if (next.has(id)) next.delete(id);
      else next.add(id);
      return next;
    });
  };

  const toggleExpanded = (id: string) => {
    setExpanded((current) => {
      const next = new Set(current);
      if (next.has(id)) next.delete(id);
      else next.add(id);
      return next;
    });
  };

  const save = () => {
    if (!response || !canSave) return;
    const text = renderSupportBundle(response, selected);
    const blob = new Blob([text], { type: 'text/plain;charset=utf-8' });
    const url = URL.createObjectURL(blob);
    const anchor = document.createElement('a');
    anchor.href = url;
    anchor.download = response.filename;
    document.body.appendChild(anchor);
    anchor.click();
    anchor.remove();
    setTimeout(() => URL.revokeObjectURL(url), 1000);
    useStore.getState().toast(`Saved ${response.filename}`, { kind: 'success', icon: '📄', ttlMs: 2200 });
    onClose();
  };

  return (
    <div className="dialog-backdrop" onMouseDown={onClose}>
      <div
        ref={dialogRef}
        className="dialog support-bundle-dialog"
        role="dialog"
        aria-modal="true"
        aria-labelledby="support-bundle-title"
        onMouseDown={(event) => event.stopPropagation()}
      >
        <div className="dialog-header">
          <div>
            <h2 id="support-bundle-title" className="dialog-title">Create support bundle</h2>
            <div className="dialog-help">Preview and choose locally redacted diagnostics before saving.</div>
          </div>
          <button ref={initialFocusRef} type="button" aria-label="Close" title="Close (Esc)" onClick={onClose}>×</button>
        </div>

        {loadState === 'loading' && (
          <div className="support-bundle-state" role="status">Collecting local diagnostics…</div>
        )}

        {loadState === 'error' && (
          <div className="support-bundle-state">
            <div className="dialog-error">{error || 'Could not collect diagnostics.'}</div>
            <button type="button" onClick={() => void collect(paneAtOpen.current)}>Retry</button>
          </div>
        )}

        {loadState === 'remote' && (
          <div className="inbox-note-warn">
            Support bundles are local-only. Diagnostics never travel over the cloud relay — open soluterminal on the machine itself to create one.
          </div>
        )}

        {loadState === 'loaded' && response && (
          <>
            {allEmpty && (
              <div className="support-bundle-state inbox-note-warn">
                No diagnostic data is available yet. Keep soluterminal open briefly, then try again.
              </div>
            )}

            {!allEmpty && (
              <div className="support-bundle-sections" aria-label="Bundle contents">
                {response.sections.map((section) => (
                  <section className="support-bundle-section" key={section.id}>
                    <div className="support-bundle-section-row">
                      <label className="support-bundle-checkbox-row">
                        <input
                          type="checkbox"
                          checked={selected.has(section.id)}
                          onChange={() => toggleSelected(section.id)}
                        />
                        <span>
                          <strong>{section.title}</strong>
                          <span className="support-bundle-description">{section.description}</span>
                        </span>
                      </label>
                      <div className="support-bundle-section-tools">
                        <span>{formatBundleBytes(section.bytes)} · {section.lines} {section.lines === 1 ? 'line' : 'lines'}</span>
                        {!section.empty && (
                          <button
                            type="button"
                            className="support-bundle-disclosure"
                            aria-expanded={expanded.has(section.id)}
                            onClick={() => toggleExpanded(section.id)}
                          >
                            {expanded.has(section.id) ? 'Hide preview' : 'Preview'}
                          </button>
                        )}
                      </div>
                    </div>
                    {(section.truncated || section.note) && (
                      <div className="support-bundle-hint">
                        {section.truncated ? 'Truncated to the newest bounded content.' : ''}
                        {section.truncated && section.note ? ' ' : ''}
                        {section.note}
                      </div>
                    )}
                    {expanded.has(section.id) && !section.empty && <pre>{section.text}</pre>}
                  </section>
                ))}
              </div>
            )}

            <div className="support-bundle-omitted">
              <strong>Not included</strong>
              <ul>{response.omitted.map((item) => <li key={item}>{item}</li>)}</ul>
              <div>Screenshots are never collected — this bundle contains no images.</div>
            </div>

            <div className="dialog-help support-bundle-destination">
              Saves to your computer&apos;s Downloads folder. Nothing is uploaded and nothing is sent — sharing the file is up to you.
            </div>

            <div className="dialog-actions">
              <span className="support-bundle-total">
                {formatBundleBytes(bundleSelectionBytes(response, selected))} selected
              </span>
              <button type="button" onClick={onClose}>Cancel</button>
              <button type="button" className="primary" disabled={!canSave} onClick={save}>Save bundle</button>
            </div>
          </>
        )}

        {(loadState === 'error' || loadState === 'remote') && (
          <div className="dialog-actions">
            <button type="button" onClick={onClose}>Cancel</button>
          </div>
        )}
      </div>
    </div>
  );
}
