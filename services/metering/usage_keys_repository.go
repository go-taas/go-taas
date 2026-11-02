package metering

import (
	"context"
	"sort"
	"time"

	"gorm.io/gorm"

	"github.com/go-taas/go-taas/pkg/database"
)

// UsageKeysBucketRow is one time bucket of the per-key aggregation.
type UsageKeysBucketRow struct {
	Bucket         int64
	RequestCount   int64
	ErrorCount     int64
	TotalTokens    int64
	TotalCostCents int64
	TotalCostMicros int64
	AvgLatencyMs   int64
	P95LatencyMs   int64
	BucketSeconds  int64
}

// UsageKeyAggRow is one API key's aggregate in the per-key table.
type UsageKeyAggRow struct {
	APIKeyID       string
	OrganizationID string
	// OwnerUserID and ProjectID are the key's attribution snapshot from
	// the request logs.
	OwnerUserID string
	ProjectID   string
	RequestCount   int64
	ErrorCount     int64
	TotalTokens    int64
	TotalCostCents int64
	TotalCostMicros int64
	AvgLatencyMs   int64
	P95LatencyMs   int64
	DataThrough    int64
}

// UsageKeysRepository aggregates request_logs and charge_records into
// the per-key analytics shapes (feature #28, AD1). It reads metering's
// request_logs and billing's charge_records read-only over the shared
// database.
type UsageKeysRepository struct {
	db *database.Manager
}

// NewUsageKeysRepository constructs a UsageKeysRepository bound to a
// GORM database.
func NewUsageKeysRepository(db *gorm.DB) *UsageKeysRepository {
	return &UsageKeysRepository{db: database.NewManager(db)}
}

// APIKeyName resolves an api key id to its display name (read-only,
// AD3). Unknown keys return "".
func (r *UsageKeysRepository) APIKeyName(ctx context.Context, apiKeyID string) (string, error) {
	var name string
	err := r.db.DB(ctx).Table("api_keys").Select("name").Where("id = ?", apiKeyID).Scan(&name).Error
	if err != nil {
		return "", err
	}
	return name, nil
}

// AggregateUsageKeys aggregates request_logs for the fleet per-key view:
// the per-bucket series rows and the per-key rows, within the optional
// org and model filters and the range.
func (r *UsageKeysRepository) AggregateUsageKeys(ctx context.Context, filter UsageKeysFilter, since, until, bucketSize int64) ([]UsageKeysBucketRow, []UsageKeyAggRow, error) {
	rows, err := r.fetchLogs(ctx, filter, since, until)
	if err != nil {
		return nil, nil, err
	}
	buckets := aggregateUsageKeyBuckets(rows, bucketSize)
	keys := aggregateUsageKeys(rows, bucketSize)
	return buckets, keys, nil
}

// AggregateUsageKey aggregates request_logs for one key: the per-bucket
// series rows, within the org filter and the range.
func (r *UsageKeysRepository) AggregateUsageKey(ctx context.Context, orgID, apiKeyID string, since, until, bucketSize int64) ([]UsageKeysBucketRow, error) {
	rows, err := r.fetchLogs(ctx, UsageKeysFilter{OrganizationID: orgID}, since, until)
	if err != nil {
		return nil, err
	}
	var filtered []RequestLog
	for _, row := range rows {
		if row.APIKeyID == apiKeyID {
			filtered = append(filtered, row)
		}
	}
	return aggregateUsageKeyBuckets(filtered, bucketSize), nil
}

