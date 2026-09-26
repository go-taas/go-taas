// Package nano provides exact Nano (XNO) amount primitives for
// representing and settling sub-cent usage charges.
//
// Nano's native unit is the raw: 1 XNO = 10^30 raw. Because the raw
// unit is 30 decimal places, a per-call charge that a card rail cannot
// collect — a cached read, a few hundred token completion — is
// represented exactly instead of rounding to the cent or to a minimum
// invoice. This package supplies lossless parse/format at the raw unit
// with no floating point. The value of a charge is expressed in XNO
// itself (e.g. 0.0005 XNO); converting that to a fiat amount is the
// caller's pricing concern and is never done here.
package nano

import (
	"errors"
	"math/big"
	"strings"
)

// RawPerXNO is the number of raw units in one XNO (30 decimals).
// Expressed as a string because it exceeds the int64 range.
const RawPerXNO = "1000000000000000000000000000000"

// Decimals is the number of decimal places in Nano's raw unit.
const Decimals = 30

// rawPerXNO is the parsed 10^30 constant used across the package.
var rawPerXNO = mustParse(RawPerXNO)

func mustParse(s string) *big.Int {
	i, ok := new(big.Int).SetString(s, 10)
	if !ok {
		panic("nano: invalid raw-per-xno constant")
	}
	return i
}

// Amount is an exact XNO value expressed in raw (10^30 base) units.
// It is immutable: every method returns a new Amount.
type Amount struct {
	// raw holds the exact value. It is the integer number of raw units;
	// 0 means "no raw units" (a zero or negative-balance-free value).
	raw *big.Int
}

// zero is the shared zero amount.
var zero = Amount{raw: big.NewInt(0)}

// Raw returns a copy of the primitive value as a *big.Int in raw units.
// The caller may freely mutate the returned value; the Amount is unchanged.
func (a Amount) Raw() *big.Int {
	if a.raw == nil {
		return new(big.Int)
	}
	return new(big.Int).Set(a.raw)
}

// IsZero reports whether the amount is exactly zero raw. The zero-value
// Amount{} (nil raw) is treated as zero.
func (a Amount) IsZero() bool {
	if a.raw == nil {
		return true
	}
	return a.raw.Sign() == 0
}

// NewRaw constructs an Amount from an integer number of raw units.
func NewRaw(raw *big.Int) Amount {
	if raw == nil {
		return zero
	}
	c := new(big.Int).Set(raw)
	if c.Sign() == 0 {
		return zero
	}
	return Amount{raw: c}
}

// RawFromInt constructs an Amount from a non-negative uint64 raw count.
func RawFromInt(raw uint64) Amount { return NewRaw(new(big.Int).SetUint64(raw)) }

// errParse is returned for malformed decimal strings.
var errParse = errors.New("nano: invalid XNO amount string")

// parseDecimal splits an integer/raw decimal string into the raw
// value, rejecting negatives and values with more than Decimals
// fractional digits. It never uses float64.
func parseDecimal(s string) (*big.Int, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, errParse
	}
	switch s[0] {
	case '+':
		s = s[1:]
	case '-':
		return nil, errParse // amount strings here are non-negative
	}
	if s == "" {
		return nil, errParse // a lone sign is not a number
	}
	dot := strings.IndexByte(s, '.')
	intPart, fracPart := s, ""
	if dot >= 0 {
		intPart = s[:dot]
		fracPart = s[dot+1:]
	}
	if strings.IndexByte(fracPart, '.') >= 0 {
		return nil, errParse
	}
	if intPart == "" {
		intPart = "0"
	}

	// A bare "." (or ".", "+." after sign strip) with digits nowhere is
	// malformed, not silently zero: "0." is fine but "." is not.
	if strings.Trim(s, ".") == "" {
		return nil, errParse
	}

	for _, c := range intPart {
		if c < '0' || c > '9' {
			return nil, errParse
		}
	}
	for _, c := range fracPart {
		if c < '0' || c > '9' {
			return nil, errParse
		}
	}
	if len(fracPart) > Decimals {
		return nil, errParse
	}
	// raw = intPart * 10^Decimals + fracPart padded to Decimals digits.
	raw := new(big.Int).SetUint64(0)
	intVal, ok := new(big.Int).SetString(intPart, 10)
	if !ok {
		return nil, errParse
	}
	raw.Mul(intVal, rawPerXNO)
	if fracPart != "" {
		fracVal, ok := new(big.Int).SetString(fracPart, 10)
		if !ok {
			return nil, errParse
		}
		// pad on the right to Decimals digits and add.
		pad := Decimals - len(fracPart)
		fracVal.Mul(fracVal, new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(pad)), nil))
		raw.Add(raw, fracVal)
	}
	return raw, nil
}

