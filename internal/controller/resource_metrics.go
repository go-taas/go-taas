package controller

import (
	"context"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/go-taas/go-taas/pkg/logger"
)

// newUUID returns a fresh UUID v4 string for a sample row primary key.
func newUUID() string { return uuid.NewString() }

// serviceResourceMetricRow is the sample row the controller writes to
// the service_resource_metrics table (feature #37, AD2). It mirrors the
// resourcemetrics module's read-only projection.
type serviceResourceMetricRow struct {
	ID           string `gorm:"primaryKey"`
	ServiceID    string `gorm:"size:64;not null;index:idx_service_resource_metrics_service_time,priority:1;index:idx_service_resource_metrics_service_replica_time,priority:1"`
	ReplicaIndex string `gorm:"size:32;not null;index:idx_service_resource_metrics_service_replica_time,priority:2"`
	SampledAt    time.Time `gorm:"not null;index:idx_service_resource_metrics_service_time,priority:2"`
	CPUPercent   float64
	MemoryBytes  int64
	GPUPercent   *float64
}

// TableName overrides the default GORM table name.
func (serviceResourceMetricRow) TableName() string { return "service_resource_metrics" }

// ResourceSampler is the per-service CPU/memory/GPU sampling loop
// (feature #37, AD2). It reads per-container CPU/memory from the
// Kubernetes metrics-server and GPU utilization from the accelerator
// signals, masks pod names to replica indices, and writes one row per
// (service, replica, sample) to the service_resource_metrics table.
//
// The sampler is best-effort telemetry, not a reconcile: transient
// Kubernetes API errors are logged and the loop retries on the next
// tick. A service with no running replicas produces no samples (FR1.3).
type ResourceSampler struct {
	db       *gorm.DB
	provider ResourceSampleProvider
	interval time.Duration
}

// ResourceSampleProvider reads the per-service per-replica resource
// samples from the cluster. It is an interface so the sampler can be
// tested against a fake and so the metrics-server / accelerator reads
// stay behind one seam.
type ResourceSampleProvider interface {
	// SampleServices returns one sample per (service, replica) for every
	// running inference service. gpu_percent is nil when the accelerator
	// signals do not expose it (AD7).
	SampleServices(ctx context.Context) ([]ResourceSample, error)
}

// ResourceSample is one (service, replica) resource sample.
type ResourceSample struct {
	ServiceID    string
	ReplicaIndex string
	CPUPercent   float64
	MemoryBytes  int64
	GPUPercent   *float64
}

// NewResourceSampler builds a ResourceSampler over a database and a
// sample provider.
func NewResourceSampler(db *gorm.DB, provider ResourceSampleProvider, interval time.Duration) *ResourceSampler {
	if interval <= 0 {
		interval = 30 * time.Second
	}
	return &ResourceSampler{db: db, provider: provider, interval: interval}
}

// Run samples on the interval until ctx is cancelled.
func (s *ResourceSampler) Run(ctx context.Context) {
	ticker := time.NewTicker(s.interval)
	defer ticker.Stop()

	// Sample once immediately so the table is populated without waiting
	// a full interval.
	s.sampleAndWrite(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.sampleAndWrite(ctx)
		}
	}
}

// sampleAndWrite reads the samples and writes them to the table. Errors
// are logged; the loop retries on the next tick.
func (s *ResourceSampler) sampleAndWrite(ctx context.Context) {
	samples, err := s.provider.SampleServices(ctx)
	if err != nil {
		logger.S().Warnw("controller: resource sampling failed", "err", err)
		return
	}
	if len(samples) == 0 {
		return
	}
	now := time.Now().UTC()
	rows := make([]serviceResourceMetricRow, 0, len(samples))
	for _, smp := range samples {
		rows = append(rows, serviceResourceMetricRow{
			ID:           newUUID(),
			ServiceID:    smp.ServiceID,
			ReplicaIndex: smp.ReplicaIndex,
			SampledAt:    now,
			CPUPercent:   smp.CPUPercent,
			MemoryBytes:  smp.MemoryBytes,
			GPUPercent:   smp.GPUPercent,
		})
	}
	if err := s.db.WithContext(ctx).Create(&rows).Error; err != nil {
		logger.S().Warnw("controller: write resource samples failed", "err", err)
		return
	}
	logger.S().Infow("controller: resource samples written",
		"samples", len(samples))
}