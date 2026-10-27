export const DEEPSEEK_FLASH_MODEL = 'deepseek-flash';
export const DEEPSEEK_VISION_MODEL = 'deepseek-v4-flash-vision-exp';

const LEGACY_NON_VISION = new Set([
  'deepseek-v4-pro',
  'deepseek-v4-flash',
  'deepseek-chat',
  'deepseek-reasoner',
]);

// Side-effect-free mirror of shared/providers.CanonicalDeepSeekDispatchModel.
// It exists at the launcher boundary because extracted harness assets run
// independently of the Go daemon and must fail before spawning a child.
export function canonicalDeepseekModel(raw) {
  const trimmed = String(raw ?? '').trim();
  if (!trimmed) return DEEPSEEK_FLASH_MODEL;
  const parts = trimmed.split('/');
  if (parts.some(part => !part.trim())) throw new Error(`Unsupported DeepSeek model: ${trimmed}`);
  const component = parts.at(-1).toLowerCase();
  if (component === DEEPSEEK_FLASH_MODEL) return DEEPSEEK_FLASH_MODEL;
  if (component === DEEPSEEK_VISION_MODEL) return DEEPSEEK_VISION_MODEL;
  if (LEGACY_NON_VISION.has(component)) return DEEPSEEK_FLASH_MODEL;
  throw new Error(`Unsupported DeepSeek model: ${trimmed}`);
}

export function validateDeepseekSelector(raw) {
  if (raw !== DEEPSEEK_FLASH_MODEL && raw !== DEEPSEEK_VISION_MODEL) {
    throw new Error(`Unsupported DeepSeek model: ${String(raw ?? '')}`);
  }
  return raw;
}