// ParseXNO parses a human decimal string (e.g. "0.0005") into an
// Amount with exact 30-decimal resolution. It is lossless: no floating
// point is involved.
func ParseXNO(s string) (Amount, error) {
	raw, err := parseDecimal(s)
	if err != nil {
		return Amount{}, err
	}
	return NewRaw(raw), nil
}

// Format renders the amount as a trimmed human decimal string, dropping
// trailing zeros and the decimal point when the fraction is zero. The
// zero-value Amount{} renders as "0". A negative amount renders with a
// leading '-'.
func (a Amount) Format() string {
	raw := a.raw
	if raw == nil {
		raw = big.NewInt(0)
	}
	neg := raw.Sign() < 0
	abs := new(big.Int).Abs(raw)
	q, r := new(big.Int).QuoRem(abs, rawPerXNO, new(big.Int))
	intPart := q.String()
	if r.Sign() == 0 {
		if neg {
			return "-" + intPart
		}
		return intPart
	}
	frac := r.String()
	for len(frac) < Decimals {
		frac = "0" + frac
	}
	frac = strings.TrimRight(frac, "0")
	if neg {
		return "-" + intPart + "." + frac
	}
	return intPart + "." + frac
}

// Add returns the exact sum of a and b (raw units).
func (a Amount) Add(b Amount) Amount {
	return NewRaw(new(big.Int).Add(rawOf(a), rawOf(b)))
}

// Sub returns the exact difference a - b (raw units); a negative raw
// result is represented as negative.
func (a Amount) Sub(b Amount) Amount {
	return Amount{raw: new(big.Int).Sub(rawOf(a), rawOf(b))}
}

// LessThan reports whether a is strictly less than b.
func (a Amount) LessThan(b Amount) bool { return rawOf(a).Cmp(rawOf(b)) < 0 }

// Cmp compares a and b: -1, 0, or 1. The zero-value Amount{} sorts equal
// to zero.
func (a Amount) Cmp(b Amount) int { return rawOf(a).Cmp(rawOf(b)) }

// rawOf returns a's raw value as a safe *big.Int (never nil).
func rawOf(a Amount) *big.Int {
	if a.raw == nil {
		return new(big.Int)
	}
	return a.raw
}

// tokensPerM is the denominator of a per-1M-token rate.
const tokensPerM = 1_000_000

// share computes one token-costing term: tokens * ratePerM / tokensPerM,
// in raw units. The rate is XNO per 1M tokens, so dividing by 1e6 is the
// decimal shift that turns a per-1M-token rate into a per-token charge;
// the division truncates any sub-raw fraction toward zero, which is the
// intended granularity of per-token accounting (a single token at a rate
// smaller than one raw per token contributes 0 raw and is written off by
// nothing — it is simply below the smallest Nano unit). A zero or
// negative rate (a model the caller cannot price, or an invalid input)
// contributes zero: rates are non-negative XNO per 1M tokens, and a
// negative one must not reduce the settlement.
func share(tokens uint64, ratePerM Amount) *big.Int {
	if tokens == 0 || ratePerM.raw == nil || ratePerM.raw.Sign() <= 0 {
		return big.NewInt(0)
	}
	num := new(big.Int).Mul(new(big.Int).SetUint64(tokens), new(big.Int).Set(rawOf(ratePerM)))
	return new(big.Int).Quo(num, big.NewInt(tokensPerM))
}

// Settlement derives the exact XNO amount payable for one inference call
// from per-1M-token XNO rates (the shape of pricing.md D2: tokens * rate
// per 1M). It prices prompt tokens and cached tokens at their own rates
// and prices reasoning tokens at the output rate, matching the D2 contract
// (docs/design/pricing.md prices reasoning_tokens at the output rate).
// Unlike the card rail — whose 2-decimal cents rounding (see
// billing.pricing.computeAmount) writes off any charge below $0.005 — this
// keeps the full 30-decimal raw precision, so a sub-cent cached read or
// short completion settles exactly instead of being batched or discarded
// (issue #7). Passing it a nil-or-zero rate respects a model it cannot
// price (matched by a caller's price resolution before settlement); a
// negative rate is treated as absent and never reduces the settlement.
func Settlement(promptTokens, completionTokens, reasoningTokens, cachedTokens uint64,
	inputPerM, outputPerM, cachedPerM Amount) Amount {
	total := new(big.Int)
	total.Add(total, share(promptTokens, inputPerM))
	total.Add(total, share(completionTokens, outputPerM))
	total.Add(total, share(reasoningTokens, outputPerM))
	total.Add(total, share(cachedTokens, cachedPerM))
	return NewRaw(total)
}
