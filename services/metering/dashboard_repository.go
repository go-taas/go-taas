package metering

import (
	"context"
	"sort"
	"strconv"
	"time"

	"gorm.io/gorm"

	"github.com/go-taas/go-taas/pkg/database"
)

// DashboardGroupBy is the aggregation dimension of the usage dashboard.
type DashboardGroupBy string

// Supported dashboard group-by dimensions (feature #9, AD2).
const (
	GroupByAPIKey          DashboardGroupBy = "api_key"
	GroupByModel           DashboardGroupBy = "model"
	GroupByAcceleratorType DashboardGroupBy = "accelerator_type"
	GroupByUser            DashboardGroupBy = "user"
	GroupByProject         DashboardGroupBy = "project"
)

// DashboardFilter narrows the dashboard aggregate. Empty ModelID and
// APIKeyID mean "no filter", so a caller that only wants a range keeps the
// original behaviour.
type DashboardFilter struct {
	OrganizationID string
	Since          int64
	Until          int64
	GroupBy        DashboardGroupBy
	// ModelID narrows the aggregate to one catalog model.
	ModelID string
	// APIKeyID narrows the aggregate to one API key.
	APIKeyID string
}

// apply adds the filter's optional predicates to a query builder.
func (f DashboardFilter) apply(q *gorm.DB) *gorm.DB {
	if f.ModelID != "" {
		q = q.Where("model_id = ?", f.ModelID)
	}
	if f.APIKeyID != "" {
		q = q.Where("api_key_id = ?", f.APIKeyID)
	}
	return q
}

// chargeRecordRow is the read-only projection of billing's
// charge_records table that the dashboard aggregates over (AD1). It is
// defined locally so the metering module reads billing's table without
// importing the billing package.
type chargeRecordRow struct {
	OrganizationID   string
	APIKeyID         string
	ModelID          string
	AcceleratorType  string
	PeriodStart      int64
	PromptTokens     int64
	CompletionTokens int64
	CachedTokens     int64
	ReasoningTokens  int64
	RequestCount     int64
	Amount           float64
	Currency         string
	Priced           bool
	// OwnerUserID and ProjectID are the key's attribution snapshot. They are
	// nullable in the billing table (a shared key has no owner), so they are
	// pointers rather than strings.
	OwnerUserID *string
	ProjectID   *string
}

// TableName overrides the default GORM table name to billing's table.
func (chargeRecordRow) TableName() string { return "charge_records" }

// DashboardAggregateRow is one (day, group) aggregate of the dashboard
// query: cost cents plus the token/request sums and the priced flag.
type DashboardAggregateRow struct {
	Day              int64
	GroupKey         string
	CostCents        int64
	CostMicros       int64
	PromptTokens     int64
	CompletionTokens int64
	CachedTokens     int64
	ReasoningTokens  int64
	RequestCount     int64
	// PricedRaw is the MIN(CASE WHEN priced THEN 1 ELSE 0 END) result
	// (0/1), portable across sqlite and Postgres; converted to Priced.
	PricedRaw int
	Priced    bool
	// Currency is the currency the charges were priced in. It is read from
	// the charge records rather than assumed, because a price may be entered
	// in a currency other than the platform default and the amounts below are
	// in the price's currency.
	Currency string
}

// DashboardRepository aggregates charge records into the usage
// dashboard's daily buckets and resolves the charge watermark. It reads
// billing's charge_records read-only over the shared database (AD1).
type DashboardRepository struct {
	db *database.Manager
}

// NewDashboardRepository constructs a DashboardRepository bound to a
// database Manager.
func NewDashboardRepository(db *gorm.DB) *DashboardRepository {
	return &DashboardRepository{db: database.NewManager(db)}
}

// groupColumn maps a group-by dimension to the charge_records column.
func groupColumn(g DashboardGroupBy) string {
	switch g {
	case GroupByModel:
		return "model_id"
	case GroupByAcceleratorType:
		return "accelerator_type"
	case GroupByUser:
		// owner_user_id is a uuid column: it cannot be scanned into a string
		// (Postgres refuses the conversion) and a shared key leaves it NULL,
		// so it is cast and coalesced. Both are valid in sqlite and Postgres.
		return "COALESCE(CAST(owner_user_id AS TEXT), '')"
	case GroupByProject:
		return "project_id"
	default:
		return "api_key_id"
	}
}

