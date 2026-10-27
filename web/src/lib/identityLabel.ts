export function maskIdentity(identity: string): string {
  const value = identity.trim();
  const at = value.indexOf('@');
  if (at < 1) return value ? `${value[0]}…` : '';
  return `${value[0]}…${value.slice(at)}`;
}
