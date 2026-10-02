// Package resourcemetrics implements the read-only per-service resource
// utilization aggregation over the service_resource_metrics table
// (feature #37). It serves the admin Service Metrics page
// (GetServiceResourceMetrics), deriving per-service CPU/memory/GPU
// utilization over time from the sample rows the Controller writes.
package resourcemetrics

import "time"

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

// bucketStart truncates a unix timestamp to the start of its bucket.
func bucketStart(ts, bucketSize int64) int64 {
	return (ts / bucketSize) * bucketSize
}

// serviceResourceMetricRow is the read-only projection of the
// service_resource_metrics table the resourcemetrics module aggregates
// over (AD3). It is defined locally so the module reads the table
// without importing the controller package.
type serviceResourceMetricRow struct {
	ID            string `gorm:"primaryKey"`
	ServiceID     string `gorm:"size:64;not null;index:idx_service_resource_metrics_service_time,priority:1;index:idx_service_resource_metrics_service_replica_time,priority:1"`
	ReplicaIndex  string `gorm:"size:32;not null;index:idx_service_resource_metrics_service_replica_time,priority:2"`
	SampledAt     time.Time `gorm:"not null;index:idx_service_resource_metrics_service_time,priority:2"`
	CPUPercent    float64
	MemoryBytes   int64
	GPUPercent    *float64
}

// TableName overrides the default GORM table name.
func (serviceResourceMetricRow) TableName() string { return "service_resource_metrics" }

// ServiceMetricRow is the exported sample row used by FVT to seed the
// service_resource_metrics table. It mirrors the internal row.
type ServiceMetricRow struct {
	ID           string `gorm:"primaryKey"`
	ServiceID    string `gorm:"size:64;not null"`
	ReplicaIndex string `gorm:"size:32;not null"`
	SampledAt    time.Time
	CPUPercent   float64
	MemoryBytes  int64
	GPUPercent   *float64
}

// TableName overrides the default GORM table name.
func (ServiceMetricRow) TableName() string { return "service_resource_metrics" }

// BucketRow is one time bucket of the aggregation query: the mean
// CPU/memory/GPU across the replicas sampled in the bucket.
type BucketRow struct {
	Bucket       int64
	CPUPercent   float64
	MemoryBytes  int64
	GPUPercent   float64
	HasGPU       bool
	SampleCount  int64
	BucketSeconds int64
}

// ReplicaRow is one replica's aggregate in the per-replica table.
type ReplicaRow struct {
	ReplicaIndex      string
	CurrentCPUPercent float64
	CurrentMemoryBytes int64
	CurrentGPUPercent float64
	HasGPU            bool
	DataThrough       int64
}