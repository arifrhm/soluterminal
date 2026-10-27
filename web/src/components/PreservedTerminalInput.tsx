import { copyToClipboard } from "../lib/clipboard";
import {
  clearTerminalRecoveredInput,
  useTerminalRecoveredInput,
} from "../lib/terminalInteractionState";

export function PreservedTerminalInput({
  recoveryKey,
  valid,
  overlay = false,
}: {
  recoveryKey: string;
  valid: () => boolean;
  overlay?: boolean;
}) {
  const text = useTerminalRecoveredInput(recoveryKey);
  if (!text || !valid()) return null;
  return (
    <section
      className={overlay ? "pane-switch-notice" : "service-section"}
      aria-label="Preserved terminal input"
    >
      <p>
        Unsent input was preserved because this terminal changed. Copy it before
        leaving this app.
      </p>
      <textarea
        aria-label="Preserved unsent terminal input"
        readOnly
        value={text}
      />
      <button
        type="button"
        onClick={() => {
          if (!valid()) return;
          void copyToClipboard(text).then((copied) => {
            if (copied && valid())
              clearTerminalRecoveredInput(recoveryKey, text);
          });
        }}
      >
        Copy preserved input
      </button>
    </section>
  );
}
