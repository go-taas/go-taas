package model

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/go-taas/go-taas/pkg/database"
	apierrors "github.com/go-taas/go-taas/pkg/errors"
)

// Repository persists models and model versions on top of the
// generic repository base. All reads and writes join an open transaction
// via the context.
type Repository struct {
	*database.BaseRepository[Model]
	versions *database.BaseRepository[Version]
	db       *database.Manager
}

// NewRepository constructs a Repository bound to a database
// Manager.
func NewRepository(db *gorm.DB) *Repository {
	mgr := database.NewManager(db)
	return &Repository{
		BaseRepository: database.NewBaseRepository[Model](mgr),
		versions:       database.NewBaseRepository[Version](mgr),
		db:             mgr,
	}
}

// CreateModel inserts a new model row, generating the id when unset.
// A unique violation on name maps to CodeModelExists.
func (r *Repository) CreateModel(ctx context.Context, m *Model) error {
	if m.ID == "" {
		m.ID = uuid.NewString()
	}
	if err := r.Create(ctx, m); err != nil {
		if isUniqueViolation(err) {
			return apierrors.New(apierrors.CodeModelExists)
		}
		return err
	}
	return nil
}

// FindByName returns the model with the given name. A miss maps to
// CodeModelNotFound.
func (r *Repository) FindByName(ctx context.Context, name string) (*Model, error) {
	var row Model
	err := r.DB(ctx).Where("name = ?", name).First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, apierrors.New(apierrors.CodeModelNotFound)
	}
	if err != nil {
		return nil, err
	}
	return &row, nil
}

// ListModels returns one page of models ordered by creation time
// (newest first) and the total count.
func (r *Repository) ListModels(ctx context.Context, offset, limit int) ([]*Model, int64, error) {
	return r.Paginate(ctx, offset, limit, nil, "created_at DESC", "id DESC")
}

// GetModel returns the model with the given id. A miss maps to
// CodeModelNotFound.
func (r *Repository) GetModel(ctx context.Context, id string) (*Model, error) {
	row, err := r.GetByID(ctx, id)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, apierrors.New(apierrors.CodeModelNotFound)
	}
	if err != nil {
		return nil, err
	}
	return row, nil
}

// CreateVersion inserts a new model version row, generating the id
// when unset. A unique violation on (model_id, version) maps to
// CodeModelExists (AC1).
func (r *Repository) CreateVersion(ctx context.Context, v *Version) error {
	if v.ID == "" {
		v.ID = uuid.NewString()
	}
	if err := r.versions.Create(ctx, v); err != nil {
		if isUniqueViolation(err) {
			return apierrors.New(apierrors.CodeModelExists)
		}
		return err
	}
	return nil
}

// ListVersionsByModel returns all versions of the model ordered by
// created_at DESC, version DESC (newest first, AC2).
func (r *Repository) ListVersionsByModel(ctx context.Context, modelID string) ([]*Version, error) {
	return r.versions.List(ctx, []any{"model_id = ?", modelID},
		"created_at DESC", "version DESC", "id DESC")
}

// FindVersion returns the specific version row of the model. A miss maps
// to CodeModelVersionNotFound; it is used by infer's deploy validation.
func (r *Repository) FindVersion(ctx context.Context, modelID, version string) (*Version, error) {
	var row Version
	err := r.versions.DB(ctx).
		Where("model_id = ? AND version = ?", modelID, version).
		First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, apierrors.New(apierrors.CodeModelVersionNotFound)
	}
	if err != nil {
		return nil, err
	}
	return &row, nil
}

// LatestVersion returns the newest version row of the model (nil when
// the model has no versions yet).
func (r *Repository) LatestVersion(ctx context.Context, modelID string) (*Version, error) {
	rows, err := r.ListVersionsByModel(ctx, modelID)
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, nil
	}
	return rows[0], nil
}

// DeleteModel hard-deletes the model row and cascades its versions in
// one transaction.
func (r *Repository) DeleteModel(ctx context.Context, id string) error {
	return r.db.WithinTx(ctx, func(ctx context.Context) error {
		if err := r.versions.DB(ctx).Where("model_id = ?", id).Delete(&Version{}).Error; err != nil {
			return err
		}
		return r.Delete(ctx, id)
	})
}

// RegisterModelOrCreateVersion is the transactional core of the
// RegisterModel RPC: when the named model does not exist it creates the
// model plus its first version; when it exists it appends the version
// (touching models.updated_at). A duplicate (model, version) pair maps
// to CodeModelExists. It returns the model id (stable across versions).
func (r *Repository) RegisterModelOrCreateVersion(ctx context.Context, name, description, version, weightPath string) (string, error) {
	var modelID string
	err := r.db.WithinTx(ctx, func(ctx context.Context) error {
		existing, err := r.FindByName(ctx, name)
		if err != nil {
			if ae, ok := apierrors.As(err); !ok || ae.Code != apierrors.CodeModelNotFound {
				return err
			}
			// Model does not exist: create it with the first version.
			m := &Model{
				ID:          uuid.NewString(),
				Name:        name,
				Description: description,
				CreatedAt:   time.Now().UTC(),
				UpdatedAt:   time.Now().UTC(),
			}
			if err := r.CreateModel(ctx, m); err != nil {
				return err
			}
			v := &Version{
				ID:         uuid.NewString(),
				ModelID:    m.ID,
				Version:    version,
				WeightPath: weightPath,
				CreatedAt:  time.Now().UTC(),
			}
			if err := r.CreateVersion(ctx, v); err != nil {
				return err
			}
			modelID = m.ID
			return nil
		}

		// Model exists: append the version or report the conflict.
		if _, err := r.FindVersion(ctx, existing.ID, version); err == nil {
			return apierrors.New(apierrors.CodeModelExists)
		} else if ae, ok := apierrors.As(err); !ok || ae.Code != apierrors.CodeModelVersionNotFound {
			return err
		}
		v := &Version{
			ID:         uuid.NewString(),
			ModelID:    existing.ID,
			Version:    version,
			WeightPath: weightPath,
			CreatedAt:  time.Now().UTC(),
		}
		if err := r.CreateVersion(ctx, v); err != nil {
			return err
		}
		if err := r.UpdateFields(ctx, existing.ID, map[string]any{
			"updated_at": time.Now().UTC(),
		}); err != nil {
			return err
		}
		modelID = existing.ID
		return nil
	})
	if err != nil {
		return "", err
	}
	return modelID, nil
}

// isUniqueViolation reports whether err is a unique-constraint violation
// across the supported dialects (PostgreSQL 23505, SQLite generic message).
func isUniqueViolation(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "23505") ||
		strings.Contains(msg, "UNIQUE constraint failed")
}
