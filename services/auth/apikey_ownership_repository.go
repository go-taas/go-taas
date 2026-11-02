package auth

import (
	"context"
	"errors"
	"time"

	"gorm.io/gorm"

	apierrors "github.com/go-taas/go-taas/pkg/errors"
)

// APIKeyFilter narrows an API key list. The zero value lists every key
// of the organization (the administrator view).
type APIKeyFilter struct {
	// OrganizationID is required; keys never cross tenants.
	OrganizationID string
	// OwnerUserID restricts the list to one personal owner. Nil means
	// no owner filter (organization administrators).
	OwnerUserID *string
	// SharedOnly restricts the list to organization-shared keys (no
	// personal owner).
	SharedOnly bool
	// ProjectID restricts the list to one project.
	ProjectID string
	// ActiveOnly excludes revoked and expired keys server-side.
	ActiveOnly bool
}

// ListByFilter returns one page of keys matching the filter ordered by
// creation time (newest first, id tie-break for stable pagination) and
// the total count before pagination.
func (r *APIKeyRepository) ListByFilter(ctx context.Context, filter APIKeyFilter, offset, limit int, now time.Time) ([]*APIKey, int64, error) {
	query := r.DB(ctx).Model(&APIKey{}).Where("organization_id = ?", filter.OrganizationID)
	if filter.OwnerUserID != nil {
		query = query.Where("owner_user_id = ?", *filter.OwnerUserID)
	}
	if filter.SharedOnly {
		query = query.Where("owner_user_id IS NULL")
	}
	if filter.ProjectID != "" {
		query = query.Where("project_id = ?", filter.ProjectID)
	}
	if filter.ActiveOnly {
		query = query.Where("revoked = false AND (expires_at IS NULL OR expires_at > ?)", now)
	}
	var total int64
	if err := query.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var rows []*APIKey
	query = query.Order("created_at DESC").Order("id DESC")
	if offset > 0 {
		query = query.Offset(offset)
	}
	if limit > 0 {
		query = query.Limit(limit)
	}
	if err := query.Find(&rows).Error; err != nil {
		return nil, 0, err
	}
	return rows, total, nil
}

// FindByIDAndOrganization returns a key by id inside the organization,
// or (nil, nil) when it does not exist there (no cross-tenant existence
// leak).
func (r *APIKeyRepository) FindByIDAndOrganization(ctx context.Context, orgID, keyID string) (*APIKey, error) {
	var row APIKey
	err := r.DB(ctx).Where("id = ? AND organization_id = ?", keyID, orgID).First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &row, nil
}

// UpdateOwnershipByIDAndOrganization transfers a key's personal owner
// and project inside its organization. A missing key or a key of
// another organization returns nil (the caller maps it to
// CodeAPIKeyNotFound). The organization and the secret never change.
func (r *APIKeyRepository) UpdateOwnershipByIDAndOrganization(ctx context.Context, orgID, keyID string, ownerUserID, projectID *string) (*APIKey, error) {
	var row APIKey
	err := r.db.WithinTx(ctx, func(ctx context.Context) error {
		err := r.DB(ctx).
			Where("id = ? AND organization_id = ?", keyID, orgID).
			First(&row).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		fields := map[string]any{
			"owner_user_id": ownerUserID,
			"project_id":    projectID,
		}
		if err := r.UpdateFields(ctx, row.ID, fields); err != nil {
			return err
		}
		return r.DB(ctx).Where("id = ?", row.ID).First(&row).Error
	})
	if err != nil {
		return nil, err
	}
	if row.ID == "" {
		return nil, nil
	}
	return &row, nil
}

