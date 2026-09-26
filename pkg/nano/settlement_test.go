package nano

import (
	"math/big"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestSettlementExactSubCent pins the fee-floor edge that issue #7 is
// about: a cheap inference call (a few hundred cached tokens at a
// sub-cent per-1M XNO rate) settles to a non-zero exact raw amount.
// On the card rail the same charge rounds to $0.00 and is written off;
// here it is preserved at 30-decimal raw precision.
func TestSettlementExactSubCent(t *testing.T) {
	// cachedPerM = 0.000001 XNO per 1M tokens = 10^24 raw / 1M tokens.
	cachedPerM, err := ParseXNO("0.000001")
	require.NoError(t, err)

	// 300 cached tokens: 300 * 10^24 / 1_000_000 = 3*10^20 raw.
	got := Settlement(0, 0, 0, 300, Amount{}, Amount{}, cachedPerM)

	want := new(big.Int).Exp(big.NewInt(10), big.NewInt(20), nil)
	want.Mul(want, big.NewInt(3))
	require.Equal(t, want.String(), got.Raw().String())
	require.False(t, got.IsZero(), "a sub-cent charge must not be written off")
	// 3*10^20 raw is 10 decimal places at 10^30/XNO.
	require.Equal(t, "0.0000000003", got.Format())
}

// TestSettlementCardRailContrast documents what the Nano leg preserves:
// the exact raw charge of the same call. On a cents-denominated card rail
// the charge is smaller than a cent and is collected as nothing; here the
// exact 30-decimal XNO amount is kept available for settlement. This
// package never converts XNO to a fiat amount (see the package doc), so
// the contrast is stated entirely in XNO raw units, not fiat cents.
func TestSettlementCardRailContrast(t *testing.T) {
	got := Settlement(0, 0, 0, 300, Amount{}, Amount{}, mustXNO("0.000001"))

	// The exact raw charge is preserved, not zeroed.
	require.False(t, got.IsZero(), "the charge must not be written off")
	require.Equal(t, 0, got.Cmp(mustXNO("0.0000000003")))
}

// TestSettlementFullCallSumsAllTermTypes checks that prompt, completion,
// reasoning and cached terms each contribute at their own rates (the D2
// price formula, kept exact), with reasoning tokens priced at the output
// rate as the pricing contract specifies.
func TestSettlementFullCallSumsAllTermTypes(t *testing.T) {
	input, _ := ParseXNO("0.00002")   // 2e25 raw / 1M prompt tokens
	output, _ := ParseXNO("0.00008")  // 8e25 raw / 1M completion tokens
	cached, _ := ParseXNO("0.000004") // 4e24 raw / 1M cached tokens

	// 1,000 prompt, 200 completion, 300 reasoning, 500 cached tokens.
	got := Settlement(1000, 200, 300, 500, input, output, cached)

	// prompt:    1000 * 2e25  / 1e6 = 2e22   raw
	// completion: 200 * 8e25  / 1e6 = 1.6e22 raw
	// reasoning:  300 * 8e25  / 1e6 = 2.4e22 raw  (priced at the output rate)
	// cached:      500 * 4e24  / 1e6 = 2e21   raw
	// total                          = 6.2e22 raw = 62000000000000000000000
	require.Equal(t, "62000000000000000000000", got.Raw().String())
}

// TestSettlementZeroNoRatesIdempotent confirms an unpriced model (all
// zero rates) settles to exactly zero raw, and an absent token bucket
// contributes nothing.
func TestSettlementZeroNoRatesIdempotent(t *testing.T) {
	require.True(t, Settlement(0, 0, 0, 0, Amount{}, Amount{}, Amount{}).IsZero())
	require.True(t, Settlement(100, 0, 0, 0, Amount{}, Amount{}, Amount{}).IsZero())
}

// TestSettlementNegativeRateNeverReduces pins the guard against a negative
// rate: a negative XNO per-1M rate is invalid input and must be treated as
// absent (contributes zero), never subtract from the settlement. Without
// the guard a negative output rate would return a negative payable amount.
func TestSettlementNegativeRateNeverReduces(t *testing.T) {
	positive, _ := ParseXNO("0.00008")

	// Build a negative rate directly at the raw level (8e22 raw negated);
	// parseDecimal only accepts non-negative strings, which is itself the
	// input guard, so this constructs the invalid input the guard must
	// tolerate at the raw layer.
	negRaw := new(big.Int)
	negRaw.SetString("80000000000000000000000", 10)
	negRaw.Neg(negRaw)
	negative := Amount{raw: negRaw}

	// Settlement(1000, 1000, 0, 0, positive, negative, Amount{}):
	// prompt=1000 tokens at +0.00008 XNO/1M (input rate) and
	// completion=1000 tokens at -0.00008 XNO/1M (output rate). The
	// negative output rate is ignored, so only the prompt term survives
	// (1000*8e25/1e6 = 8e22 raw).
	got := Settlement(1000, 1000, 0, 0, positive, negative, Amount{})
	require.Equal(t, "80000000000000000000000", got.Raw().String())
	require.False(t, got.IsZero(), "a negative rate must not turn a real charge into zero or negative")
}

// TestSettlementTruncatesToRawGranularity documents the per-token
// accounting granularity: a rate smaller than one raw per token, applied
// to a single token, truncates to 0 raw. This is intended — raw is the
// smallest Nano unit and a fraction of a raw cannot be charged — and a
// many-token call accumulates the full precision across its tokens.
func TestSettlementTruncatesToRawGranularity(t *testing.T) {
	// RawFromInt(999999) = 999999 raw per 1M tokens. A single token is
	// 0.999999 raw, which truncates to 0: below one raw, no charge.
	single := Settlement(0, 1, 0, 0, Amount{}, RawFromInt(999999), Amount{})
	require.True(t, single.IsZero())

	// Two tokens reach 1.999998 raw and truncate to 1 raw.
	two := Settlement(0, 2, 0, 0, Amount{}, RawFromInt(999999), Amount{})
	require.Equal(t, "1", two.Raw().String())

	// A million tokens move the full rate exactly: 1e6 * 999999 / 1e6.
	million := Settlement(0, 1_000_000, 0, 0, Amount{}, RawFromInt(999999), Amount{})
	require.Equal(t, "999999", million.Raw().String())
}

func mustXNO(s string) Amount {
	a, err := ParseXNO(s)
	if err != nil {
		panic(err)
	}
	return a
}
