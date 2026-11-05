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
// others (logged, retried next pass, FR4.5). When the org has a
// billing account, each new charge also deducts from it inside the
// same transaction (feature #8, AD3).
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
	// Resolve the org's account once per pass (AD3); nil means the org
	// is ungated (AC12).
	var account *Account
	for i := range groups {
		if account == nil || account.OrganizationID != groups[i].OrganizationID {
			account, err = repo.Accounts().FindByOrg(ctx, groups[i].OrganizationID)
			if err != nil {
				return err
			}
		}
		if err := s.chargeGroup(ctx, repo, &groups[i], account); err != nil {
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

// resolvePriceEntry applies the D6 resolution chain for one
// (model, card, time) cell: (model, card) → (model, default) → the model's
// name with each card. Charging and accrual estimation share it so the
// in-flight figure always prices the same row the ledger will.
func (s *Service) resolvePriceEntry(ctx context.Context, repo *Repository, modelID, cardType string, at int64) (*PriceEntry, error) {
	entry, err := repo.ApplicablePrice(ctx, modelID, cardType, at)
	if err != nil || entry != nil {
		return entry, err
	}
	if cardType != defaultCardType {
		entry, err = repo.ApplicablePrice(ctx, modelID, defaultCardType, at)
		if err != nil || entry != nil {
			return entry, err
		}
	}
	// A price row may key on the model's name (the deployment name an
	// older console sent) while metering records the catalog id. Retrying
	// with the name keeps that price effective instead of charging zero.
	name, nameErr := repo.ModelName(ctx, modelID)
	if nameErr != nil || name == "" || name == modelID {
		return nil, nil
	}
	for _, card := range []string{cardType, defaultCardType} {
		entry, err = repo.ApplicablePrice(ctx, name, card, at)
		if err != nil || entry != nil {
			return entry, err
		}
	}
	return nil, nil
}

// chargeGroup prices and commits one usage group: price lookup with the
// D6 fallback chain, month-to-date volume, tier selection, the D2
// amount formula, and the ChargeGroup transaction (FR4, AC6-AC11).
// When account is non-nil the charge also deducts from it (AD3).
func (s *Service) chargeGroup(ctx context.Context, repo *Repository, group *UsageGroup, account *Account) error {
	now := time.Now().UTC()
	currency := config.GetConfig().Billing.Currency

	// D6 resolution chain: (model, card) → (model, default) → unpriced.
	entry, err := s.resolvePriceEntry(ctx, repo, group.ModelID, group.AcceleratorType, group.PeriodStart)
	if err != nil {
		return err
	}

	record := &ChargeRecord{
		ID:                uuid.NewString(),
		OrganizationID:    group.OrganizationID,
		APIKeyID:          group.APIKeyID,
		OwnerUserID:       optionalBillingID(group.OwnerUserID),
		ProjectID:         optionalBillingID(group.ProjectID),
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

	// Feature #8: an org with an account deducts its share of the cycle
	// spend inside the charge transaction (AD3); a charge whose rounded
	// share is still 0 deducts 0 and keeps the fraction for the next one
	// (FR3.2), orgs without an account skip the deduction (AC12).
	var deduction *DeductionSpec
	if account != nil {
		deduction = &DeductionSpec{
			AccountID: account.ID,
			ChargeID:  record.ID,
		}
	}

	return repo.ChargeGroup(ctx, *group, record, deduction)
}

// centsFromAmount converts a 2-decimal amount into integer minor units
// (AD2).
func centsFromAmount(amount float64) int64 {
	return int64(math.Round(amount * 100))
}

// amountScale is the precision charges are stored at: 1e-6 currency
// units. A 2-decimal rounding would erase any usage below one cent (a few
// thousand tokens at a per-million rate), which is most per-request
// traffic, so the ledger keeps micros and the console formats them.
const amountScale = 1_000_000

// computeAmount applies the D2 formula: per-1M rates times token counts,
// summed and rounded to micro-unit precision.
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
	return math.Round(amount*amountScale) / amountScale
}

// AmountMicros converts a stored amount into integer micro-units. It is
// the exact companion of computeAmount's rounding.
func AmountMicros(amount float64) int64 {
	return int64(math.Round(amount * amountScale))
}

// MicrosPerCent is the scale between integer minor units (cents) and the
// ledger's micro-unit precision: 1 cent = 1e4 micros.
const MicrosPerCent = int64(10_000)

// CentsToMicros converts integer minor units into ledger micro-units.
func CentsToMicros(cents int64) int64 { return cents * MicrosPerCent }

// EstimateAccrualMicros prices a usage line at ingestion time so the funds
// gate can see money that is spent but not yet settled.
//
// It deliberately mirrors computeAmount (same token fields, same micro-unit
// rounding) so the in-flight figure is comparable with the eventual charge.
// Two differences are intentional:
//
//   - Tier selection needs the org's month-to-date volume, which is a
//     charging-time question; here the highest tier rate is used instead, so
//     the estimate is an upper bound rather than a lower one.
//   - A nil entry (no price yet) accrues nothing: an unpriceable request
//     cannot be turned into money, and inventing a number would block a
//     tenant for a configuration gap.
func EstimateAccrualMicros(entry *PriceEntry, promptTokens, completionTokens, cachedTokens int64) int64 {
	if entry == nil {
		return 0
	}
	inputRate := entry.InputPricePerMillion
	outputRate := entry.OutputPricePerMillion
	if tiers, err := decodeTiers(entry.TiersJSON); err == nil {
		for _, tier := range tiers {
			if tier.InputPricePerMillion > inputRate {
				inputRate = tier.InputPricePerMillion
			}
			if tier.OutputPricePerMillion > outputRate {
				outputRate = tier.OutputPricePerMillion
			}
		}
	}
	amount := float64(promptTokens)*inputRate/1_000_000 +
		float64(completionTokens)*outputRate/1_000_000 +
		float64(cachedTokens)*entry.CachedPricePerMillion/1_000_000
	return AmountMicros(amount)
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
