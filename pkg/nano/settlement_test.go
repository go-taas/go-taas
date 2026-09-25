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
	got := Settlement(0, 0, 300, Amount{}, Amount{}, cachedPerM)

	want := new(big.Int).Exp(big.NewInt(10), big.NewInt(20), nil)
	want.Mul(want, big.NewInt(3))
	require.Equal(t, want.String(), got.Raw().String())
	require.False(t, got.IsZero(), "a sub-cent charge must not be written off")
	// 3*10^20 raw is 10 decimal places at 10^30/XNO.
	require.Equal(t, "0.0000000003", got.Format())
}

// TestSettlementCardRailContrast shows the value the cents-rounded card
// path (billing.pricing.computeAmount -> centsFromAmount) throws away on
// the same call: rounding 0.0000000003 XNO to 2 fiat decimals is 0 cents,
// so the card rail would collect nothing and write the charge off.
func TestSettlementCardRailContrast(t *testing.T) {
	got := Settlement(0, 0, 300, Amount{}, Amount{}, mustXNO("0.000001"))

	// Simulate the card rail's 2-decimal rounding on the charge: the
	// charge is 3*10^20 raw, and a cent is 10^28 raw, so it rounds to 0.
	cardCents := new(big.Int).Quo(got.Raw(), mustXNO("0.01").Raw())
	require.Equal(t, int64(0), cardCents.Int64(),
		"the card rail rounds this charge to 0 cents and collects nothing")

	// The Nano leg keeps the exact charge available for settlement.
	require.Equal(t, 0, got.Cmp(mustXNO("0.0000000003")))
}

// TestSettlementFullCallSumsAllTermTypes checks that prompt, completion
// and cached terms each contribute at their own XNO per-1M rates (the
// D2 price formula, kept exact).
func TestSettlementFullCallSumsAllTermTypes(t *testing.T) {
	input, _ := ParseXNO("0.00002")   // 2e25 raw / 1M prompt tokens
	output, _ := ParseXNO("0.00008")  // 8e25 raw / 1M completion tokens
	cached, _ := ParseXNO("0.000004") // 4e24 raw / 1M cached tokens

	// 1,000 prompt, 200 completion, 500 cached tokens.
	got := Settlement(1000, 200, 500, input, output, cached)

	// prompt:   1000 * 2e25  / 1e6 = 2e22 raw
	// completion:200 * 8e25  / 1e6 = 1.6e22 raw
	// cached:    500 * 4e24  / 1e6 = 2e21  raw
	// total                    = 3.8e22 raw = 38000000000000000000000
	require.Equal(t, "38000000000000000000000", got.Raw().String())
}

// TestSettlementZeroNoRatesIdempotent confirms an unpriced model (all
// zero rates) settles to exactly zero raw, and an absent token bucket
// contributes nothing.
func TestSettlementZeroNoRatesIdempotent(t *testing.T) {
	require.True(t, Settlement(0, 0, 0, Amount{}, Amount{}, Amount{}).IsZero())
	require.True(t, Settlement(100, 0, 0, Amount{}, Amount{}, Amount{}).IsZero())
}

func mustXNO(s string) Amount {
	a, err := ParseXNO(s)
	if err != nil {
		panic(err)
	}
	return a
}
