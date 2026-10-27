package model

import "time"

// Download states of a hub download job.
const (
	// DownloadStateQueued is a job that has not started yet.
	DownloadStateQueued = "queued"
	// DownloadStateDownloading is a job whose weights are being fetched.
	DownloadStateDownloading = "downloading"
	// DownloadStateSucceeded is a job whose weights are on disk and whose
	// model version was registered.
	DownloadStateSucceeded = "succeeded"
	// DownloadStateFailed is a job that failed; Message carries why.
	DownloadStateFailed = "failed"
)

// DownloadJob is the GORM model of the model_downloads table: one
// asynchronous hub download of a model's weights. The job owns the whole
// import flow (download, then register the model version) so the console
// can start it and poll its progress instead of holding an HTTP request
// open for a multi-gigabyte transfer.
type DownloadJob struct {
	// ID is the server-generated UUID v4 exposed as download_id.
	ID string `gorm:"primaryKey;type:uuid"`
	// Source is the model hub ("modelscope" or "huggingface").
	Source string `gorm:"size:32;not null"`
	// HubModelID is the model id on the hub, in the "<vendor>/<model>"
	// form. It is not unique per row: re-downloading a model after a
	// failure creates a new job.
	HubModelID string `gorm:"size:255;not null;index"`
	// Name is the catalog model name the job registers.
	Name string `gorm:"size:128;not null;default:''"`
	// Version is the catalog version the job registers.
	Version string `gorm:"size:64;not null;default:''"`
	// Description is the free-text model description.
	Description string `gorm:"size:1024;not null;default:''"`
	// State is one of the DownloadState values.
	State string `gorm:"size:16;not null;index"`
	// Message carries the failure reason; empty on success.
	Message string `gorm:"size:1024;not null;default:''"`
	// Bytes is the number of weight bytes already on disk, sampled while
	// the download runs so the console can show progress.
	Bytes int64 `gorm:"not null;default:0"`
	// TotalBytes is the model's size on the hub, sampled before the
	// transfer starts so progress is a percentage rather than an unknown.
	// Zero when the hub reports no sizes.
	TotalBytes int64 `gorm:"not null;default:0"`
	// WeightPath is the derived weight path (only when succeeded).
	WeightPath string `gorm:"size:512;not null;default:''"`
	// ModelID is the catalog model the job registered (only when
	// succeeded).
	ModelID string `gorm:"size:36;not null;default:''"`
	// CreatedAt is the enqueue time (UTC).
	CreatedAt time.Time `gorm:"index:idx_model_downloads_created,priority:1,sort:DESC"`
	// UpdatedAt is the last progress or state update (UTC).
	UpdatedAt time.Time
}

// TableName returns the table name of DownloadJob.
func (DownloadJob) TableName() string { return "model_downloads" }
