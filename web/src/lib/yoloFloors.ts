export const YOLO_FLOOR_IDS = [
  'critical-tier',
  'risk-floor',
  'spec-mismatch',
  'recovery-revert',
  'screens-missing',
  'engine-control-path',
] as const;

export function yoloFloorLabel(id: string): string {
  switch (id) {
    case 'critical-tier': return 'critical-tier tasks';
    case 'risk-floor': return 'risk-floor tasks';
    case 'spec-mismatch': return 'spec-mismatch briefs';
    case 'recovery-revert': return 'recovery/revert tasks';
    case 'screens-missing': return 'missing requested screenshots';
    case 'engine-control-path': return 'engine control-path changes';
    default: return id;
  }
}
