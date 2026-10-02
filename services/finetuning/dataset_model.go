// Package finetuning implements the dataset registry and the
// fine-tuning job lifecycle (feature #39). It serves the admin
// Fine-tuning pages, registering datasets, creating/monitoring
// fine-tuning jobs, and deploying fine-tuned models as inference
// services.
package finetuning

import (
	"strings"
	"time"
)

// FineTuningDataset is the GORM model for the datasets table (AD3).
type FineTuningDataset struct { //nolint:revive // finetuning.FineTuningDataset is the documented domain name
	ID         string `gorm:"primaryKey"`
	Name       string `gorm:"size:128;not null"`
	Format     string `gorm:"size:8;not null"`
	ObjectPath string `gorm:"size:512;not null"`
	CreatedAt  time.Time
}

// TableName overrides the default GORM table name.
func (FineTuningDataset) TableName() string { return "datasets" }

// validDatasetFormat reports whether a dataset format is supported.
func validDatasetFormat(format string) bool {
	return format == "jsonl" || format == "csv"
}

// validObjectPath validates an object-storage path with the same syntax
// rules as weight_path (AD3): non-empty, <= 512 chars, no ".." segments,
// no leading "/", no backslashes.
func validObjectPath(path string) bool {
	if path == "" || len(path) > 512 {
		return false
	}
	if strings.HasPrefix(path, "/") || strings.Contains(path, "\\") {
		return false
	}
	for _, seg := range strings.Split(path, "/") {
		if seg == ".." {
			return false
		}
	}
	return true
}