// usageLineRow is the read-only projection of metering's own usage_lines
// table: the raw, per-request usage every call records, whether or not it has
// been priced yet.
type usageLineRow struct {
	OrganizationID   string
	APIKeyID         string
	ModelID          string
	AcceleratorType  string
	PromptTokens     int64
	CompletionTokens int64
	CachedTokens     int64
	ReasoningTokens  int64
	CompletedAt      time.Time
	OwnerUserID      *string
	ProjectID        *string
}

// TableName overrides the default GORM table name.
func (usageLineRow) TableName() string { return "usage_lines" }

// DashboardAggregate aggregates the organization's usage for one range,
// grouped by UTC day x group dimension (AD2).
//
// Usage is taken from usage_lines, which every metered call writes; the cost
// comes from charge_records, which only exist once the settlement job priced
// the hour. Reading only the charge records (the original behaviour) left the
// dashboard empty on an installation with no billing account or no price —
// the usage was there, the view said zero. A (day, group) that has charges
// reports their cost and is priced; one with only raw usage reports zero cost
// and is not priced, which the card surfaces as unpriced requests.
func (r *DashboardRepository) DashboardAggregate(ctx context.Context, orgID string, since, until int64, groupBy DashboardGroupBy) ([]DashboardAggregateRow, error) {
	return r.DashboardAggregateFiltered(ctx, DashboardFilter{
		OrganizationID: orgID, Since: since, Until: until, GroupBy: groupBy,
	})
}

// DashboardAggregateFiltered is DashboardAggregate with the optional model and
// API-key filters applied: a usage page that narrows to one model must narrow
// the tokens and charges together, or the two halves would disagree.
func (r *DashboardRepository) DashboardAggregateFiltered(ctx context.Context, filter DashboardFilter) ([]DashboardAggregateRow, error) {
	orgID, since, until, groupBy := filter.OrganizationID, filter.Since, filter.Until, filter.GroupBy
	col := groupColumn(groupBy)
	merged := make(map[string]*DashboardAggregateRow)

	// Raw usage: always present, so tokens and requests are truthful. The
	// day bucket and the time comparison are dialect-specific (sqlite in
	// tests, Postgres in production), so the expression follows the dialector
	// instead of relying on a failing query.
	dayExpr := "(EXTRACT(EPOCH FROM completed_at)::bigint / 86400) * 86400"
	lowerBound, upperBound := "to_timestamp(?)", "to_timestamp(?)"
	if r.db.DB(ctx).Name() == "sqlite" {
		dayExpr = "(CAST(strftime('%s', completed_at) AS INTEGER) / 86400) * 86400"
		lowerBound, upperBound = "datetime(?, 'unixepoch')", "datetime(?, 'unixepoch')"
	}
	var usage []struct {
		Day              int64
		GroupKey         string
		PromptTokens     int64
		CompletionTokens int64
		CachedTokens     int64
		ReasoningTokens  int64
		RequestCount     int64
	}
	err := filter.apply(r.db.DB(ctx).Model(&usageLineRow{}).
		Where("organization_id = ? AND completed_at >= "+lowerBound+" AND completed_at < "+upperBound,
			orgID, since, until)).
		Select(dayExpr + " AS day, " +
			col + " AS group_key, " +
			"COALESCE(SUM(prompt_tokens),0) AS prompt_tokens, " +
			"COALESCE(SUM(completion_tokens),0) AS completion_tokens, " +
			"COALESCE(SUM(cached_tokens),0) AS cached_tokens, " +
			"COALESCE(SUM(reasoning_tokens),0) AS reasoning_tokens, " +
			"COUNT(*) AS request_count").
		Group("day, " + col).
		Scan(&usage).Error
	if err != nil {
		return nil, err
	}
	for _, row := range usage {
		key := aggregateKey(row.Day, row.GroupKey)
		merged[key] = &DashboardAggregateRow{
			Day:              row.Day,
			GroupKey:         row.GroupKey,
			PromptTokens:     row.PromptTokens,
			CompletionTokens: row.CompletionTokens,
			CachedTokens:     row.CachedTokens,
			ReasoningTokens:  row.ReasoningTokens,
			RequestCount:     row.RequestCount,
		}
	}

	// Charges: the priced cost, when settlement has run.
	var charges []DashboardAggregateRow
	err = filter.apply(r.db.DB(ctx).Model(&chargeRecordRow{}).
		Where("organization_id = ? AND period_start >= ? AND period_start < ?",
			orgID, since, until)).
		Select("(period_start / 86400) * 86400 AS day, " +
			col + " AS group_key, " +
			"ROUND(SUM(amount) * 100) AS cost_cents, " +
			"CAST(ROUND(SUM(amount) * 1000000) AS INTEGER) AS cost_micros, " +
			"COALESCE(MAX(currency), '') AS currency, " +
			"COALESCE(SUM(prompt_tokens),0) AS prompt_tokens, " +
			"COALESCE(SUM(completion_tokens),0) AS completion_tokens, " +
			"COALESCE(SUM(cached_tokens),0) AS cached_tokens, " +
			"COALESCE(SUM(reasoning_tokens),0) AS reasoning_tokens, " +
			"COALESCE(SUM(request_count),0) AS request_count, " +
			"MIN(CASE WHEN priced THEN 1 ELSE 0 END) AS priced_raw").
		Group("day, " + col).
		Scan(&charges).Error
	if err != nil {
		return nil, err
	}
	for _, row := range charges {
		key := aggregateKey(row.Day, row.GroupKey)
		entry, ok := merged[key]
		if !ok {
			// A charge with no raw usage line (an older row, or a manual
			// adjustment): keep it, its numbers are the only record.
			entry = &DashboardAggregateRow{Day: row.Day, GroupKey: row.GroupKey}
			merged[key] = entry
		}
		entry.CostCents = row.CostCents
		entry.CostMicros = row.CostMicros
		entry.Currency = row.Currency
		entry.Priced = row.PricedRaw != 0
		// The raw lines are the source of truth for the token and request
		// counts; the charge record only fills a bucket that has no raw line
		// left (retention pruning, or a manual adjustment).
		if entry.RequestCount == 0 && entry.PromptTokens == 0 && entry.CompletionTokens == 0 {
			entry.PromptTokens = row.PromptTokens
			entry.CompletionTokens = row.CompletionTokens
			entry.CachedTokens = row.CachedTokens
			entry.ReasoningTokens = row.ReasoningTokens
			entry.RequestCount = row.RequestCount
		}
	}

	out := make([]DashboardAggregateRow, 0, len(merged))
	for _, row := range merged {
		out = append(out, *row)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Day != out[j].Day {
			return out[i].Day < out[j].Day
		}
		return out[i].GroupKey < out[j].GroupKey
	})
	return out, nil
}

