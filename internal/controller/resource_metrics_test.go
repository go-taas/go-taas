package controller

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/client-go/kubernetes/fake"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// fakeSampleProvider returns a fixed set of samples.
type fakeSampleProvider struct {
	samples []ResourceSample
	err     error
}

func (f *fakeSampleProvider) SampleServices(context.Context) ([]ResourceSample, error) {
	return f.samples, f.err
}

func newSamplerDB(t *testing.T) *gorm.DB {
	t.Helper()
	// File-backed SQLite so the sampler goroutine and the test can
	// access the DB concurrently without the in-memory shared-cache
	// lock.
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "sampler.db")), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&serviceResourceMetricRow{}))
	t.Cleanup(func() {
		sqlDB, _ := db.DB()
		_ = sqlDB.Close()
	})
	return db
}

func TestResourceSamplerWritesSamples(t *testing.T) {
	db := newSamplerDB(t)
	gpu := 42.0
	provider := &fakeSampleProvider{samples: []ResourceSample{
		{ServiceID: "svc-1", ReplicaIndex: "replica-1", CPUPercent: 50, MemoryBytes: 1024, GPUPercent: &gpu},
		{ServiceID: "svc-1", ReplicaIndex: "replica-2", CPUPercent: 70, MemoryBytes: 2048},
	}}
	sampler := NewResourceSampler(db, provider, 10*time.Millisecond)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		sampler.Run(ctx)
		close(done)
	}()

	// The sampler writes immediately on start.
	require.Eventually(t, func() bool {
		var count int64
		_ = db.Model(&serviceResourceMetricRow{}).Count(&count).Error
		return count >= 2
	}, 2*time.Second, 10*time.Millisecond)

	var rows []serviceResourceMetricRow
	require.NoError(t, db.Order("sampled_at ASC").Find(&rows).Error)
	require.GreaterOrEqual(t, len(rows), 2)
	assert.Equal(t, "svc-1", rows[0].ServiceID)
	assert.Equal(t, "replica-1", rows[0].ReplicaIndex)
	assert.InDelta(t, 50, rows[0].CPUPercent, 0.001)
	assert.Equal(t, int64(1024), rows[0].MemoryBytes)
	require.NotNil(t, rows[0].GPUPercent)
	assert.InDelta(t, 42, *rows[0].GPUPercent, 0.001)
	// The second sample has no GPU.
	assert.Nil(t, rows[1].GPUPercent)

	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return after cancel")
	}
}

func TestResourceSamplerNoSamples(t *testing.T) {
	db := newSamplerDB(t)
	provider := &fakeSampleProvider{samples: nil}
	sampler := NewResourceSampler(db, provider, 10*time.Millisecond)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		sampler.Run(ctx)
		close(done)
	}()

	// No samples -> no rows written.
	time.Sleep(50 * time.Millisecond)
	var count int64
	require.NoError(t, db.Model(&serviceResourceMetricRow{}).Count(&count).Error)
	assert.Equal(t, int64(0), count)

	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return after cancel")
	}
}

func TestResourceSamplerProviderError(t *testing.T) {
	db := newSamplerDB(t)
	provider := &fakeSampleProvider{err: assert.AnError}
	sampler := NewResourceSampler(db, provider, 10*time.Millisecond)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		sampler.Run(ctx)
		close(done)
	}()

	// Provider error -> no rows written, loop keeps running.
	time.Sleep(50 * time.Millisecond)
	var count int64
	require.NoError(t, db.Model(&serviceResourceMetricRow{}).Count(&count).Error)
	assert.Equal(t, int64(0), count)

	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return after cancel")
	}
}

func TestParseReplicaIndex(t *testing.T) {
	assert.Equal(t, 1, parseReplicaIndex("replica-1"))
	assert.Equal(t, 2, parseReplicaIndex("replica-2"))
	assert.Equal(t, 0, parseReplicaIndex("replica"))
	assert.Equal(t, 0, parseReplicaIndex(""))
}

func TestNewResourceSamplerDefaultInterval(t *testing.T) {
	db := newSamplerDB(t)
	// A zero interval falls back to the 30s default.
	sampler := NewResourceSampler(db, &fakeSampleProvider{}, 0)
	assert.Equal(t, 30*time.Second, sampler.interval)
}

func TestNewK8sResourceSampleProviderDefaultNamespace(t *testing.T) {
	clientset := fake.NewSimpleClientset()
	metricsClient := newMetricsClient(nil)
	// An empty namespace falls back to "taas-infer".
	provider := NewK8sResourceSampleProvider(clientset, metricsClient, "", nil)
	assert.Equal(t, "taas-infer", provider.namespace)
}