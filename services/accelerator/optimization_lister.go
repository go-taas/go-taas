package accelerator

import (
	"context"

	"github.com/go-taas/go-taas/services/optimization"
)

// NewCardTypeListerAdapter adapts the projection cache's card-type set
// to the optimization module's CardTypeLister seam (feature #50,
// AD14): the idle-service rule checks that a service's card type is
// actually present in the fleet before estimating a saving.
func NewCardTypeListerAdapter(cache *ProjectionCache) optimization.CardTypeLister {
	return cardTypeListerFunc(func(_ context.Context) ([]optimization.CardTypePair, error) {
		cards := cache.ListCardTypes()
		out := make([]optimization.CardTypePair, 0, len(cards))
		for _, ct := range cards {
			out = append(out, optimization.CardTypePair{Vendor: ct.Vendor, CardType: ct.CardType})
		}
		return out, nil
	})
}

// cardTypeListerFunc adapts a function to the CardTypeLister
// interface.
type cardTypeListerFunc func(ctx context.Context) ([]optimization.CardTypePair, error)

// ListCardTypes implements optimization.CardTypeLister.
func (f cardTypeListerFunc) ListCardTypes(ctx context.Context) ([]optimization.CardTypePair, error) {
	return f(ctx)
}

// ensure the adapter satisfies the seam at compile time.
var _ optimization.CardTypeLister = cardTypeListerFunc(nil)
