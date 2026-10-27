import { VoiceAutoSendToggle, VoiceLanguageSelect } from './VoiceButton';
import { VoiceHistoryInline } from './VoiceHistoryButton';
import { TopbarPopover } from './TopbarPopover';
import { IconChevronDown } from './ui/icons';

// Voice settings popover — the options half of the mic split-button. A caret
// attached to the mic (shared shell, hairline divider) opens auto-send toggle,
// language select, and history. Daily use is hold-to-talk; these three are
// "configure once" or "review occasionally" controls.

export function VoiceConfigMenu() {
  return (
    <TopbarPopover
      minWidth={300}
      align="right"
      trigger={({ toggle, open }) => (
        <button
          type="button"
          className="chrome-btn chrome-split-caret"
          onClick={toggle}
          title="Voice settings (auto-send, language, history)"
          aria-label="Voice settings"
          aria-expanded={open}
        >
          <IconChevronDown size={13} />
        </button>
      )}
    >
      <div className="topbar-menu-row">
        <span className="topbar-menu-label">Auto-send</span>
        <VoiceAutoSendToggle />
      </div>
      <div className="topbar-menu-row">
        <span className="topbar-menu-label">Language</span>
        <VoiceLanguageSelect />
      </div>
      <div className="topbar-menu-sep" />
      <VoiceHistoryInline />
    </TopbarPopover>
  );
}
