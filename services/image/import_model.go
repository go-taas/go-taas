package image

import "time"

// Import states of an asynchronous image import task.
const (
	// ImportStatePending is a task that has not started yet.
	ImportStatePending = "pending"
	// ImportStateRunning is a task whose image is being copied.
	ImportStateRunning = "running"
	// ImportStateSucceeded is a task whose image was pushed and registered.
	ImportStateSucceeded = "succeeded"
	// ImportStateFailed is a task that failed; FailureReason carries why.
	ImportStateFailed = "failed"
)

// ImportTask is the GORM model of the image_import_tasks table: one
// asynchronous copy of an engine image from a source registry into the
// internal Harbor project. The task owns the whole import flow (copy,
// then register the catalog row) so the console can start it and poll
// its progress instead of holding an HTTP request open for a
// multi-gigabyte transfer.
type ImportTask struct {
	// ID is the server-generated UUID v4 exposed as task_id.
	ID string `gorm:"primaryKey;type:uuid"`
	// SourceReference is the full source image reference to pull.
	SourceReference string `gorm:"size:512;not null"`
	// DestinationReference is the derived Harbor reference the image is
	// pushed to (only when succeeded).
	DestinationReference string `gorm:"size:512;not null;default:''"`
	// Accelerator names the hardware platform (nvidia, iluvatar, metax).
	Accelerator string `gorm:"size:32;not null"`
	// Engine names the inference engine (vllm, sglang, ...).
	Engine string `gorm:"size:64;not null"`
	// Description is free-text build notes.
	Description string `gorm:"size:1024;not null;default:''"`
	// SourceUsername optionally authenticates against a private source
	// registry.
	SourceUsername string `gorm:"size:255;not null;default:''"`
	// SourcePassword optionally authenticates against a private source
	// registry. It is stored only for the lifetime of the task and
	// cleared once the copy finishes.
	SourcePassword string `gorm:"size:512;not null;default:''"`
	// State is one of the ImportState values.
	State string `gorm:"size:16;not null;index"`
	// FailureReason carries the failure detail; empty on success.
	FailureReason string `gorm:"size:1024;not null;default:''"`
	// ImageID is the catalog image the task registered (only when
	// succeeded).
	ImageID string `gorm:"size:64;not null;default:''"`
	// CreatedAt is the enqueue time (UTC).
	CreatedAt time.Time `gorm:"index:idx_image_import_tasks_created,priority:1,sort:DESC"`
	// UpdatedAt is the last state update (UTC).
	UpdatedAt time.Time
}

// TableName returns the table name of ImportTask.
func (ImportTask) TableName() string { return "image_import_tasks" }