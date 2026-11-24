package model

import "time"

// UploadSessionState values for a chunked model upload.
const (
	UploadStateCreating    = "creating"
	UploadStateUploading   = "uploading"
	UploadStateCompleting  = "completing"
	UploadStateProcessing  = "processing"  // handed to a transfer Job
	UploadStateInterrupted = "interrupted" // the browser stopped driving it
	UploadStateSucceeded   = "succeeded"
	UploadStateFailed      = "failed"
	UploadStateCancelled   = "cancelled"
)

// ModelUpload is the GORM model of the model_uploads table: one chunked model
// archive upload from an operator's browser to the staging object store,
// followed by a transfer Job that unpacks it into the target backend and a
// server-side catalog registration.
type ModelUpload struct { //nolint:revive // the name is part of the model package API
	// ID is the server-generated UUID v4, the idempotency key.
	ID string `gorm:"primaryKey;type:uuid"`
	// OrganizationID scopes the row to a tenant/platform.
	OrganizationID string `gorm:"size:64;not null;index"`
	// Name is the catalog model name (required).
	Name string `gorm:"size:128;not null"`
	// HubModelID is the "<vendor>/<model>" hub identity (required, unique).
	HubModelID string `gorm:"size:256;not null"`
	// Version is the catalog version; empty derives one at complete time.
	Version string `gorm:"size:64;not null;default:''"`
	// Description is the free-text model description.
	Description string `gorm:"size:1024;not null;default:''"`
	// StorageBackend is the target backend. Empty uses the platform default.
	StorageBackend string `gorm:"size:63;not null;default:''"`
	// ArchiveName is the uploaded file's name.
	ArchiveName string `gorm:"size:256;not null;default:''"`
	// TotalBytes is the number of archive bytes expected.
	TotalBytes int64 `gorm:"not null;default:0"`
	// PartSizeBytes is the size of one chunk.
	PartSizeBytes int64 `gorm:"not null;default:0"`
	// UploadID is the object-store multipart upload id.
	UploadID string `gorm:"size:128;not null;default:''"`
	// ObjectKey is the staging object-store key of the composed archive.
	ObjectKey string `gorm:"size:1024;not null;default:''"`
	// Format is the resolved unpack format.
	Format string `gorm:"size:16;not null;default:''"`
	// State is creating, uploading, completing, processing, succeeded, failed
	// or cancelled.
	State string `gorm:"size:16;not null;index"`
	// BytesReceived is the number of bytes staged so far.
	BytesReceived int64 `gorm:"not null;default:0"`
	// ModelID is the catalog model registered on success; null until then.
	ModelID *string `gorm:"type:uuid;index"`
	// TransferID links the processing transfer Job that unpacks the archive;
	// null until the Job is dispatched.
	TransferID *string `gorm:"type:uuid;index"`
	// Message carries the failure reason.
	Message string `gorm:"size:1024;not null;default:''"`
	// Actor is the creating user id, for the audit trail.
	Actor string `gorm:"size:64;not null;default:''"`
	// CreatedAt is the create time (UTC).
	CreatedAt time.Time `gorm:"index:idx_model_uploads_org_created,priority:2,sort:DESC"`
	// UpdatedAt is the last state/progress update (UTC).
	UpdatedAt time.Time
}

// TableName returns the table name of ModelUpload.
func (ModelUpload) TableName() string { return "model_uploads" }