// ChargeCostByKey returns the per-key cost from charge_records within the
// org filter and range (AD1), in micro-units so a sub-cent charge is not
// rounded to zero.
func (r *UsageKeysRepository) ChargeCostByKey(ctx context.Context, orgFilter string, since, until int64) (map[string]int64, error) {
	query := r.db.DB(ctx).Model(&chargeRecordRow{}).
		Select("api_key_id, CAST(ROUND(SUM(amount) * 1000000) AS INTEGER) AS cost_micros").
		Where("period_start >= ? AND period_start < ?", since, until)
	if orgFilter != "" {
		query = query.Where("organization_id = ?", orgFilter)
	}
	var rows []struct {
		APIKeyID   string
		CostMicros int64
	}
	if err := query.Group("api_key_id").Scan(&rows).Error; err != nil {
		return nil, err
	}
	out := make(map[string]int64, len(rows))
	for _, row := range rows {
		out[row.APIKeyID] = row.CostMicros
	}
	return out, nil
}

// DataThrough returns the start of the last complete bucket covered by
// request logs for the filters and range (AD7): the most recent
// request-log created_at truncated to the bucket boundary, minus one
// bucket. 0 when no request logs match.
func (r *UsageKeysRepository) DataThrough(ctx context.Context, filter UsageKeysFilter, since, until, bucketSize int64) (int64, error) {
	rows, err := r.fetchLogs(ctx, filter, since, until)
	if err != nil {
		return 0, err
	}
	if len(rows) == 0 {
		return 0, nil
	}
	var max int64
	for _, row := range rows {
		ts := row.CreatedAt.Unix()
		if ts > max {
			max = ts
		}
	}
	last := bucketStart(max, bucketSize)
	return last - bucketSize, nil
}

// UsageKeysFilter narrows the per-key analytics (feature #28).
type UsageKeysFilter struct {
	OrganizationID string
	ModelID        string
	OwnerUserID    string
	ProjectID      string
}

// fetchLogs loads the request-log rows within the filters and range.
func (r *UsageKeysRepository) fetchLogs(ctx context.Context, filter UsageKeysFilter, since, until int64) ([]RequestLog, error) {
	base := r.db.DB(ctx).Model(&RequestLog{}).
		Where("created_at >= ? AND created_at < ?", unixTime(since), unixTime(until))
	if filter.OrganizationID != "" {
		base = base.Where("organization_id = ?", filter.OrganizationID)
	}
	if filter.ModelID != "" {
		base = base.Where("model_id = ?", filter.ModelID)
	}
	if filter.OwnerUserID != "" {
		base = base.Where("owner_user_id = ?", filter.OwnerUserID)
	}
	if filter.ProjectID != "" {
		base = base.Where("project_id = ?", filter.ProjectID)
	}
	var rows []RequestLog
	if err := base.Order("created_at ASC").Scan(&rows).Error; err != nil {
		return nil, err
	}
	return rows, nil
}

// aggregateUsageKeyBuckets groups rows into time buckets and computes
// each bucket's counts, latency aggregates and token sums.
func aggregateUsageKeyBuckets(rows []RequestLog, bucketSize int64) []UsageKeysBucketRow {
	byBucket := make(map[int64]*usageKeyBucketAccum)
	for _, row := range rows {
		b := bucketStart(row.CreatedAt.Unix(), bucketSize)
		acc := byBucket[b]
		if acc == nil {
			acc = &usageKeyBucketAccum{bucket: b}
			byBucket[b] = acc
		}
		acc.add(row)
	}
	out := make([]UsageKeysBucketRow, 0, len(byBucket))
	for _, acc := range byBucket {
		out = append(out, acc.row(bucketSize))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Bucket < out[j].Bucket })
	return out
}

// aggregateUsageKeys groups rows by api key and computes each key's
// aggregate, sorted by request count descending.
func aggregateUsageKeys(rows []RequestLog, bucketSize int64) []UsageKeyAggRow {
	byKey := make(map[string]*usageKeyAccum)
	for _, row := range rows {
		acc := byKey[row.APIKeyID]
		if acc == nil {
			acc = &usageKeyAccum{
				apiKeyID:    row.APIKeyID,
				orgID:       row.OrganizationID,
				ownerUserID: derefID(row.OwnerUserID),
				projectID:   derefID(row.ProjectID),
			}
			byKey[row.APIKeyID] = acc
		}
		acc.add(row)
	}
	out := make([]UsageKeyAggRow, 0, len(byKey))
	for _, acc := range byKey {
		out = append(out, acc.row(bucketSize))
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].RequestCount != out[j].RequestCount {
			return out[i].RequestCount > out[j].RequestCount
		}
		return out[i].APIKeyID < out[j].APIKeyID
	})
	return out
}

