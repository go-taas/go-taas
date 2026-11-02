package billing

import (
	"context"
	"sort"

	"gorm.io/gorm"

	"github.com/go-taas/go-taas/pkg/database"
)

// CostAnalyticsRepository aggregates charge_records into the cost
// analytics shapes (feature #29, AD1). It reads billing's charge_records
// read-only over the shared database.
type CostAnalyticsRepository struct {
	db *gorm.DB
}

// NewCostAnalyticsRepository constructs a CostAnalyticsRepository bound
// to a GORM database.
func NewCostAnalyticsRepository(db *gorm.DB) *CostAnalyticsRepository {
	return &CostAnalyticsRepository{db: db}
}

// DB resolves the gorm handle for the context (the database.Manager
// pattern).
func (r *CostAnalyticsRepository) DB(ctx context.Context) *gorm.DB {
	return database.NewManager(r.db).DB(ctx)
}

// AggregateCost aggregates charge_records for the cost view: the
// per-bucket series rows and the per-dimension-value rows, within the
// optional org filter, the dimension, and the range. dimension is one of
// organization / model / api_key / user / project; value filters to one dimension value
// (empty for the overview).
func (r *CostAnalyticsRepository) AggregateCost(ctx context.Context, orgFilter, dimension, value string, since, until, bucketSize int64) ([]CostBucketRow, []CostBreakdownAggRow, error) {
	rows, err := r.fetchCharges(ctx, orgFilter, dimension, value, since, until)
	if err != nil {
		return nil, nil, err
	}
	buckets := aggregateCostBuckets(rows, bucketSize)
	breakdown := aggregateCostBreakdown(rows, dimension)
	return buckets, breakdown, nil
}

// DataThrough returns the start of the last complete bucket covered by
// charge records for the filters and range (AD8): the most recent
// period_start truncated to the bucket boundary, minus one bucket. 0 when
// no charge records match.
func (r *CostAnalyticsRepository) DataThrough(ctx context.Context, orgFilter string, since, until, bucketSize int64) (int64, error) {
	rows, err := r.fetchCharges(ctx, orgFilter, "", "", since, until)
	if err != nil {
		return 0, err
	}
	if len(rows) == 0 {
		return 0, nil
	}
	var max int64
	for _, row := range rows {
		if row.PeriodStart > max {
			max = row.PeriodStart
		}
	}
	last := bucketStart(max, bucketSize)
	return last - bucketSize, nil
}

// costChargeRow is the read-only projection of charge_records that the
// cost aggregation reads.
type costChargeRow struct {
	OrganizationID string
	APIKeyID       string
	ModelID        string
	OwnerUserID    string
	ProjectID      string
	PeriodStart    int64
	PromptTokens   int64
	CompletionTokens int64
	CachedTokens   int64
	ReasoningTokens int64
	Amount         float64
}

// TableName overrides the default GORM table name.
func (costChargeRow) TableName() string { return "charge_records" }

// fetchCharges loads the charge-record rows within the filters and range.
func (r *CostAnalyticsRepository) fetchCharges(ctx context.Context, orgFilter, dimension, value string, since, until int64) ([]costChargeRow, error) {
	base := r.DB(ctx).Model(&costChargeRow{}).
		Where("period_start >= ? AND period_start < ?", since, until)
	if orgFilter != "" {
		base = base.Where("organization_id = ?", orgFilter)
	}
	switch dimension {
	case "organization":
		if value != "" {
			base = base.Where("organization_id = ?", value)
		}
	case "model":
		if value != "" {
			base = base.Where("model_id = ?", value)
		}
	case "api_key":
		if value != "" {
			base = base.Where("api_key_id = ?", value)
		}
	case "user":
		if value != "" {
			base = base.Where("owner_user_id = ?", value)
		}
	case "project":
		if value != "" {
			base = base.Where("project_id = ?", value)
		}
	}
	var rows []costChargeRow
	if err := base.Order("period_start ASC").Scan(&rows).Error; err != nil {
		return nil, err
	}
	return rows, nil
}

// aggregateCostBuckets groups rows into time buckets and computes each
// bucket's cost and token sums.
func aggregateCostBuckets(rows []costChargeRow, bucketSize int64) []CostBucketRow {
	byBucket := make(map[int64]*costBucketAccum)
	for _, row := range rows {
		b := bucketStart(row.PeriodStart, bucketSize)
		acc := byBucket[b]
		if acc == nil {
			acc = &costBucketAccum{bucket: b}
			byBucket[b] = acc
		}
		acc.add(row)
	}
	out := make([]CostBucketRow, 0, len(byBucket))
	for _, acc := range byBucket {
		out = append(out, acc.row(bucketSize))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Bucket < out[j].Bucket })
	return out
}

// aggregateCostBreakdown groups rows by the dimension value and computes
// each value's cost and token sums, sorted by cost descending.
func aggregateCostBreakdown(rows []costChargeRow, dimension string) []CostBreakdownAggRow {
	byValue := make(map[string]*costBreakdownAccum)
	for _, row := range rows {
		v := dimensionValue(row, dimension)
		if v == "" {
			continue
		}
		acc := byValue[v]
		if acc == nil {
			acc = &costBreakdownAccum{dimensionValue: v}
			byValue[v] = acc
		}
		acc.add(row)
	}
	out := make([]CostBreakdownAggRow, 0, len(byValue))
	for _, acc := range byValue {
		out = append(out, acc.row())
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].TotalCostCents != out[j].TotalCostCents {
			return out[i].TotalCostCents > out[j].TotalCostCents
		}
		return out[i].DimensionValue < out[j].DimensionValue
	})
	return out
}

// dimensionValue returns the dimension value of a charge row.
func dimensionValue(row costChargeRow, dimension string) string {
	switch dimension {
	case "organization":
		return row.OrganizationID
	case "model":
		return row.ModelID
	case "api_key":
		return row.APIKeyID
	case "user":
		return row.OwnerUserID
	case "project":
		return row.ProjectID
	}
	return ""
}

// costBucketAccum accumulates one time bucket.
type costBucketAccum struct {
	bucket         int64
	totalCostCents int64
	totalTokens    int64
}

func (a *costBucketAccum) add(row costChargeRow) {
	a.totalCostCents += int64(row.Amount * 100)
	a.totalTokens += row.PromptTokens + row.CompletionTokens + row.CachedTokens + row.ReasoningTokens
}

func (a *costBucketAccum) row(bucketSize int64) CostBucketRow {
	return CostBucketRow{
		Bucket:         a.bucket,
		TotalCostCents: a.totalCostCents,
		TotalTokens:    a.totalTokens,
		BucketSeconds:  bucketSize,
	}
}

// costBreakdownAccum accumulates one dimension value's aggregate.
type costBreakdownAccum struct {
	dimensionValue string
	totalCostCents int64
	totalTokens    int64
}

func (a *costBreakdownAccum) add(row costChargeRow) {
	a.totalCostCents += int64(row.Amount * 100)
	a.totalTokens += row.PromptTokens + row.CompletionTokens + row.CachedTokens + row.ReasoningTokens
}

func (a *costBreakdownAccum) row() CostBreakdownAggRow {
	return CostBreakdownAggRow{
		DimensionValue: a.dimensionValue,
		TotalCostCents: a.totalCostCents,
		TotalTokens:    a.totalTokens,
	}
}