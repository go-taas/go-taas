package evaluation

import (
	"context"
	"errors"
	"math"
	"time"

	"gorm.io/gorm"
)

// CostAttributor estimates the per-request cost of one metered case
// call in minor units (feature #44, D4). The default implementation
// mirrors the metering module's computeAmount formula (AD4) against
// the effective-dated price at completion time; the metering path
// remains the source of truth for billing (AD5).
type CostAttributor interface {
	// EstimateCaseCost returns the estimated cost in minor units for
	// one case execution. priced=false means no price entry applied
	// (cost 0).
	EstimateCaseCost(ctx context.Context, modelID, cardType string, promptTokens, completionTokens int64, completedAt time.Time) (costMinor int64, priced bool)
}

// PriceReader is the seam the default estimator reads price entries
// through, so tests can supply in-memory prices.
type PriceReader interface {
	// ApplicablePrice returns the price version in effect for (model,
	// card) at the given unix time: the greatest effective_from <= at,
	// or nil when absent.
	ApplicablePrice(ctx context.Context, modelID, cardType string, at int64) (*PriceEntry, error)
}

// PriceEntry is the exported price row handed to the estimator.
type PriceEntry struct {
	InputPricePerMillion  float64
	OutputPricePerMillion float64
	CachedPricePerMillion float64
}

// defaultCostAttributor mirrors the metering module's per-request cost
// formula (AD4): prompt x in + completion x out + cached x cache, per
// 1M, rounded to 2 decimals, then converted to integer minor units.
type defaultCostAttributor struct {
	reader PriceReader
}

// NewDefaultCostAttributor constructs the estimator over a PriceReader.
func NewDefaultCostAttributor(reader PriceReader) CostAttributor {
	return &defaultCostAttributor{reader: reader}
}

// defaultCardType mirrors metering.defaultCardType.
const defaultCardType = "default"

// EstimateCaseCost implements CostAttributor (feature #44, D4).
func (a *defaultCostAttributor) EstimateCaseCost(ctx context.Context, modelID, cardType string, promptTokens, completionTokens int64, completedAt time.Time) (int64, bool) {
	if a.reader == nil {
		return 0, false
	}
	at := completedAt.Unix()
	entry, err := a.reader.ApplicablePrice(ctx, modelID, cardType, at)
	if err != nil || entry == nil {
		return 0, false
	}
	amount := float64(promptTokens)*entry.InputPricePerMillion/1_000_000 +
		float64(completionTokens)*entry.OutputPricePerMillion/1_000_000
	amount = math.Round(amount*100) / 100
	return int64(math.Round(amount * 100)), true
}

// ensure the default estimator satisfies the seam.
var _ CostAttributor = (*defaultCostAttributor)(nil)

// dbPriceReader reads effective-dated price entries from the
// price_entries table (the same rows the metering module's
// CostAttributor reads).
type dbPriceReader struct {
	db *gorm.DB
}

// priceEntryRow mirrors the price_entries table columns the estimator
// needs.
type priceEntryRow struct {
	ModelID               string  `gorm:"column:model_id"`
	AcceleratorType       string  `gorm:"column:accelerator_type"`
	EffectiveFrom         int64   `gorm:"column:effective_from"`
	InputPricePerMillion  float64 `gorm:"column:input_price_per_million"`
	OutputPricePerMillion float64 `gorm:"column:output_price_per_million"`
	CachedPricePerMillion float64 `gorm:"column:cached_price_per_million"`
}

func (priceEntryRow) TableName() string { return "price_entries" }

// NewDBPriceReader constructs a PriceReader over the database.
func NewDBPriceReader(db *gorm.DB) PriceReader {
	return &dbPriceReader{db: db}
}

// ApplicablePrice implements PriceReader: the greatest
// effective_from <= at for (model, card), or nil when absent.
func (r *dbPriceReader) ApplicablePrice(ctx context.Context, modelID, cardType string, at int64) (*PriceEntry, error) {
	var row priceEntryRow
	err := r.db.WithContext(ctx).
		Where("model_id = ? AND accelerator_type = ? AND effective_from <= ?", modelID, cardType, at).
		Order("effective_from DESC").
		First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &PriceEntry{
		InputPricePerMillion:  row.InputPricePerMillion,
		OutputPricePerMillion: row.OutputPricePerMillion,
		CachedPricePerMillion: row.CachedPricePerMillion,
	}, nil
}
