package finetuning

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/go-taas/go-taas/pkg/database"
	apierrors "github.com/go-taas/go-taas/pkg/errors"
)

// DatasetRepository persists fine-tuning datasets.
type DatasetRepository struct {
	db *database.Manager
}

// NewDatasetRepository constructs a DatasetRepository bound to a GORM
// database.
func NewDatasetRepository(db *gorm.DB) *DatasetRepository {
	return &DatasetRepository{db: database.NewManager(db)}
}

// CreateDataset inserts a new dataset row.
func (r *DatasetRepository) CreateDataset(ctx context.Context, d *FineTuningDataset) error {
	return r.db.DB(ctx).Create(d).Error
}

// ListDatasets returns the datasets, newest first.
func (r *DatasetRepository) ListDatasets(ctx context.Context) ([]FineTuningDataset, error) {
	var rows []FineTuningDataset
	err := r.db.DB(ctx).Order("created_at DESC").Find(&rows).Error
	return rows, err
}

// FindDatasetByID returns one dataset by id. A miss maps to
// CodeFineTuningDatasetNotFound.
func (r *DatasetRepository) FindDatasetByID(ctx context.Context, datasetID string) (*FineTuningDataset, error) {
	if _, err := uuid.Parse(datasetID); err != nil {
		return nil, apierrors.New(apierrors.CodeFineTuningDatasetNotFound)
	}
	var row FineTuningDataset
	err := r.db.DB(ctx).Where("id = ?", datasetID).First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, apierrors.New(apierrors.CodeFineTuningDatasetNotFound)
	}
	if err != nil {
		return nil, err
	}
	return &row, nil
}