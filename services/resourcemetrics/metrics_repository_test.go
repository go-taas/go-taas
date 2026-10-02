package resourcemetrics

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func newTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&serviceResourceMetricRow{}))
	t.Cleanup(func() {
		sqlDB, _ := db.DB()
		_ = sqlDB.Close()
	})
	return db
}

func gpuPtr(v float64) *float64 { return &v }

func seedRows(t *testing.T, db *gorm.DB, serviceID string) {
	t.Helper()
	base := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	rows := []serviceResourceMetricRow{
		{ID: "1", ServiceID: serviceID, ReplicaIndex: "replica-1", SampledAt: base, CPUPercent: 50, MemoryBytes: 1024, GPUPercent: gpuPtr(30)},
		{ID: "2", ServiceID: serviceID, ReplicaIndex: "replica-2", SampledAt: base, CPUPercent: 70, MemoryBytes: 2048, GPUPercent: gpuPtr(40)},
		{ID: "3", ServiceID: serviceID, ReplicaIndex: "replica-1", SampledAt: base.Add(time.Hour), CPUPercent: 60, MemoryBytes: 1536, GPUPercent: gpuPtr(35)},
		{ID: "4", ServiceID: serviceID, ReplicaIndex: "replica-2", SampledAt: base.Add(time.Hour), CPUPercent: 80, MemoryBytes: 2560, GPUPercent: gpuPtr(45)},
	}
	require.NoError(t, db.Create(&rows).Error)
}

func TestAggregateServiceMetrics(t *testing.T) {
	db := newTestDB(t)
	seedRows(t, db, "svc-1")
	repo := NewRepository(db)

	since := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC).Unix()
	until := time.Date(2026, 10, 1, 2, 0, 0, 0, time.UTC).Unix()

	// AC1: bucketed series + per-replica rows.
	buckets, replicas, err := repo.AggregateServiceMetrics(context.Background(), "svc-1", since, until, 3600, "")
	require.NoError(t, err)
	require.Len(t, buckets, 2)
	// Bucket 0: mean CPU (50+70)/2=60, memory (1024+2048)/2=1536, GPU (30+40)/2=35.
	assert.Equal(t, since, buckets[0].Bucket)
	assert.InDelta(t, 60, buckets[0].CPUPercent, 0.001)
	assert.Equal(t, int64(1536), buckets[0].MemoryBytes)
	assert.True(t, buckets[0].HasGPU)
	assert.InDelta(t, 35, buckets[0].GPUPercent, 0.001)

	require.Len(t, replicas, 2)
	assert.Equal(t, "replica-1", replicas[0].ReplicaIndex)
	assert.InDelta(t, 60, replicas[0].CurrentCPUPercent, 0.001)
	assert.Equal(t, int64(1536), replicas[0].CurrentMemoryBytes)
	assert.True(t, replicas[0].HasGPU)
	assert.InDelta(t, 35, replicas[0].CurrentGPUPercent, 0.001)

	// AC1: replica filter narrows the series.
	buckets, replicas, err = repo.AggregateServiceMetrics(context.Background(), "svc-1", since, until, 3600, "replica-1")
	require.NoError(t, err)
	require.Len(t, buckets, 2)
	assert.InDelta(t, 50, buckets[0].CPUPercent, 0.001)
	require.Len(t, replicas, 1)
	assert.Equal(t, "replica-1", replicas[0].ReplicaIndex)
}

func TestReadDataThrough(t *testing.T) {
	db := newTestDB(t)
	seedRows(t, db, "svc-1")
	repo := NewRepository(db)

	since := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC).Unix()
	until := time.Date(2026, 10, 1, 2, 0, 0, 0, time.UTC).Unix()

	// AC3: data_through is the start of the last complete bucket minus
	// one bucket. The latest sample is at base+1h, so the last complete
	// bucket is base (bucketStart(base+1h, 3600) - 3600 = base).
	dt, err := repo.ReadDataThrough(context.Background(), "svc-1", since, until, 3600, "")
	require.NoError(t, err)
	assert.Equal(t, since, dt)

	// Empty range -> 0.
	dt, err = repo.ReadDataThrough(context.Background(), "svc-1", until, until+3600, 3600, "")
	require.NoError(t, err)
	assert.Equal(t, int64(0), dt)
}

func TestBucketSizeForRange(t *testing.T) {
	// AC3: hourly for ranges <= 7 days, daily otherwise.
	assert.Equal(t, int64(3600), bucketSizeForRange(0, 7*24*3600))
	assert.Equal(t, int64(3600), bucketSizeForRange(0, 6*24*3600))
	assert.Equal(t, int64(24*3600), bucketSizeForRange(0, 8*24*3600))
}

func TestBucketStart(t *testing.T) {
	assert.Equal(t, int64(3600), bucketStart(3600+100, 3600))
	assert.Equal(t, int64(0), bucketStart(100, 3600))
}