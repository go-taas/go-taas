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

// ProviderFilter narrows a provider list query.
type ProviderFilter struct { //nolint:revive // provider.ProviderFilter is the documented domain name
	Name        string
	Type        string
	Enabled     string // "" | "true" | "false"
	ProbeStatus string
	Offset      int
	Limit       int
}

// ProviderRepository persists providers and their probe results.
type ProviderRepository struct { //nolint:revive // provider.ProviderRepository is the documented domain name
	db *database.Manager
}

// NewProviderRepository constructs a ProviderRepository bound to a GORM
// database.
func NewProviderRepository(db *gorm.DB) *ProviderRepository {
	return &ProviderRepository{db: database.NewManager(db)}
}

// Create inserts a provider, generating the id when unset. A unique
// violation on name maps to 13002.
func (r *ProviderRepository) Create(ctx context.Context, p *Provider) error {
	if p.ID == "" {
		p.ID = uuid.NewString()
	}
	if err := r.db.DB(ctx).Create(p).Error; err != nil {
		if isUniqueViolation(err) {
			return apierrors.New(apierrors.CodeProviderNameConflict)
		}
		return err
	}
	return nil
}

// GetByID returns the provider with the given id (13001 on a miss).
func (r *ProviderRepository) GetByID(ctx context.Context, id string) (*Provider, error) {
	var row Provider
	err := r.db.DB(ctx).Where("id = ?", id).First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, apierrors.New(apierrors.CodeProviderNotFound)
	}
	if err != nil {
		return nil, err
	}
	return &row, nil
}

// FindByName returns the provider with the given name, or nil when none
// exists (used for the uniqueness check).
func (r *ProviderRepository) FindByName(ctx context.Context, name string) (*Provider, error) {
	var row Provider
	err := r.db.DB(ctx).Where("name = ?", name).First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &row, nil
}

// Update persists the mutable columns of a provider row.
func (r *ProviderRepository) Update(ctx context.Context, p *Provider) error {
	err := r.db.DB(ctx).Model(&Provider{}).Where("id = ?", p.ID).
		Updates(map[string]any{
			"name":                    p.Name,
			"type":                    p.Type,
			"base_url":                p.BaseURL,
			"enabled":                 p.Enabled,
			"request_timeout_seconds": p.RequestTimeoutSeconds,
			"extra_headers":           p.ExtraHeaders,
			"api_key_ciphertext":      p.APIKeyCiphertext,
			"api_key_hint":            p.APIKeyHint,
			"updated_by":              p.UpdatedBy,
			"updated_at":              time.Now().UTC(),
		}).Error
	if err != nil {
		if isUniqueViolation(err) {
			return apierrors.New(apierrors.CodeProviderNameConflict)
		}
		return err
	}
	return nil
}

// UpdateProbe persists the last probe result on the provider row.
func (r *ProviderRepository) UpdateProbe(ctx context.Context, id string, res ProbeResult) error {
	return r.db.DB(ctx).Model(&Provider{}).Where("id = ?", id).
		Updates(map[string]any{
			"probe_status":      res.Status,
			"probe_latency_ms":  res.LatencyMs,
			"probe_http_status": res.HTTPStatus,
			"probe_message":     res.Message,
			"probe_checked_at":  res.CheckedAt,
		}).Error
}

// List returns one page of providers and the total count.
func (r *ProviderRepository) List(ctx context.Context, f ProviderFilter) ([]*Provider, int64, error) {
	q := r.db.DB(ctx).Model(&Provider{})
	if s := strings.TrimSpace(f.Name); s != "" {
		q = q.Where("name LIKE ?", "%"+s+"%")
	}
	if s := strings.TrimSpace(f.Type); s != "" {
		q = q.Where("type = ?", s)
	}
	switch f.Enabled {
	case "true":
		q = q.Where("enabled = ?", true)
	case "false":
		q = q.Where("enabled = ?", false)
	}
	if s := strings.TrimSpace(f.ProbeStatus); s != "" {
		q = q.Where("probe_status = ?", s)
	}
	var total int64
	if err := q.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	rows := make([]*Provider, 0)
	err := q.Order("created_at DESC, id DESC").Offset(f.Offset).Limit(f.Limit).Find(&rows).Error
	if err != nil {
		return nil, 0, err
	}
	return rows, total, nil
}

// ListEnabled returns every enabled provider (the probe worker's input).
func (r *ProviderRepository) ListEnabled(ctx context.Context) ([]*Provider, error) {
	rows := make([]*Provider, 0)
	err := r.db.DB(ctx).Where("enabled = ?", true).Find(&rows).Error
	if err != nil {
		return nil, err
	}
	return rows, nil
}

// DeleteWithModels deletes a provider and its curated rows in one
// transaction.
func (r *ProviderRepository) DeleteWithModels(ctx context.Context, id string) error {
	return r.db.WithinTx(ctx, func(ctx context.Context) error {
		if err := r.db.DB(ctx).Where("provider_id = ?", id).Delete(&ProviderModel{}).Error; err != nil {
			return err
		}
		return r.db.DB(ctx).Where("id = ?", id).Delete(&Provider{}).Error
	})
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
