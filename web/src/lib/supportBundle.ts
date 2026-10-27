export interface SupportBundleSection {
  id: string;
  title: string;
  description: string;
  text: string;
  bytes: number;
  lines: number;
  selectedByDefault: boolean;
  empty: boolean;
  truncated: boolean;
  note?: string;
}

export interface SupportBundleResponse {
  schema: number;
  generatedAt: string;
  filename: string;
  sections: SupportBundleSection[];
  omitted: string[];
  logPaths: string[];
}

export class SupportBundleRemoteError extends Error {
  constructor() {
    super('Support bundles are local-only. Open soluterminal on the machine itself to create one.');
    this.name = 'SupportBundleRemoteError';
  }
}

export function defaultSelection(res: SupportBundleResponse): Set<string> {
  return new Set(res.sections.filter((section) => section.selectedByDefault).map((section) => section.id));
}

export function bundleSelectionBytes(res: SupportBundleResponse, selectedIds: ReadonlySet<string>): number {
  return res.sections.reduce(
    (total, section) => total + (selectedIds.has(section.id) ? section.bytes : 0),
    0,
  );
}

export function formatBundleBytes(n: number): string {
  const bytes = Math.max(0, Math.round(n));
  if (bytes < 1024) return `${bytes} B`;
  if (bytes < 1024 * 1024) return `${Math.ceil(bytes / 1024)} KB`;
  const mb = bytes / (1024 * 1024);
  return `${mb >= 10 ? Math.ceil(mb) : mb.toFixed(1).replace(/\.0$/, '')} MB`;
}

export function renderSupportBundle(res: SupportBundleResponse, selectedIds: ReadonlySet<string>): string {
  const included = res.sections.filter((section) => selectedIds.has(section.id));
  const excluded = res.sections.filter((section) => !selectedIds.has(section.id));
  const header = [
    '# soluterminal support bundle',
    `# Schema: ${res.schema}`,
    `# Generated: ${res.generatedAt}`,
    '# Tokens, API keys and home paths were stripped on this machine before this file was written.',
    '#',
    '# Not included:',
    ...res.omitted.map((item) => `# - ${item}`),
    '#',
    `# Included section ids: ${included.map((section) => section.id).join(', ') || '(none)'}`,
    `# Excluded section ids: ${excluded.map((section) => section.id).join(', ') || '(none)'}`,
    '#',
    '',
  ].join('\n');

  let rendered = header;
  for (const section of included) {
    rendered += `===== ${section.title} =====\n`;
    rendered += section.text;
    rendered += '\n\n';
  }
  return rendered;
}
