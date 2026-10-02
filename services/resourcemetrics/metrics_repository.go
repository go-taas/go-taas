package resourcemetrics

import (
	"context"
	"sort"

	"gorm.io/gorm"

	"github.com/go-taas/go-taas/pkg/database"
)

// Repository aggregates service_resource_metrics into the resource
// metrics shapes: the bucketed time series and the per-replica rows,
// plus the data_through watermark. It reads the sample table read-only
// over the shared database (AD3).
//
// The aggregation is computed in Go over the range's sample rows rather
// than in SQL. This keeps the queries portable across the SQLite
// FVT/unit-test database and the production PostgreSQL database, at the
// cost of fetching the range's rows into memory — acceptable at the
// sample-retention scale.
type Repository struct {
	db *database.Manager
}

// NewRepository constructs a resourcemetrics Repository bound to a GORM
// database.
func NewRepository(db *gorm.DB) *Repository {
	return &Repository{db: database.NewManager(db)}
}

// AggregateServiceMetrics aggregates service_resource_metrics for a
// service into the bucketed series and the per-replica rows, within the
// optional replica filter and the range.
func (r *Repository) AggregateServiceMetrics(ctx context.Context, serviceID string, since, until, bucketSize int64, replica string) ([]BucketRow, []ReplicaRow, error) {
	rows, err := r.fetchRows(ctx, serviceID, since, until, replica)
	if err != nil {
		return nil, nil, err
	}
	buckets := aggregateBuckets(rows, bucketSize)
	replicas := aggregateReplicas(rows, bucketSize)
	return buckets, replicas, nil
}

// ReadDataThrough returns the start of the last complete sample bucket
// covered by the table for the service and range (AD6): the most recent
// sampled_at truncated to the bucket boundary, minus one bucket. 0 when
// no samples match.
func (r *Repository) ReadDataThrough(ctx context.Context, serviceID string, since, until, bucketSize int64, replica string) (int64, error) {
	rows, err := r.fetchRows(ctx, serviceID, since, until, replica)
	if err != nil {
		return 0, err
	}
	if len(rows) == 0 {
		return 0, nil
	}
	var max int64
	for _, row := range rows {
		ts := row.SampledAt.Unix()
		if ts > max {
			max = ts
		}
	}
	last := bucketStart(max, bucketSize)
	return last - bucketSize, nil
}

// fetchRows loads the sample rows within the service, range and optional
// replica filter.
func (r *Repository) fetchRows(ctx context.Context, serviceID string, since, until int64, replica string) ([]serviceResourceMetricRow, error) {
	base := r.db.DB(ctx).Model(&serviceResourceMetricRow{}).
		Where("service_id = ? AND sampled_at >= ? AND sampled_at < ?", serviceID, unixTime(since), unixTime(until))
	if replica != "" {
		base = base.Where("replica_index = ?", replica)
	}
	var rows []serviceResourceMetricRow
	if err := base.Order("sampled_at ASC").Scan(&rows).Error; err != nil {
		return nil, err
	}
	return rows, nil
}

// aggregateBuckets groups sample rows into time buckets and computes each
// bucket's mean CPU/memory/GPU across the sampled replicas.
func aggregateBuckets(rows []serviceResourceMetricRow, bucketSize int64) []BucketRow {
	byBucket := make(map[int64]*bucketAccum)
	for _, row := range rows {
		b := bucketStart(row.SampledAt.Unix(), bucketSize)
		acc := byBucket[b]
		if acc == nil {
			acc = &bucketAccum{bucket: b}
			byBucket[b] = acc
		}
		acc.add(row)
	}
	out := make([]BucketRow, 0, len(byBucket))
	for _, acc := range byBucket {
		out = append(out, acc.row(bucketSize))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Bucket < out[j].Bucket })
	return out
}

// aggregateReplicas groups sample rows by replica and computes each
// replica's current (most recent) CPU/memory/GPU and data_through.
func aggregateReplicas(rows []serviceResourceMetricRow, bucketSize int64) []ReplicaRow {
	byReplica := make(map[string]*replicaAccum)
	for _, row := range rows {
		acc := byReplica[row.ReplicaIndex]
		if acc == nil {
			acc = &replicaAccum{replicaIndex: row.ReplicaIndex}
			byReplica[row.ReplicaIndex] = acc
		}
		acc.add(row)
	}
	out := make([]ReplicaRow, 0, len(byReplica))
	for _, acc := range byReplica {
		out = append(out, acc.row(bucketSize))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ReplicaIndex < out[j].ReplicaIndex })
	return out
}

// bucketAccum accumulates one time bucket.
type bucketAccum struct {
	bucket       int64
	cpuSum       float64
	memorySum    int64
	gpuSum       float64
	gpuCount     int64
	sampleCount  int64
}

func (a *bucketAccum) add(row serviceResourceMetricRow) {
	a.cpuSum += row.CPUPercent
	a.memorySum += row.MemoryBytes
	a.sampleCount++
	if row.GPUPercent != nil {
		a.gpuSum += *row.GPUPercent
		a.gpuCount++
	}
}

func (a *bucketAccum) row(bucketSize int64) BucketRow {
	row := BucketRow{
		Bucket:        a.bucket,
		CPUPercent:    a.cpuSum / float64(a.sampleCount),
		MemoryBytes:   a.memorySum / a.sampleCount,
		SampleCount:   a.sampleCount,
		BucketSeconds: bucketSize,
	}
	if a.gpuCount > 0 {
		row.GPUPercent = a.gpuSum / float64(a.gpuCount)
		row.HasGPU = true
	}
	return row
}

// replicaAccum accumulates one replica's aggregate.
type replicaAccum struct {
	replicaIndex      string
	latest            *serviceResourceMetricRow
}

func (a *replicaAccum) add(row serviceResourceMetricRow) {
	if a.latest == nil || row.SampledAt.After(a.latest.SampledAt) {
		cp := row
		a.latest = &cp
	}
}

func (a *replicaAccum) row(bucketSize int64) ReplicaRow {
	row := ReplicaRow{
		ReplicaIndex:       a.replicaIndex,
		CurrentCPUPercent:  a.latest.CPUPercent,
		CurrentMemoryBytes: a.latest.MemoryBytes,
		DataThrough:        bucketStart(a.latest.SampledAt.Unix(), bucketSize) - bucketSize,
	}
	if a.latest.GPUPercent != nil {
		row.CurrentGPUPercent = *a.latest.GPUPercent
		row.HasGPU = true
	}
	return row
}