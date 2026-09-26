package accelerator

import (
	"context"
	"sort"

	"github.com/go-taas/go-taas/services/image"
)

// NewCardTypesProvider builds a CardTypesProvider over a projection
// cache. It is the constructor the composing layer (apps/taas-server)
// uses to inject the live card-type set into the image module.
func NewCardTypesProvider(cache *ProjectionCache) image.CardTypesProvider {
	return image.CardTypesProviderFunc(func(_ context.Context) ([]image.CardType, error) {
		return cache.ListCardTypes(), nil
	})
}

// ListCardTypes returns the distinct (vendor, card_type) pairs present
// in the cache, sorted by vendor then card type. It derives the set from
// the per-node resources (a card type with zero allocatable is omitted).
func (c *ProjectionCache) ListCardTypes() []image.CardType {
	c.mu.RLock()
	defer c.mu.RUnlock()

	seen := map[string]image.CardType{}
	for _, n := range c.nodes {
		for _, r := range n.Resources {
			if r.Allocatable <= 0 {
				continue
			}
			key := n.Vendor + "\x00" + r.CardType
			if _, ok := seen[key]; !ok {
				seen[key] = image.CardType{Vendor: n.Vendor, CardType: r.CardType}
			}
		}
	}
	out := make([]image.CardType, 0, len(seen))
	for _, ct := range seen {
		out = append(out, ct)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Vendor != out[j].Vendor {
			return out[i].Vendor < out[j].Vendor
		}
		return out[i].CardType < out[j].CardType
	})
	return out
}
