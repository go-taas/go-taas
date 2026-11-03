package billing

import (
	"context"

	"github.com/go-taas/go-taas/services/model"
)

// CurrentModelRates implements the model module's PriceLookup seam: it
// answers the display rate card of a page of models so the user catalog can
// show what a token costs.
//
// One row per model is returned, and the choice is deliberate:
//
//   - a "default" row wins when the model has one, because that is the rate
//     the platform charges a caller who cannot pick a card (DeepSeek-style
//     single-price display);
//   - otherwise the card-specific row with the lowest input rate is shown, so
//     a model priced only per card type still displays a rate instead of
//     nothing. Which card actually served the request is a routing detail the
//     caller neither sees nor controls.
//
// Only versions in effect now are considered: a scheduled version is not a
// price yet, and a superseded one is no longer the rate.
func (s *Service) CurrentModelRates(ctx context.Context, modelIDs []string) (map[string]model.Rate, error) {
	rates := make(map[string]model.Rate, len(modelIDs))
	if len(modelIDs) == 0 {
		return rates, nil
	}
	repo, err := s.repository()
	if err != nil {
		return nil, err
	}
	rows, err := repo.CurrentPricesByModel(ctx, modelIDs)
	if err != nil {
		return nil, err
	}
	// A price row may key on the model's name (the identifier an older
	// console sent) while the catalog lists the id. Retrying those models by
	// name keeps that price visible on the rate card, exactly as charging
	// keeps it effective.
	priced := make(map[string]bool, len(rows))
	for _, row := range rows {
		priced[row.ModelID] = true
	}
	var aliases []string
	for _, id := range modelIDs {
		if priced[id] {
			continue
		}
		if name, nameErr := repo.ModelName(ctx, id); nameErr == nil && name != "" && name != id {
			aliases = append(aliases, name)
		}
	}
	if len(aliases) > 0 {
		aliasRows, aliasErr := repo.CurrentPricesByModel(ctx, aliases)
		if aliasErr != nil {
			return nil, aliasErr
		}
		// Re-key the alias rows onto the catalog id so the caller only ever
		// sees ids.
		byName := make(map[string]string, len(aliases))
		for _, id := range modelIDs {
			if priced[id] {
				continue
			}
			if name, nameErr := repo.ModelName(ctx, id); nameErr == nil && name != "" {
				byName[name] = id
			}
		}
		for _, row := range aliasRows {
			if id, ok := byName[row.ModelID]; ok {
				copied := *row
				copied.ModelID = id
				rows = append(rows, &copied)
			}
		}
	}
	for _, row := range rows {
		card, ok := rates[row.ModelID]
		if ok && card.AcceleratorType == defaultCardType {
			continue
		}
		if ok && row.AcceleratorType != defaultCardType && row.InputPricePerMillion >= card.InputPerMillion {
			continue
		}
		rates[row.ModelID] = model.Rate{
			InputPerMillion:  row.InputPricePerMillion,
			OutputPerMillion: row.OutputPricePerMillion,
			CachedPerMillion: row.CachedPricePerMillion,
			Currency:         row.Currency,
			AcceleratorType:  row.AcceleratorType,
		}
	}
	return rates, nil
}
