// Package batch implements the batch inference service: the batch-job
// lifecycle, the JSONL input/output file store, the JSONL validator, the
// batch worker, and the retention runner (feature #42).
package batch

import "time"

// Batch job lifecycle statuses (AD3).
const (
	StatusValidating = "validating"
	StatusInProgress = "in_progress"
	StatusFinalizing = "finalizing"
	StatusCompleted  = "completed"
	StatusFailed     = "failed"
	StatusExpired    = "expired"
	StatusCancelled  = "cancelled"
)

// BatchDiscountFactor is the batch inference discount applied to the
// real-time price (AD4).
const BatchDiscountFactor = 0.5

// BatchJob is the GORM model for the batch_jobs table (AD2, §4.1).
type BatchJob struct { //nolint:revive // batch.BatchJob is the documented domain name
	BatchID           string     `gorm:"primaryKey;type:uuid"`
	OrganizationID    string     `gorm:"size:64;not null;index:idx_batch_jobs_org_created,priority:1"`
	Model             string     `gorm:"size:128;not null"`
	Status            string     `gorm:"size:16;not null;index"`
	CompletionWindow  string     `gorm:"size:8;not null"`
	InputFileKey      string     `gorm:"size:512;not null"`
	ResultFileKey     string     `gorm:"size:512"`
	ErrorFileKey      string     `gorm:"size:512"`
	TotalRequests     int64      `gorm:"not null;default:0"`
	ProcessedRequests int64      `gorm:"not null;default:0"`
	SucceededRequests int64      `gorm:"not null;default:0"`
	FailedRequests    int64      `gorm:"not null;default:0"`
	InputTokens       int64      `gorm:"not null;default:0"`
	OutputTokens      int64      `gorm:"not null;default:0"`
	Cost              int64      `gorm:"not null;default:0"`
	Currency          string     `gorm:"size:8;not null;default:USD"`
	FilesExpired      bool       `gorm:"not null;default:false"`
	Error             string     `gorm:"size:512;not null;default:''"`
	CreatedAt         time.Time  `gorm:"not null;index:idx_batch_jobs_org_created,priority:2"`
	CompletedAt       *time.Time `gorm:"index"`
	FilesExpireAt     *time.Time `gorm:"index"`
}

// TableName overrides the default GORM table name.
func (BatchJob) TableName() string { return "batch_jobs" }

// BatchFilter scopes a batch-job list query (AC2).
type BatchFilter struct { //nolint:revive // batch.BatchFilter is the documented domain name
	OrganizationID string
	Status         string
	Model          string
	Offset         int
	Limit          int
}

// cancellable reports whether a job can be cancelled (AC3).
func cancellable(status string) bool {
	return status == StatusValidating || status == StatusInProgress
}
