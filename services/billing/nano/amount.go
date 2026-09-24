// Package nano implements a feeless exact-settlement leg for the
// billing.settlements contract (issue #7). It converts priced USD
// amounts into Nano (XNO) raw values (30 decimal places, 10^30 raw =
// 1 XNO) so each metered sub-cent charge settles at its exact value
// with no floor, no batching and no rounding loss.
//
// Amounts arrive from the billing charging engine as float64 cents-level
// values (pricing D2). The Nano leg converts the amount through the
// platform's configured USD→XNO rate and serializes the result as an
// exact raw decimal string (the value handed to the rail). Precision is
// exact at all 30 decimal places, so a $0.0005 charge is represented
// exactly, never rounded to the cent (the fee-floor the issue removes).
package nano

import (
	"math/big"
	"strconv"
	"strings"
)

// XNODecimals is the number of decimal places of the Nano base unit:
// 1 XNO = 10^30 raw.
const XNODecimals = 30

// XNORawFromUSD returns the exact XNO raw amount for a USD amount
// expressed as a decimal string (e.g. "0.0005" for half a mill), at
// the given USD→XNO rate (XNO units per USD unit, e.g. 1 for a 1:1
// boundary). The result is the raw integer with no rounding loss at any
// precision within the 30-decimal XNO grid.
//
// The rate is the boundary concern: the platform operator sets how many
// XNO a USD unit represents at settlement. The construction here is
// exact regardless of the rate — the decimal is scaled to the 30-place
// raw grid, never rounded.
func XNORawFromUSD(usdDecimal string, rateXNOperUSD string) string {
	usdDecimal = strings.TrimSpace(usdDecimal)
	rateXNOperUSD = strings.TrimSpace(rateXNOperUSD)

	// Parse the USD amount as a decimal big.Rat for exact arithmetic.
	amount, ok := new(big.Rat).SetString(usdDecimal)
	if !ok {
		return "0"
	}
	if amount.Sign() < 0 {
		amount.Neg(amount)
	}

	rate := big.NewRat(1, 1)
	if rateXNOperUSD != "" && rateXNOperUSD != "1" {
		if r, ok2 := new(big.Rat).SetString(rateXNOperUSD); ok2 {
			rate = r
		}
	}

	// XNO amount = USD amount × rate.
	xno := new(big.Rat).Mul(amount, rate)

	// Scale to raw: multiply by 10^30 and truncate to integer.
	scale := new(big.Int).Exp(big.NewInt(10), big.NewInt(XNODecimals), nil)
	rawRat := new(big.Rat).Mul(xno, new(big.Rat).SetInt(scale))
	raw := new(big.Int)
	raw.Quo(rawRat.Num(), rawRat.Denom())

	if raw.Sign() < 0 {
		raw.Neg(raw)
	}
	return raw.String()
}

// XNORawFromUSD_CentsFloat64 converts a float64 USD cents value
// (e.g. 0.05 for five cents) to an exact XNO raw amount string at the
// given rate. This is the entry point from the billing engine, which
// produces float64 amounts at cent precision (pricing D2).
func XNORawFromUSD_CentsFloat64(cents float64, rateXNOperUSD string) string {
	s := strconv.FormatFloat(cents, 'f', -1, 64)
	return XNORawFromUSD(s, rateXNOperUSD)
}

// FormatXNOAmount formats a raw amount string as a human-readable XNO
// value with up to 30 decimal places (e.g. "0.0005").
func FormatXNOAmount(rawAmount string) string {
	if rawAmount == "" || rawAmount == "0" {
		return "0"
	}

	neg := strings.HasPrefix(rawAmount, "-")
	rawAmount = strings.TrimPrefix(rawAmount, "-")

	raw := new(big.Int)
	if _, ok := raw.SetString(rawAmount, 10); !ok {
		return rawAmount
	}

	divisor := new(big.Int).Exp(big.NewInt(10), big.NewInt(XNODecimals), nil)
	quo := new(big.Int).Div(raw, divisor)
	rem := new(big.Int).Mod(raw, divisor)

	sign := ""
	if neg && (quo.Sign() != 0 || rem.Sign() != 0) {
		sign = "-"
	}
	if rem.Sign() == 0 {
		return sign + quo.String()
	}

	remStr := rem.String()
	for len(remStr) < XNODecimals {
		remStr = "0" + remStr
	}
	remStr = strings.TrimRight(remStr, "0")

	return sign + quo.String() + "." + remStr
}
