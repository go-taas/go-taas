// Email address validation shared by the sign-in, registration and profile
// forms. It mirrors the server's `validEmailAddress` closely enough to catch
// a typo before a request is made: one addr-spec, a dotted domain, no
// whitespace or display name. The server remains authoritative.
export function isValidEmail(value: string): boolean {
  const email = value.trim();
  if (!email || email.length > 256) return false;
  if (/\s/.test(email)) return false;
  const at = email.indexOf('@');
  if (at <= 0 || at !== email.lastIndexOf('@')) return false;
  const domain = email.slice(at + 1);
  if (domain.length > 253 || !domain.includes('.') || domain.startsWith('.') || domain.endsWith('.')) {
    return false;
  }
  if (domain.includes('..')) return false;
  return domain.split('.').every((label) => label.length > 0 && label.length <= 63);
}
