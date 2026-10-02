package finetuning

import (
	"encoding/json"
	"time"
)

// Fine-tuning job states (AD4).
const (
	JobStatePending   = "pending"
	JobStateRunning   = "running"
	JobStateSucceeded = "succeeded"
	JobStateFailed    = "failed"
)

// FineTuningJob is the GORM model for the finetuning_jobs table (AD4).
type FineTuningJob struct { //nolint:revive // finetuning.FineTuningJob is the documented domain name
	ID                 string `gorm:"primaryKey"`
	Name               string `gorm:"size:128;not null"`
	BaseModelID        string `gorm:"size:64;not null"`
	BaseModelVersion   string `gorm:"size:64;not null"`
	DatasetID          string `gorm:"size:64;not null;index"`
	Hyperparameters    string `gorm:"type:jsonb;not null"`
	State              string `gorm:"size:16;not null;index"`
	FailureReason      string `gorm:"size:512;not null;default:''"`
	FineTunedModelID   string `gorm:"size:64"`
	CreatedAt          time.Time
	UpdatedAt          time.Time
}

// TableName overrides the default GORM table name.
func (FineTuningJob) TableName() string { return "finetuning_jobs" }

// Hyperparameters is the decoded hyperparameters of a job.
type Hyperparameters struct {
	Epochs       int32   `json:"epochs"`
	BatchSize    int32   `json:"batch_size"`
	LearningRate float64 `json:"learning_rate"`
}

// encodeHyperparameters serializes hyperparameters to the jsonb column.
func encodeHyperparameters(h Hyperparameters) (string, error) {
	b, err := json.Marshal(h)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// decodeHyperparameters parses the jsonb column.
func decodeHyperparameters(raw string) (Hyperparameters, error) {
	var h Hyperparameters
	if err := json.Unmarshal([]byte(raw), &h); err != nil {
		return h, err
	}
	return h, nil
}

// validHyperparameters validates the hyperparameters (AD10): epochs and
// batch_size must be positive, learning_rate must be > 0.
func validHyperparameters(h Hyperparameters) bool {
	return h.Epochs > 0 && h.BatchSize > 0 && h.LearningRate > 0
}