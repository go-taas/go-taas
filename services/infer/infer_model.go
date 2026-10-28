package infer

import (
	"time"

	"gorm.io/datatypes"
)

// Inference service lifecycle states (closed set, contract constraint 2).
//
//	pending    created, the controller has not picked the change up yet
//	queued     the controller accepted the change; nothing is scheduled
//	starting   a pod is placed on a node and the engine is loading weights
//	running    the engine itself answers its health endpoint
//	failed     scheduling, pulling, starting or health-checking failed
//	terminated the service was deleted
//
// deploying is the legacy name for the phase between queued and starting;
// it stays reportable so an older controller keeps working.
const (
	StatePending    = "pending"
	StateQueued     = "queued"
	StateStarting   = "starting"
	StateDeploying  = "deploying"
	StateRunning    = "running"
	StateFailed     = "failed"
	StateTerminated = "terminated"
)

// InferenceService is the GORM model of the inference_services table and
// the single source of truth for its schema (created by AutoMigrate, no
// hand-written DDL).
type InferenceService struct {
	// ID is the server-generated UUID v4 exposed as service_id.
	ID string `gorm:"primaryKey;type:uuid"`
	// OrganizationID is the owning organization (transitional plain
	// string; foreign key deferred to features #6/#7).
	OrganizationID string `gorm:"size:64;not null;uniqueIndex:idx_infer_services_org_name,priority:1;index:idx_infer_services_org_updated,priority:1"`
	// Name is the DNS-safe service name, 1-63 chars.
	Name string `gorm:"size:63;not null;uniqueIndex:idx_infer_services_org_name,priority:2"`
	// ModelID is the deployed model's id.
	ModelID string `gorm:"type:uuid;not null;index"`
	// ModelVersion is the pinned version (never "latest").
	ModelVersion string `gorm:"size:64;not null"`
	// ImageID is the engine image's id from the image registry
	// (config-driven in feature #2; the images table arrives in
	// feature #3).
	ImageID string `gorm:"size:64;not null"`
	// ClusterID is the cluster the service is placed on (feature #40,
	// AD7). Empty means the platform default cluster was used.
	ClusterID string `gorm:"size:64;not null;default:'';index"`
	// Replicas is the desired replica count, 1-100.
	Replicas int `gorm:"not null"`
	// Accelerator names the hardware platform (nvidia/iluvatar/metax).
	Accelerator string `gorm:"size:32;not null"`
	// AcceleratorType is the concrete card model (e.g. A800, BI-V150).
	AcceleratorType string `gorm:"size:64;not null"`
	// State is the observed lifecycle state.
	State string `gorm:"size:16;not null;default:'pending'"`
	// FailureReason is the human-readable reason when state=failed.
	FailureReason *string `gorm:"size:512"`
	// EngineArgs are the serving-engine parameters, as a JSON object of
	// flag name (no leading dashes) to value. A value of "flag:true" is a
	// valueless switch. Passed to the engine by the controller.
	EngineArgs datatypes.JSON `gorm:"type:jsonb;not null;default:'{}'"`
	// ExtraArgs are free-form engine arguments appended after EngineArgs,
	// so a parameter the console does not know about is still usable.
	ExtraArgs datatypes.JSON `gorm:"type:jsonb;not null;default:'[]'"`
	// Endpoints holds the OpenAI-compatible base URLs as a JSON array,
	// set by the controller through the status subject. The jsonb type
	// applies on PostgreSQL; SQLite (tests) stores the same JSON text.
	Endpoints []string `gorm:"type:jsonb;serializer:json;not null;default:'[]'"`
	// Autoscaling is the per-service autoscaling policy as a JSON object
	// (feature #16, AD1). Empty {} means "inherit the global default".
	Autoscaling datatypes.JSON `gorm:"type:jsonb;not null;default:'{}'"`
	// AutoscalingState is the observed autoscaling state (feature #16,
	// FR3.2). Write-only by the controller; read-only on the wire.
	AutoscalingState string `gorm:"size:32;not null;default:''"`
	// AutoscalingCurrentReplicas is the observed replica count.
	AutoscalingCurrentReplicas int `gorm:"not null;default:0"`
	// AutoscalingDesiredReplicas is the desired replica count.
	AutoscalingDesiredReplicas int `gorm:"not null;default:0"`
	// AutoscalingCurrentConcurrency is the observed in-flight concurrency.
	AutoscalingCurrentConcurrency int `gorm:"not null;default:0"`
	// AutoscalingTargetConcurrency is the effective target concurrency.
	AutoscalingTargetConcurrency int `gorm:"not null;default:0"`
	// AutoscalingLastScalingEventAt is the last scaling event time.
	AutoscalingLastScalingEventAt *time.Time
	// AutoscalingErrorReason is the failure reason when state=error.
	AutoscalingErrorReason string `gorm:"size:512;not null;default:''"`
	// PoolID is the vGPU slicing pool the service opted into (feature
	// #52, AD2). Empty = whole card.
	PoolID string `gorm:"column:pool_id;size:64;not null;default:'';index"`
	// SliceTemplateID is the slice template the service requested
	// (feature #52, AD2). Empty = whole card.
	SliceTemplateID string `gorm:"column:slice_template_id;size:64;not null;default:''"`
	// SliceNodeID is the physical node the service's slices landed on
	// once HAMI schedules them (feature #52, AD14). Empty before
	// scheduling.
	SliceNodeID string `gorm:"column:slice_node_id;size:64;not null;default:''"`
	// SliceGPUIndex is the physical GPU index the slices landed on
	// (feature #52, AD14). -1 before scheduling.
	SliceGPUIndex int `gorm:"column:slice_gpu_index;not null;default:-1"`
	// SliceMemoryGiB is the assigned slice memory (feature #52, AD14).
	SliceMemoryGiB int64 `gorm:"column:slice_memory_gib;not null;default:0"`
	// SliceComputePct is the assigned slice compute percentage (feature
	// #52, AD14).
	SliceComputePct int64 `gorm:"column:slice_compute_pct;not null;default:0"`
	// CreatedAt is the creation time (UTC).
	CreatedAt time.Time
	// UpdatedAt is the last state change.
	UpdatedAt time.Time `gorm:"index:idx_infer_services_org_updated,priority:2,sort:DESC"`
}

// TableName returns the table name of InferenceService.
func (InferenceService) TableName() string { return "inference_services" }
