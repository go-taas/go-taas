package image

import (
	"time"

	"gorm.io/datatypes"
)

// Image is the DB-backed inference engine image catalog row. The GORM
// model is the single source of truth for the schema (created by
// AutoMigrate, no hand-written DDL).
type Image struct {
	// ID is the opaque image identifier: config-seeded rows keep their
	// config imageId (e.g. img-vllm-nvidia-v063), new registrations get
	// a UUID v4 (D4).
	ID string `gorm:"primaryKey;size:64"`
	// Name is the image repository name without tag, 1-255 chars,
	// lowercase (D3).
	Name string `gorm:"size:255;not null;uniqueIndex:idx_images_name_tag_accelerator,priority:1"`
	// Tag is the image tag, 1-128 chars (D3).
	Tag string `gorm:"size:128;not null;uniqueIndex:idx_images_name_tag_accelerator,priority:2"`
	// Digest optionally pins integrity: "sha256:" + 64 hex chars.
	Digest string `gorm:"size:71;not null;default:''"`
	// Accelerator names the hardware platform (nvidia, iluvatar, metax).
	Accelerator string `gorm:"size:32;not null;uniqueIndex:idx_images_name_tag_accelerator,priority:3"`
	// Engine names the inference engine (vllm, sglang, ...).
	Engine string `gorm:"size:64;not null"`
	// Description is free-text build notes, the only editable field (D6).
	Description string `gorm:"size:1024;not null;default:''"`
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// TableName overrides the default GORM table name.
func (Image) TableName() string { return "images" }

// Warmup task states (closed set, D8).
const (
	TaskStatePending   = "pending"
	TaskStateRunning   = "running"
	TaskStateSucceeded = "succeeded"
	TaskStateFailed    = "failed"
)

// NodeResult is one node's pre-pull outcome inside a warmup task.
type NodeResult struct {
	// Node is the Kubernetes node name.
	Node string `json:"node"`
	// State is the per-node outcome: succeeded or failed.
	State string `json:"state"`
	// Message carries the failure detail when State is failed.
	Message string `json:"message"`
}

// WarmupTask is one asynchronous image pre-pull task. Task rows are
// never deleted: they are the warmup history.
type WarmupTask struct {
	// ID is the server-generated UUID v4, exposed as task_id.
	ID string `gorm:"primaryKey;type:uuid"`
	// ImageID is the image being pre-pulled.
	ImageID string `gorm:"size:64;not null;index"`
	// State is the closed set pending/running/succeeded/failed (D8).
	State string `gorm:"size:16;not null;default:'pending'"`
	// NodeSelector restricts the pre-pull to nodes matching the labels.
	NodeSelector datatypes.JSON `gorm:"type:jsonb;not null;default:'{}'"`
	// NodeResults carries per-node outcomes, filled by status reports.
	NodeResults datatypes.JSON `gorm:"type:jsonb;not null;default:'[]'"`
	// FailureReason is the human-readable reason when State is failed.
	FailureReason *string `gorm:"size:512"`
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

// TableName overrides the default GORM table name.
func (WarmupTask) TableName() string { return "warmup_tasks" }