// aggregateKey is the (day, group) map key.
func aggregateKey(day int64, group string) string {
	return strconv.FormatInt(day, 10) + "|" + group
}

// ChargeWatermark returns how far the organization's numbers are complete.
//
// It is the max period_start of its charge records, falling back to the last
// usage line when nothing has been priced yet: the dashboard reads raw usage
// too, so reporting "data through 0" on an unpriced installation would
// contradict the numbers right above it.
func (r *DashboardRepository) ChargeWatermark(ctx context.Context, orgID string) (int64, error) {
	var agg struct {
		Max int64
	}
	err := r.db.DB(ctx).Model(&chargeRecordRow{}).
		Select("COALESCE(MAX(period_start),0) AS max").
		Where("organization_id = ?", orgID).
		Scan(&agg).Error
	if err != nil {
		return 0, err
	}
	if agg.Max > 0 {
		return agg.Max, nil
	}
	// Unpriced usage: the raw lines are the freshest data the dashboard shows.
	epoch := "EXTRACT(EPOCH FROM completed_at)::bigint"
	if r.db.DB(ctx).Name() == "sqlite" {
		epoch = "CAST(strftime('%s', completed_at) AS INTEGER)"
	}
	var raw struct{ Max int64 }
	if err := r.db.DB(ctx).Model(&usageLineRow{}).
		Select("COALESCE(MAX("+epoch+"),0) AS max").
		Where("organization_id = ?", orgID).
		Scan(&raw).Error; err != nil {
		return 0, err
	}
	return raw.Max, nil
}

// LifetimeCostMicros returns the scope's all-time charged cost in micro-units.
//
// A usage page shows the current range next to "total spent", and the range is
// capped at 92 days, so the total cannot be summed from the buckets the page
// already has. The model and API-key filters still apply: a filtered page
// reports what that slice has cost overall.
func (r *DashboardRepository) LifetimeCostMicros(ctx context.Context, filter DashboardFilter) (int64, error) {
	var agg struct {
		Micros int64
	}
	err := filter.apply(r.db.DB(ctx).Model(&chargeRecordRow{}).
		Where("organization_id = ?", filter.OrganizationID)).
		Select("CAST(ROUND(COALESCE(SUM(amount), 0) * 1000000) AS INTEGER) AS micros").
		Scan(&agg).Error
	if err != nil {
		return 0, err
	}
	return agg.Micros, nil
}
