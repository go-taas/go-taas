package infer

import (
	"time"
)

// Inference service lifecycle states (closed set, contract constraint 2).
const (
	StatePending    = "pending"
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
	// Endpoints holds the OpenAI-compatible base URLs as a JSON array,
	// set by the controller through the status subject. The jsonb type
	// applies on PostgreSQL; SQLite (tests) stores the same JSON text.
	Endpoints []string `gorm:"type:jsonb;serializer:json;not null;default:'[]'"`
	// CreatedAt is the creation time (UTC).
	CreatedAt time.Time
	// UpdatedAt is the last state change.
	UpdatedAt time.Time `gorm:"index:idx_infer_services_org_updated,priority:2,sort:DESC"`
}

// TableName returns the table name of InferenceService.
func (InferenceService) TableName() string { return "inference_services" }
