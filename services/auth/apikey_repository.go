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
