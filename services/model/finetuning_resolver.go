package model

import (
	"context"

	"gorm.io/gorm"

	"github.com/go-taas/go-taas/pkg/database"
)

// FineTuningModelResolver resolves base models and registers fine-tuned
// models for the finetuning module (feature #39, AD2/AD6). It is
// consumed by the finetuning service.
type FineTuningModelResolver struct {
	db *database.Manager
}

// NewFineTuningModelResolver constructs a FineTuningModelResolver bound
// to a GORM database.
func NewFineTuningModelResolver(db *gorm.DB) *FineTuningModelResolver {
	return &FineTuningModelResolver{db: database.NewManager(db)}
}

// ModelVersionExists reports whether a (model, version) exists.
func (r *FineTuningModelResolver) ModelVersionExists(ctx context.Context, modelID, version string) (bool, error) {
	var count int64
	err := r.db.DB(ctx).Table("model_versions").
		Where("model_id = ? AND version = ?", modelID, version).
		Count(&count).Error
	if err != nil {
		return false, err
	}
	return count > 0, nil
}

// RegisterModelVersion registers a fine-tuned model version and returns
// the model id.
func (r *FineTuningModelResolver) RegisterModelVersion(ctx context.Context, name, version, weightPath string) (string, error) {
	repo := NewRepository(r.db.DB(ctx))
	return repo.RegisterModelOrCreateVersion(ctx, name, "", version, weightPath)
}