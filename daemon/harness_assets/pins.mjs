// Single source of truth for the managed runtime pins. install.mjs installs
// exactly these versions, launch.mjs refuses a runtime that has drifted from
// them, and scripts/check-dsh-pin.mjs reports npm drift for each entry.
export const PINS = { deepseek: '0.1.5-rc.1', pi: '0.85.1' };
