package billing

import (
	"context"
	"encoding/json"
	"math"
	"time"

	"github.com/google/uuid"

	"github.com/go-taas/go-taas/pkg/config"
	"github.com/go-taas/go-taas/pkg/logger"
)

// defaultCardType is the per-model fallback sentinel (D6).
const defaultCardType = "default"

// PriceOnce charges every uncharged (model, card) group of one
// (api_key, hour) bucket — the single charging entry point shared by
// the settlements consumer and the reconciliation runner (D7). Groups
// are processed independently: one group's failure does not abort the
// others (logged, retried next pass, FR4.5).
func (s *Service) PriceOnce(ctx context.Context, apiKeyID string, periodStart int64) error {
	repo, err := s.repository()
	if err != nil {
		return err
	}
	periodEnd := periodStart + 3600
	groups, err := repo.UnchargedGroups(ctx, apiKeyID, periodStart, periodEnd)
	if err != nil {
		return err
	}
	for i := range groups {
		if err := s.chargeGroup(ctx, repo, &groups[i]); err != nil {
			logger.S().Warnw("billing: group charge failed, will retry next pass",
				"api_key_id", groups[i].APIKeyID,
				"model_id", groups[i].ModelID,
				"accelerator_type", groups[i].AcceleratorType,
				"period_start", groups[i].PeriodStart,
				"err", err)
		}
	}
	return nil
}

// chargeGroup prices and commits one usage group: price lookup with the
// D6 fallback chain, month-to-date volume, tier selection, the D2
// amount formula, and the ChargeGroup transaction (FR4, AC6-AC11).
func (s *Service) chargeGroup(ctx context.Context, repo *Repository, group *UsageGroup) error {
	now := time.Now().UTC()
	currency := config.GetConfig().Billing.Currency

	// D6 resolution chain: (model, card) → (model, default) → unpriced.
	entry, err := repo.ApplicablePrice(ctx, group.ModelID, group.AcceleratorType, group.PeriodStart)
	if err != nil {
		return err
	}
	if entry == nil && group.AcceleratorType != defaultCardType {
		entry, err = repo.ApplicablePrice(ctx, group.ModelID, defaultCardType, group.PeriodStart)
		if err != nil {
			return err
		}
	}

	record := &ChargeRecord{
		ID:                uuid.NewString(),
		OrganizationID:    group.OrganizationID,
		APIKeyID:          group.APIKeyID,
		ModelID:           group.ModelID,
		AcceleratorType:   group.AcceleratorType,
		PeriodStart:       group.PeriodStart,
		PeriodEnd:         group.PeriodEnd,
		PromptTokens:      group.PromptTokens,
		CompletionTokens:  group.CompletionTokens,
		CachedTokens:      group.CachedTokens,
		ReasoningTokens:   group.ReasoningTokens,
		RequestCount:      group.RequestCount,
		Amount:            0,
		Currency:          currency,
		TierIndex:         -1,
		Priced:            false,
		MonthToDateTokens: 0,
		ChargedAt:         now,
	}

	if entry != nil {
		tiers, decodeErr := decodeTiers(entry.TiersJSON)
		if decodeErr != nil {
			return decodeErr
		}
		// D3: the tier is selected by the org's month-to-date volume
		// for (model, card) before this charge.
		monthStart := monthStartOf(time.Unix(group.PeriodStart, 0).UTC())
		monthToDate, mtdErr := repo.MonthToDateTokens(ctx,
			group.OrganizationID, group.ModelID, group.AcceleratorType,
			monthStart, group.PeriodStart)
		if mtdErr != nil {
			return mtdErr
		}
		tierIndex, inRate, outRate := selectTier(tiers, monthToDate)
		amount := computeAmount(group, entry, inRate, outRate)

		record.Amount = amount
		record.Currency = entry.Currency
		record.TierIndex = tierIndex
		record.Priced = true
		record.MonthToDateTokens = monthToDate
		priceID := entry.ID
		record.PriceID = &priceID
	}

	return repo.ChargeGroup(ctx, *group, record)
}

// computeAmount applies the D2 formula: per-1M rates times token
// counts, summed and rounded to 2 decimals.
func computeAmount(group *UsageGroup, entry *PriceEntry, tierInputRate, tierOutputRate float64) float64 {
	inputRate := entry.InputPricePerMillion
	outputRate := entry.OutputPricePerMillion
	if tierInputRate > 0 || tierOutputRate > 0 {
		inputRate = tierInputRate
		outputRate = tierOutputRate
	}
	amount := float64(group.PromptTokens)*inputRate/1_000_000 +
		float64(group.CompletionTokens)*outputRate/1_000_000 +
		float64(group.CachedTokens)*entry.CachedPricePerMillion/1_000_000
	return math.Round(amount*100) / 100
}

// selectTier picks the volume tier for the month-to-date volume: the
// first tier whose up_to_tokens exceeds the volume, else the unbounded
// last tier. Flat entries (no tiers) return -1 and the entry rates
// (AC8).
func selectTier(tiers []PriceTier, monthToDate int64) (index int, input, output float64) {
	if len(tiers) == 0 {
		return -1, 0, 0
	}
	for i, tier := range tiers {
		if tier.UpToTokens == 0 || monthToDate < tier.UpToTokens {
			return i, tier.InputPricePerMillion, tier.OutputPricePerMillion
		}
	}
	last := tiers[len(tiers)-1]
	return len(tiers) - 1, last.InputPricePerMillion, last.OutputPricePerMillion
}

// decodeTiers parses the stored tiers JSON; an empty/NULL value means
// flat.
func decodeTiers(raw []byte) ([]PriceTier, error) {
	if len(raw) == 0 || string(raw) == "null" || string(raw) == "[]" {
		return nil, nil
	}
	var tiers []PriceTier
	if err := json.Unmarshal(raw, &tiers); err != nil {
		return nil, err
	}
	return tiers, nil
}

// encodeTiers serializes the tier list; nil means flat ("[]").
func encodeTiers(tiers []PriceTier) ([]byte, error) {
	if len(tiers) == 0 {
		return []byte("[]"), nil
	}
	return json.Marshal(tiers)
}
