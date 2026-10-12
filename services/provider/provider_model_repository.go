package provider

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

// ProviderModelFilter narrows a curated-model list query.
type ProviderModelFilter struct { //nolint:revive // provider.ProviderModelFilter is the documented domain name
	UpstreamModelID string
	Enabled         string // "" | "true" | "false"
	Published       string // "" | "true" | "false"
	Offset          int
	Limit           int
}

// ProviderModelRepository persists curated upstream-model rows.
type ProviderModelRepository struct { //nolint:revive // provider.ProviderModelRepository is the documented domain name
	db *database.Manager
}

// NewProviderModelRepository constructs a ProviderModelRepository bound to
// a GORM database.
func NewProviderModelRepository(db *gorm.DB) *ProviderModelRepository {
	return &ProviderModelRepository{db: database.NewManager(db)}
}

// Create inserts a curated row, generating the id when unset. A duplicate
// (provider_id, upstream_model_id) maps to 13009.
func (r *ProviderModelRepository) Create(ctx context.Context, m *ProviderModel) error {
	if m.ID == "" {
		m.ID = uuid.NewString()
	}
	if err := r.db.DB(ctx).Create(m).Error; err != nil {
		if isUniqueViolation(err) {
			return apierrors.New(apierrors.CodeProviderModelExists)
		}
		return err
	}
	return nil
}

// GetByID returns a curated row scoped to its provider (13008 on a miss).
func (r *ProviderModelRepository) GetByID(ctx context.Context, providerID, modelID string) (*ProviderModel, error) {
	var row ProviderModel
	err := r.db.DB(ctx).Where("id = ? AND provider_id = ?", modelID, providerID).First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, apierrors.New(apierrors.CodeProviderModelNotFound)
	}
	if err != nil {
		return nil, err
	}
	return &row, nil
}

// FindByUpstream returns the curated row for an upstream id, or nil.
func (r *ProviderModelRepository) FindByUpstream(ctx context.Context, providerID, upstreamID string) (*ProviderModel, error) {
	var row ProviderModel
	err := r.db.DB(ctx).Where("provider_id = ? AND upstream_model_id = ?", providerID, upstreamID).First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &row, nil
}

// Update persists the mutable columns of a curated row.
func (r *ProviderModelRepository) Update(ctx context.Context, m *ProviderModel) error {
	return r.db.DB(ctx).Model(&ProviderModel{}).Where("id = ?", m.ID).
		Updates(map[string]any{
			"display_name":   m.DisplayName,
			"context_window": m.ContextWindow,
			"enabled":        m.Enabled,
			"updated_at":     time.Now().UTC(),
		}).Error
}

// SetPublication binds (or clears) the published catalog model on a row.
func (r *ProviderModelRepository) SetPublication(ctx context.Context, modelID, catalogModelID string, published bool) error {
	return r.db.DB(ctx).Model(&ProviderModel{}).Where("id = ?", modelID).
		Updates(map[string]any{
			"published":        published,
			"catalog_model_id": catalogModelID,
			"updated_at":       time.Now().UTC(),
		}).Error
}

// Delete removes a curated row.
func (r *ProviderModelRepository) Delete(ctx context.Context, modelID string) error {
	return r.db.DB(ctx).Where("id = ?", modelID).Delete(&ProviderModel{}).Error
}

// List returns one page of curated rows for a provider and the total.
func (r *ProviderModelRepository) List(ctx context.Context, providerID string, f ProviderModelFilter) ([]*ProviderModel, int64, error) {
	q := r.db.DB(ctx).Model(&ProviderModel{}).Where("provider_id = ?", providerID)
	if s := strings.TrimSpace(f.UpstreamModelID); s != "" {
		q = q.Where("upstream_model_id LIKE ?", "%"+s+"%")
	}
	switch f.Enabled {
	case "true":
		q = q.Where("enabled = ?", true)
	case "false":
		q = q.Where("enabled = ?", false)
	}
	switch f.Published {
	case "true":
		q = q.Where("published = ?", true)
	case "false":
		q = q.Where("published = ?", false)
	}
	var total int64
	if err := q.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	rows := make([]*ProviderModel, 0)
	err := q.Order("created_at DESC, id DESC").Offset(f.Offset).Limit(f.Limit).Find(&rows).Error
	if err != nil {
		return nil, 0, err
	}
	return rows, total, nil
}

// CountByProvider returns the curated and published row counts.
func (r *ProviderModelRepository) CountByProvider(ctx context.Context, providerID string) (curated, published int64, err error) {
	if err = r.db.DB(ctx).Model(&ProviderModel{}).Where("provider_id = ?", providerID).Count(&curated).Error; err != nil {
		return 0, 0, err
	}
	if err = r.db.DB(ctx).Model(&ProviderModel{}).
		Where("provider_id = ? AND published = ?", providerID, true).Count(&published).Error; err != nil {
		return 0, 0, err
	}
	return curated, published, nil
}

// CountPublished returns the number of published rows for a provider.
func (r *ProviderModelRepository) CountPublished(ctx context.Context, providerID string) (int64, error) {
	var n int64
	err := r.db.DB(ctx).Model(&ProviderModel{}).
		Where("provider_id = ? AND published = ?", providerID, true).Count(&n).Error
	return n, err
}
