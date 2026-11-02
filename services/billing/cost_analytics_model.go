package billing

// CostBucketRow is one time bucket of the cost aggregation.
type CostBucketRow struct {
	Bucket          int64
	TotalCostCents  int64
	TotalCostMicros int64
	TotalTokens     int64
	BucketSeconds   int64
}

// CostBreakdownAggRow is one dimension value's aggregate in the
// breakdown.
type CostBreakdownAggRow struct {
	DimensionValue  string
	TotalCostCents  int64
	TotalCostMicros int64
	TotalTokens     int64
}

// bucketSizeForRange returns the time-bucket size in seconds for a
// range: hourly for ranges <= 7 days, daily otherwise (AD6).
func bucketSizeForRange(since, until int64) int64 {
	if until-since <= 7*24*3600 {
		return 3600
	}
	return 24 * 3600
}