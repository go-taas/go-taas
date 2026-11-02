package metering

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/go-taas/go-taas/pkg/config"
)

// TestDashboardCurrencyFollowsTheCharges covers the card's currency: a price
// can be entered in a currency other than the platform default, and the
// amounts are in the price's currency, so labelling them with the default
// would be wrong.
func TestDashboardCurrencyFollowsTheCharges(t *testing.T) {
	rows := []DashboardAggregateRow{
		{GroupKey: "a", Currency: "CNY", CostMicros: 7273},
	}
	assert.Equal(t, "CNY", dashboardCurrency(rows))

	// Unpriced rows carry no cost, so their currency must not win over a
	// priced one below them.
	rows = append([]DashboardAggregateRow{{GroupKey: "z", Currency: "CNY"}}, rows...)
	assert.Equal(t, "CNY", dashboardCurrency(rows))

	// No charges at all: the platform default applies.
	cfg := &config.Configuration{}
	cfg.Billing.Currency = "EUR"
	config.SetConfigForTest(cfg)
	t.Cleanup(func() { config.SetConfigForTest(&config.Configuration{}) })
	assert.Equal(t, "EUR", dashboardCurrency(nil))
}
