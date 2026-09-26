package auth

import (
	"context"
	"errors"
	"time"

	"gorm.io/gorm"

	"github.com/go-taas/go-taas/pkg/database"
	apierrors "github.com/go-taas/go-taas/pkg/errors"
)

// APIKeyRepository persists API keys on top of the generic repository
// base. All reads and writes join an open transaction via the context.
type APIKeyRepository struct {
	*database.BaseRepository[APIKey]
	db *database.Manager
}

// NewAPIKeyRepository constructs an APIKeyRepository bound to a database
// Manager.
func NewAPIKeyRepository(db *gorm.DB) *APIKeyRepository {
	mgr := database.NewManager(db)
	return &APIKeyRepository{
		BaseRepository: database.NewBaseRepository[APIKey](mgr),
		db:             mgr,
	}
}

// ListByOrganization returns one page of the organization's keys ordered
// by creation time (newest first, id tie-break for stable pagination)
// and the total count matching the (unpaginated) conditions. When
// activeOnly is set, revoked and expired keys are excluded server-side.
func (r *APIKeyRepository) ListByOrganization(ctx context.Context, orgID string, offset, limit int, activeOnly bool, now time.Time) ([]*APIKey, int64, error) {
	conds := []any{"organization_id = ?", orgID}
	if activeOnly {
		conds = []any{
			"organization_id = ? AND revoked = false AND (expires_at IS NULL OR expires_at > ?)",
			orgID, now,
		}
	}
	return r.Paginate(ctx, offset, limit, conds, "created_at DESC", "id DESC")
}

// FindByLookupHash returns the key row with the given lookup hash
// (the verification lookup). Unknown digests map to CodeAPIKeyNotFound.
func (r *APIKeyRepository) FindByLookupHash(ctx context.Context, digest string) (*APIKey, error) {
	var row APIKey
	err := r.DB(ctx).Where("lookup_hash = ?", digest).First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, apierrors.New(apierrors.CodeAPIKeyNotFound)
	}
	if err != nil {
		return nil, err
	}
	return &row, nil
}

// RevokeByIDAndOrganization soft-revokes the key owned by orgID inside a
// transaction. A missing key or a key of another organization maps to
// CodeAPIKeyNotFound (no cross-org existence leak). The update is
// idempotent: re-revoking succeeds and preserves the first revoked_at.
// It returns the row's lookup hash for cache invalidation.
func (r *APIKeyRepository) RevokeByIDAndOrganization(ctx context.Context, orgID, keyID string, now time.Time) (string, error) {
	var lookupHash string
	err := r.db.WithinTx(ctx, func(ctx context.Context) error {
		var row APIKey
		err := r.DB(ctx).
			Where("id = ? AND organization_id = ?", keyID, orgID).
			First(&row).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return apierrors.New(apierrors.CodeAPIKeyNotFound)
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

// UpdateByIDAndOrganization edits a key's name, expiry and rate limits,
// scoped to the owning organization (feature #11, AD4). A missing key or
// a key of another organization returns nil (the caller maps it to
// CodeAPIKeyNotFound — no cross-org existence leak). It never touches
// the secret columns.
func (r *APIKeyRepository) UpdateByIDAndOrganization(ctx context.Context, orgID, keyID, name string, expiresAt *time.Time, rpm, tpm int64) (*APIKey, error) {
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
			"name":           name,
			"rate_limit_rpm": rpm,
			"rate_limit_tpm": tpm,
		}
		if expiresAt != nil {
			fields["expires_at"] = *expiresAt
		} else {
			fields["expires_at"] = nil
		}
		return r.UpdateFields(ctx, row.ID, fields)
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

// FindSystemCredential returns the synthetic platform credential used by
// the load-test runner (feature #20, AD11). A miss maps to
// CodeAPIKeyNotFound: the row is seeded at migration, so its absence is
// an infrastructure invariant violation.
func (r *APIKeyRepository) FindSystemCredential(ctx context.Context) (*APIKey, error) {
	var row APIKey
	err := r.DB(ctx).Where("is_system = ?", true).First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, apierrors.New(apierrors.CodeAPIKeyNotFound)
	}
	if err != nil {
		return nil, err
	}
	return &row, nil
}

// UpsertSystemCredential inserts or refreshes the single synthetic
// platform credential row (feature #20, AD11). It is idempotent: exactly
// one is_system row is kept, and the lookup/salted hashes are refreshed
// to match the caller-supplied key material.
func (r *APIKeyRepository) UpsertSystemCredential(ctx context.Context, row *APIKey) error {
	row.IsSystem = true
	return r.db.WithinTx(ctx, func(ctx context.Context) error {
		var existing APIKey
		err := r.DB(ctx).Where("is_system = ?", true).First(&existing).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return r.Create(ctx, row)
		}
		if err != nil {
			return err
		}
		row.ID = existing.ID
		return r.UpdateFields(ctx, existing.ID, map[string]any{
			"prefix":          row.Prefix,
			"lookup_hash":     row.LookupHash,
			"salt":            row.Salt,
			"salted_hash":     row.SaltedHash,
			"organization_id": row.OrganizationID,
			"revoked":         false,
			"is_system":       true,
		})
	})
}
