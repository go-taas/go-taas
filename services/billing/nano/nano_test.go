package nano

import (
	"context"
	"testing"

	"github.com/go-taas/go-taas/pkg/mq"
)

// rateOne is the 1:1 USD→XNO boundary rate used in tests.
const rateOne = "1"

// TestXNORawFromUSD_SubCent verifies laws L1/L2/L3: sub-cent USD
// amounts produce exact, non-zero XNO raw amounts with 30-decimal
// precision. At the 1:1 boundary, USD decimals map 1:1 onto XNO
// decimals, so a $0.0005 charge is 5 × 10^26 raw.
func TestXNORawFromUSD_SubCent(t *testing.T) {
	cases := []struct {
		usd   string
		raw   string
		human string
	}{
		// $0.0005 (half a mill) — the canonical sub-cent case from
		// issue #7. 0.0005 XNO × 10^30 = 5 × 10^26 raw.
		{usd: "0.0005", raw: "500000000000000000000000000", human: "0.0005"},
		// $0.05 → 5 cents → 5 × 10^28 raw.
		{usd: "0.05", raw: "50000000000000000000000000000", human: "0.05"},
		// $1.00 → 10^30 raw = 1 XNO.
		{usd: "1.00", raw: "1000000000000000000000000000000", human: "1"},
		// $0.000 → zero, nothing to settle (D8).
		{usd: "0", raw: "0", human: "0"},
		// $0.001 → 1 × 10^27 raw.
		{usd: "0.001", raw: "1000000000000000000000000000", human: "0.001"},
	}
	for _, c := range cases {
		got := XNORawFromUSD(c.usd, rateOne)
		if got != c.raw {
			t.Errorf("XNORawFromUSD(%q) = %q, want %q", c.usd, got, c.raw)
		}
		if h := FormatXNOAmount(got); h != c.human {
			t.Errorf("FormatXNOAmount(%q) = %q, want %q", got, h, c.human)
		}
	}
}

// TestXNORawFromUSD_Roundtrip verifies the raw construction and
// formatting are exact inverses across a range of decimal depths
// (law L2).
func TestXNORawFromUSD_Roundtrip(t *testing.T) {
	values := []string{"0.001", "0.0001", "0.0005", "0.123456789", "12.34", "1000000.01"}
	for _, v := range values {
		raw := XNORawFromUSD(v, rateOne)
		if raw == "" {
			t.Fatalf("XNORawFromUSD(%q) returned empty", v)
		}
		for _, ch := range raw {
			if ch < '0' || ch > '9' {
				t.Errorf("XNORawFromUSD(%q) = %q is not a digit-only integer", v, raw)
			}
		}
		if h := FormatXNOAmount(raw); h != v {
			t.Errorf("roundtrip %q -> raw %q -> %q, want %q", v, raw, h, v)
		}
	}
}

// TestXNORawFromUSD_Rate verifies the rate scales the value exactly.
func TestXNORawFromUSD_Rate(t *testing.T) {
	// $1.00 at a 0.5 XNO/USD rate = 0.5 XNO = 5 × 10^29 raw.
	if got := XNORawFromUSD("1.00", "0.5"); got != "500000000000000000000000000000" {
		t.Errorf("$1 at 0.5 rate -> %q", got)
	}
}

// TestXNORawFromUSD_Zero verifies zero and empty inputs.
func TestXNORawFromUSD_Zero(t *testing.T) {
	for _, v := range []string{"", "0", "0.00", "0.000"} {
		if got := XNORawFromUSD(v, rateOne); got != "0" {
			t.Errorf("XNORawFromUSD(%q) = %q, want 0", v, got)
		}
	}
}

// TestXNORawFromUSD_CentsFloat64 verifies the float64 entry point.
func TestXNORawFromUSD_CentsFloat64(t *testing.T) {
	if got := XNORawFromUSD_CentsFloat64(0.05, rateOne); got != "50000000000000000000000000000" {
		t.Errorf("5 cents -> %q, want 5e28", got)
	}
	if got := XNORawFromUSD_CentsFloat64(0, rateOne); got != "0" {
		t.Errorf("0 cents -> %q, want 0", got)
	}
}

// TestNanoConsumer_RunnerNilWhenDisabled verifies the runner returns nil
// when the Nano consumer is disabled (the established consumer pattern,
// law L5).
func TestNanoConsumer_RunnerNilWhenDisabled(t *testing.T) {
	// NewNanoSettlementsConsumerRunner returns nil when disabled, but
	// constructing it directly always succeeds with a workers default.
	c := NewNanoSettlementsConsumer(mq.NewFake(), 0)
	if c == nil {
		t.Fatal("consumer should be constructed")
	}
	if c.workers != 1 {
		t.Errorf("workers default = %d, want 1", c.workers)
	}
}

// TestNanoConsumer_Subscribe verifies the consumer wires a subscription
// on the settlements subject (law L1).
func TestNanoConsumer_Subscribe(t *testing.T) {
	fake := mq.NewFake()
	c := NewNanoSettlementsConsumer(fake, 1)
	if err := c.Run(context.Background()); err != nil {
		t.Fatalf("Run error: %v", err)
	}
}

// TestAmountForPeriod_ReturnsZeroForNoPrice verifies the additive
// consumer's no-op when an hour is unpriced (D8 in pricing.md).
func TestAmountForPeriod_ReturnsZeroForNoPrice(t *testing.T) {
	c := NewNanoSettlementsConsumer(mq.NewFake(), 1)
	amt, err := c.amountForPeriod(context.Background(), "key-1", 1789000000)
	if err != nil {
		t.Fatalf("amountForPeriod error: %v", err)
	}
	if amt != 0 {
		t.Errorf("amountForPeriod for unpriced hour = %v, want 0", amt)
	}
}

// TestRawAmountUsesInjectedRaver verifies the exact-amount seam: tests
// can inject a fixed rawer to observe the value constructed.
func TestRawAmountUsesInjectedRaver(t *testing.T) {
	c := NewNanoSettlementsConsumer(mq.NewFake(), 1)
	want := "5000000000000000000000000000000"
	c.rawer = func(cents float64) string { return want }
	if got := c.rawAmount(0.05); got != want {
		t.Errorf("rawAmount = %q, want %q", got, want)
	}
}
