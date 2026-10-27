import { useState } from 'react';
import { PortPanel } from './PortPanel';

// The browser tile's start page, shown while the tile has no URL. Lists the
// dev servers started from the user's panes (others on request) so the
// usual next step is one click, plus an address field for anything else.
export function BrowserStartPage({ onOpen }: { onOpen: (url: string) => void }) {
  const [draft, setDraft] = useState('');
  return (
    <div className="browser-start">
      <form
        className="browser-start-form"
        onSubmit={(e) => {
          e.preventDefault();
          if (draft.trim()) onOpen(draft);
        }}
      >
        <label className="browser-start-label" htmlFor="browser-start-url">
          Open a page
        </label>
        <input
          id="browser-start-url"
          className="browser-start-input"
          value={draft}
          onChange={(e) => setDraft(e.target.value)}
          placeholder="localhost:3000, a URL, or a search"
          spellCheck={false}
          autoComplete="off"
          autoFocus
        />
      </form>
      <PortPanel />
    </div>
  );
}