// usageKeyBucketAccum accumulates one time bucket.
type usageKeyBucketAccum struct {
	bucket       int64
	requestCount int64
	errorCount   int64
	latencySum   int64
	latencies    []int64
	totalTokens  int64
}

func (a *usageKeyBucketAccum) add(row RequestLog) {
	a.requestCount++
	if row.Status == "error" {
		a.errorCount++
	}
	a.latencySum += row.LatencyMs
	a.latencies = append(a.latencies, row.LatencyMs)
	a.totalTokens += row.PromptTokens + row.CompletionTokens + row.CachedTokens + row.ReasoningTokens
}

func (a *usageKeyBucketAccum) row(bucketSize int64) UsageKeysBucketRow {
	return UsageKeysBucketRow{
		Bucket:        a.bucket,
		RequestCount:  a.requestCount,
		ErrorCount:    a.errorCount,
		TotalTokens:   a.totalTokens,
		AvgLatencyMs:  avg(a.latencySum, a.requestCount),
		P95LatencyMs:  percentile(a.latencies, 0.95),
		BucketSeconds: bucketSize,
	}
}

// usageKeyAccum accumulates one api key's aggregate.
type usageKeyAccum struct {
	apiKeyID     string
	orgID        string
	ownerUserID  string
	projectID    string
	requestCount int64
	errorCount   int64
	latencySum   int64
	latencies    []int64
	totalTokens  int64
}

func (a *usageKeyAccum) add(row RequestLog) {
	a.requestCount++
	if row.Status == "error" {
		a.errorCount++
	}
	a.latencySum += row.LatencyMs
	a.latencies = append(a.latencies, row.LatencyMs)
	a.totalTokens += row.PromptTokens + row.CompletionTokens + row.CachedTokens + row.ReasoningTokens
}

func (a *usageKeyAccum) row(_ int64) UsageKeyAggRow {
	return UsageKeyAggRow{
		APIKeyID:       a.apiKeyID,
		OrganizationID: a.orgID,
		OwnerUserID:    a.ownerUserID,
		ProjectID:      a.projectID,
		RequestCount:   a.requestCount,
		ErrorCount:     a.errorCount,
		TotalTokens:    a.totalTokens,
		AvgLatencyMs:   avg(a.latencySum, a.requestCount),
		P95LatencyMs:   percentile(a.latencies, 0.95),
	}
}

// bucketStart truncates a unix timestamp to the start of its bucket.
func bucketStart(ts, bucketSize int64) int64 {
	return (ts / bucketSize) * bucketSize
}

// unixTime converts a unix-seconds value to a UTC time for GORM range
// filters (portable across SQLite and Postgres).
func unixTime(ts int64) time.Time {
	return time.Unix(ts, 0).UTC()
}

// bucketSizeForRange returns the time-bucket size in seconds for a
// range: hourly for ranges <= 7 days, daily otherwise (AD5).
func bucketSizeForRange(since, until int64) int64 {
	if until-since <= 7*24*3600 {
		return 3600
	}
	return 24 * 3600
}

// avg returns the rounded integer average, or 0 when n is 0.
func avg(sum, n int64) int64 {
	if n == 0 {
		return 0
	}
	return (sum + n/2) / n
}

// percentile returns the nearest-rank percentile of the latency values
// (0 when empty).
func percentile(values []int64, p float64) int64 {
	n := len(values)
	if n == 0 {
		return 0
	}
	sorted := make([]int64, n)
	copy(sorted, values)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	rank := int(p*float64(n) + 0.999999)
	if rank < 1 {
		rank = 1
	}
	if rank > n {
		rank = n
	}
	return sorted[rank-1]
}