// RevokeByIDAndOrganizationOwned soft-revokes a key only when it
// belongs to the organization and is either owned by ownerUserID or
// organization-shared (ownerUserID nil matches shared keys only). The
// administrator path uses RevokeByIDAndOrganization.
func (r *APIKeyRepository) RevokeByIDAndOrganizationOwned(ctx context.Context, orgID, keyID, ownerUserID string, now time.Time) (string, error) {
	var lookupHash string
	err := r.db.WithinTx(ctx, func(ctx context.Context) error {
		var row APIKey
		err := r.DB(ctx).
			Where("id = ? AND organization_id = ? AND owner_user_id = ?", keyID, orgID, ownerUserID).
			First(&row).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return apiKeyNotFound()
		}
		if err != nil {
			return err
		}
		lookupHash = row.LookupHash
		return r.UpdateFields(ctx, row.ID, map[string]any{
			"revoked":    true,
			"revoked_at": gorm.Expr("COALESCE(revoked_at, ?)", now),
		})
	})
	if err != nil {
		return "", err
	}
	return lookupHash, nil
}

// DeleteByIDAndOrganizationOwned removes a key permanently only when it
// belongs to the organization and to the given owner.
func (r *APIKeyRepository) DeleteByIDAndOrganizationOwned(ctx context.Context, orgID, keyID, ownerUserID string) (string, error) {
	var lookupHash string
	err := r.db.WithinTx(ctx, func(ctx context.Context) error {
		var row APIKey
		err := r.DB(ctx).
			Where("id = ? AND organization_id = ? AND owner_user_id = ?", keyID, orgID, ownerUserID).
			First(&row).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return apiKeyNotFound()
		}
		if err != nil {
			return err
		}
		lookupHash = row.LookupHash
		return r.DB(ctx).Where("id = ?", row.ID).Delete(&APIKey{}).Error
	})
	if err != nil {
		return "", err
	}
	return lookupHash, nil
}

// UpdateByIDAndOrganizationOwned edits a key's name, expiry and rate
// limits only when the key belongs to the organization and the owner.
func (r *APIKeyRepository) UpdateByIDAndOrganizationOwned(ctx context.Context, orgID, keyID, ownerUserID, name string, expiresAt *time.Time, rpm, tpm int64) (*APIKey, error) {
	var row APIKey
	err := r.db.WithinTx(ctx, func(ctx context.Context) error {
		err := r.DB(ctx).
			Where("id = ? AND organization_id = ? AND owner_user_id = ?", keyID, orgID, ownerUserID).
			First(&row).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		fields := map[string]any{
			"name":           name,
			"rate_limit_rpm": rpm,
			"rate_limit_tpm": tpm,
		}
		if expiresAt != nil {
			fields["expires_at"] = *expiresAt
		} else {
			fields["expires_at"] = nil
		}
		if err := r.UpdateFields(ctx, row.ID, fields); err != nil {
			return err
		}
		return r.DB(ctx).Where("id = ?", row.ID).First(&row).Error
	})
	if err != nil {
		return nil, err
	}
	if row.ID == "" {
		return nil, nil
	}
	row.Name = name
	row.RateLimitRPM = rpm
	row.RateLimitTPM = tpm
	row.ExpiresAt = expiresAt
	return &row, nil
}

// UpdateScopeByIDAndOrganizationOwned replaces a key's model allow-list
// only when the key belongs to the organization and the owner. It
// returns the updated row and the previous list.
func (r *APIKeyRepository) UpdateScopeByIDAndOrganizationOwned(ctx context.Context, orgID, keyID, ownerUserID, modelsJSON string) (*APIKey, []string, error) {
	var row APIKey
	var before []string
	err := r.db.WithinTx(ctx, func(ctx context.Context) error {
		err := r.DB(ctx).
			Where("id = ? AND organization_id = ? AND owner_user_id = ?", keyID, orgID, ownerUserID).
			First(&row).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		before = decodeModelIDs(row.Models)
		if err := r.UpdateFields(ctx, row.ID, map[string]any{"models": modelsJSON}); err != nil {
			return err
		}
		return r.DB(ctx).Where("id = ?", row.ID).First(&row).Error
	})
	if err != nil {
		return nil, nil, err
	}
	if row.ID == "" {
		return nil, nil, nil
	}
	return &row, before, nil
}

// apiKeyNotFound builds the canonical not-found error shared by the
// owner-scoped operations (no cross-tenant, cross-owner existence leak).
func apiKeyNotFound() error {
	return apierrors.New(apierrors.CodeAPIKeyNotFound)
}
