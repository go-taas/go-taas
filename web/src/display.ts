// Installation-wide display preferences.
//
// The platform administrator picks one time zone, one default language and one
// currency for the whole installation, so every console renders the same units.
// The values arrive asynchronously (they are stored server-side), so they live
// in this dependency-free module: `formatTime`, `formatMoney` and the money
// helpers read them without importing React or the API client, which keeps the
// import graph acyclic.
//
// An empty time zone means "use the viewer's own zone", which is also what a
// browser does by default.

let displayTimezone = '';
let displayCurrency = 'CNY';

// CURRENCY_SYMBOLS maps the ISO-4217 codes the platform supports to the symbol
// the console prints. An unknown code falls back to the code itself, so an
// amount is never shown without a unit.
const CURRENCY_SYMBOLS: Record<string, string> = {
  USD: '$',
  CNY: '¥',
  EUR: '€',
  HKD: 'HK$',
  JPY: '¥',
};

/** setDisplayPreferences applies the installation's display preferences. */
export function setDisplayPreferences(timezone: string, currency: string): void {
  displayTimezone = timezone || '';
  if (currency) displayCurrency = currency.toUpperCase();
}

/** getDisplayTimezone returns the configured IANA zone, or '' for the viewer's. */
export function getDisplayTimezone(): string {
  return displayTimezone;
}

/** getDisplayCurrency returns the configured display currency. */
export function getDisplayCurrency(): string {
  return displayCurrency;
}

/** currencySymbol returns the symbol printed before an amount. */
export function currencySymbol(currency?: string): string {
  const code = (currency || displayCurrency).toUpperCase();
  return CURRENCY_SYMBOLS[code] ?? `${code} `;
}

/**
 * formatMoney renders a minor-unit amount (cents) in the display currency.
 * An explicit currency overrides the platform preference, which is what a
 * record that already carries its own currency (an invoice, a price) needs.
 */
export function formatMoney(cents: number | string | undefined, currency?: string): string {
  return formatAmount(cents, 2, currency);
}

/**
 * formatAmount renders a minor-unit amount with an explicit precision, for the
 * figures that need more than two decimals (a per-1k-token price).
 */
export function formatAmount(
  cents: number | string | undefined,
  digits: number,
  currency?: string,
): string {
  const value = typeof cents === 'string' ? parseInt(cents, 10) : cents;
  const amount = Number.isFinite(value as number) ? (value as number) / 100 : 0;
  return `${currencySymbol(currency)}${amount.toFixed(digits)}`;
}
