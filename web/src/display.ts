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

/** toCents parses a minor-unit value that may arrive as a string. */
function toCents(value: number | string | undefined): number {
  const parsed = typeof value === 'string' ? parseInt(value, 10) : value;
  return Number.isFinite(parsed as number) ? (parsed as number) : 0;
}

/**
 * quotaRemaining resolves a quota tier's budget and what is left of it: the
 * tier's own cap when it sets one, otherwise the cap the ladder resolves to.
 * A tier that inherits its budget therefore reports the money it can still
 * spend instead of "unlimited". `limitCents` of 0 means the resolved cap is
 * unlimited, which the caller renders as such.
 */
export function quotaRemaining(
  ownLimitCents: number | string | undefined,
  effectiveLimitCents: number | string | undefined,
  spentCents: number | string | undefined,
): { limitCents: number; remainingCents: number } {
  const own = toCents(ownLimitCents);
  const limitCents = own > 0 ? own : toCents(effectiveLimitCents);
  return {
    limitCents,
    remainingCents: limitCents > 0 ? Math.max(limitCents - toCents(spentCents), 0) : 0,
  };
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

/**
 * formatMicros renders a micro-unit amount (1e-6 currency) with adaptive
 * precision: two decimals once the amount reaches a cent, up to six
 * decimals below it, so a sub-cent charge is not printed as zero.
 */
export function formatMicros(micros: number | string | undefined, currency?: string): string {
  const value = typeof micros === 'string' ? parseInt(micros, 10) : micros;
  const amount = Number.isFinite(value as number) ? (value as number) / 1_000_000 : 0;
  const digits = amount === 0 || Math.abs(amount) >= 0.01 ? 2 : 6;
  return `${currencySymbol(currency)}${amount.toFixed(digits)}`;
}

/**
 * formatMajor renders an amount that is already in major units (a bill total,
 * a batch cost the API reports as a decimal string) in the display currency.
 * It exists so those figures get the same symbol and precision as the
 * minor-unit ones instead of printing a bare number and a currency code.
 */
export function formatMajor(
  amount: number | string | undefined,
  digits = 2,
  currency?: string,
): string {
  const value = typeof amount === 'string' ? parseFloat(amount) : amount;
  const safe = Number.isFinite(value as number) ? (value as number) : 0;
  return `${currencySymbol(currency)}${safe.toFixed(digits)}`;
}

/**
 * formatRate renders a per-million-token rate, which the console's rate cards
 * show. Rates are major-unit amounts (a price of 2 means 2 per million), so
 * they are written with the currency symbol and up to four decimals: a cached
 * rate of 0.04 must not print as 0.00.
 */
export function formatRate(rate: number | string | undefined, currency?: string): string {
  const value = typeof rate === 'string' ? parseFloat(rate) : rate;
  const safe = Number.isFinite(value as number) ? (value as number) : 0;
  const digits = safe === 0 ? 2 : safe >= 1 ? 2 : 4;
  return `${currencySymbol(currency)}${safe.toFixed(digits)}`;
}

/**
 * displayCurrencyLabel is the currency code every amount on the console is
 * printed in. It is used where a unit has to appear in text (a form label)
 * rather than next to a number.
 */
export function displayCurrencyLabel(): string {
  return displayCurrency;
}
