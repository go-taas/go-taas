package billing

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestCurrentModelRatesReturnsTheCurrentVersion covers the display rate card
// the user catalog shows: one rate per model, the version that is in effect
// now, never a superseded or a scheduled one.
func TestCurrentModelRatesReturnsTheCurrentVersion(t *testing.T) {
	svc := newBillingTestService(t)
	repo := NewRepository(svc.repo.DB(context.Background()))
	ctx := context.Background()
	now := time.Now().Unix()

	// model-a: an old default rate, a newer default rate, and a scheduled
	// one in the future. The newer current one must win.
	_, err := repo.UpsertPrice(ctx, &PriceEntry{
		ModelID: "model-a", AcceleratorType: "default",
		InputPricePerMillion: 1, OutputPricePerMillion: 2, CachedPricePerMillion: 0.1,
		Currency: "CNY", EffectiveFrom: now - 7200, TiersJSON: []byte("[]"),
	})
	require.NoError(t, err)
	_, err = repo.UpsertPrice(ctx, &PriceEntry{
		ModelID: "model-a", AcceleratorType: "default",
		InputPricePerMillion: 3, OutputPricePerMillion: 6, CachedPricePerMillion: 0.3,
		Currency: "CNY", EffectiveFrom: now - 60, TiersJSON: []byte("[]"),
	})
	require.NoError(t, err)
	_, err = repo.UpsertPrice(ctx, &PriceEntry{
		ModelID: "model-a", AcceleratorType: "default",
		InputPricePerMillion: 99, OutputPricePerMillion: 99, CachedPricePerMillion: 99,
		Currency: "CNY", EffectiveFrom: now + 86400, TiersJSON: []byte("[]"),
	})
	require.NoError(t, err)

	// model-b is priced only per card type: the card row is its rate.
	_, err = repo.UpsertPrice(ctx, &PriceEntry{
		ModelID: "model-b", AcceleratorType: "A800",
		InputPricePerMillion: 2, OutputPricePerMillion: 8, CachedPricePerMillion: 0.4,
		Currency: "CNY", EffectiveFrom: now - 60, TiersJSON: []byte("[]"),
	})
	require.NoError(t, err)

	rates, err := svc.CurrentModelRates(ctx, []string{"model-a", "model-b", "model-unpriced"})
	require.NoError(t, err)
	require.Len(t, rates, 2)

	a := rates["model-a"]
	assert.Equal(t, 3.0, a.InputPerMillion, "the current version wins over the older and the scheduled one")
	assert.Equal(t, 6.0, a.OutputPerMillion)
	assert.Equal(t, 0.3, a.CachedPerMillion)
	assert.Equal(t, "CNY", a.Currency)
	assert.Equal(t, "default", a.AcceleratorType)

	// A model priced only for a card type still shows a rate: the caller
	// does not pick a card, the platform routes to whatever is ready.
	b := rates["model-b"]
	assert.Equal(t, 2.0, b.InputPerMillion)
	assert.Equal(t, "A800", b.AcceleratorType)

	_, priced := rates["model-unpriced"]
	assert.False(t, priced, "an unpriced model is absent, not zero-priced")
}

// TestCurrentModelRatesPrefersTheDefaultCard covers the multi-card case: when a
// model has both a "default" row and card-specific rows, the display rate is
// the model-wide fallback, because that is the rate the platform charges a
// caller who cannot choose the card.
func TestCurrentModelRatesPrefersTheDefaultCard(t *testing.T) {
	svc := newBillingTestService(t)
	repo := NewRepository(svc.repo.DB(context.Background()))
	ctx := context.Background()
	now := time.Now().Unix()

	for _, entry := range []*PriceEntry{
		{ModelID: "model-c", AcceleratorType: "A800", InputPricePerMillion: 9, OutputPricePerMillion: 9,
			Currency: "CNY", EffectiveFrom: now - 60, TiersJSON: []byte("[]")},
		{ModelID: "model-c", AcceleratorType: defaultCardType, InputPricePerMillion: 1, OutputPricePerMillion: 2,
			Currency: "CNY", EffectiveFrom: now - 60, TiersJSON: []byte("[]")},
	} {
		_, err := repo.UpsertPrice(ctx, entry)
		require.NoError(t, err)
	}

	rates, err := svc.CurrentModelRates(ctx, []string{"model-c"})
	require.NoError(t, err)
	assert.Equal(t, 1.0, rates["model-c"].InputPerMillion)
	assert.Equal(t, defaultCardType, rates["model-c"].AcceleratorType)
}

// TestCurrentModelRatesEmptyInput covers the trivial case: no models, no query.
func TestCurrentModelRatesEmptyInput(t *testing.T) {
	svc := newBillingTestService(t)
	rates, err := svc.CurrentModelRates(context.Background(), nil)
	require.NoError(t, err)
	assert.Empty(t, rates)
}

// TestCurrentModelRatesResolvesTheNameAlias covers a price row keyed on the
// model's name (the identifier an older console sent): the catalog lists ids,
// so the lookup has to resolve the alias or the rate card would show nothing
// for a model that is charged correctly.
func TestCurrentModelRatesResolvesTheNameAlias(t *testing.T) {
	svc := newBillingTestService(t)
	ctx := context.Background()
	now := time.Now().Unix()

	// The catalog row the alias resolves to. The models table belongs to the
	// model module; here it only has to answer the id -> name lookup.
	require.NoError(t, svc.repo.DB(ctx).Exec(
		"CREATE TABLE models (id TEXT PRIMARY KEY, name TEXT)").Error)
	require.NoError(t, svc.repo.DB(ctx).Exec(
		"INSERT INTO models (id, name) VALUES (?, ?)", "11111111-1111-4111-8111-111111111111", "qwen").Error)
	_, err := svc.repo.UpsertPrice(ctx, &PriceEntry{
		ModelID: "qwen", AcceleratorType: defaultCardType,
		InputPricePerMillion: 2, OutputPricePerMillion: 8,
		Currency: "CNY", EffectiveFrom: now - 60, TiersJSON: []byte("[]"),
	})
	require.NoError(t, err)

	rates, err := svc.CurrentModelRates(ctx, []string{"11111111-1111-4111-8111-111111111111"})
	require.NoError(t, err)
	rate, ok := rates["11111111-1111-4111-8111-111111111111"]
	require.True(t, ok, "the name-keyed price is reported under the catalog id")
	assert.Equal(t, 2.0, rate.InputPerMillion)
}
