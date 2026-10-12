package model

import (
	"context"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm/clause"
)

// GrantAccess adds an organization to the model's grant list, recording
// the granting user (AD8) and the grant time. It is idempotent: the
// composite unique key (model_id, organization_id) makes a repeated
// grant a no-op write rather than a second row (AC2).
func (r *Repository) GrantAccess(ctx context.Context, modelID, organizationID, grantedBy string) error {
	row := &Authorization{
		ID:             uuid.NewString(),
		ModelID:        modelID,
		OrganizationID: organizationID,
		GrantedBy:      grantedBy,
		CreatedAt:      time.Now().UTC(),
	}
	return r.authorizations.DB(ctx).
		Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "model_id"}, {Name: "organization_id"}},
			DoNothing: true,
		}).
		Create(row).Error
}

// RevokeAccess removes the organization from the model's grant list. It
// is idempotent: revoking an organization that holds no grant deletes
// zero rows and reports success (AC4). Revoking the last grant returns
// the model to default-allow (AC3).
func (r *Repository) RevokeAccess(ctx context.Context, modelID, organizationID string) error {
	return r.authorizations.DB(ctx).
		Where("model_id = ? AND organization_id = ?", modelID, organizationID).
		Delete(&Authorization{}).Error
}

// ListAuthorizations returns one page of the model's grant rows, newest
// first, with the total count (AC10).
func (r *Repository) ListAuthorizations(ctx context.Context, modelID string, offset, limit int) ([]*Authorization, int64, error) {
	return r.authorizations.Paginate(ctx, offset, limit,
		[]any{"model_id = ?", modelID}, "created_at DESC", "id DESC")
}

// CountAuthorizations returns the number of grant rows of the model: 0
// means the model is still open to every organization (AD2), > 0 means it
// is restricted to the granted set (AD1).
func (r *Repository) CountAuthorizations(ctx context.Context, modelID string) (int64, error) {
	return r.authorizations.Count(ctx, "model_id = ?", modelID)
}

// HasAuthorization reports whether the organization holds a grant on the
// model, ignoring the default-allow rule.
func (r *Repository) HasAuthorization(ctx context.Context, modelID, organizationID string) (bool, error) {
	count, err := r.authorizations.Count(ctx, "model_id = ? AND organization_id = ?", modelID, organizationID)
	if err != nil {
		return false, err
	}
	return count > 0, nil
}

// IsModelAuthorized implements the shared default-allow check (AD5) used
// by both enforcement points: a model with zero grants is authorized for
// every organization, a restricted model only for the organizations in
// its grant list. One implementation, so the control plane and the data
// plane cannot drift.
func (r *Repository) IsModelAuthorized(ctx context.Context, modelID, organizationID string) (bool, error) {
	count, err := r.CountAuthorizations(ctx, modelID)
	if err != nil {
		return false, err
	}
	if count == 0 {
		// Default-allow: the model has never been restricted (AD2).
		return true, nil
	}
	return r.HasAuthorization(ctx, modelID, organizationID)
}

// RestrictedModelIDs returns the subset of modelIDs carrying at least one
// grant row, so a page of models can be flagged without a query per row
// (AC13).
func (r *Repository) RestrictedModelIDs(ctx context.Context, modelIDs []string) (map[string]bool, error) {
	restricted := map[string]bool{}
	if len(modelIDs) == 0 {
		return restricted, nil
	}
	var ids []string
	if err := r.authorizations.DB(ctx).
		Model(&Authorization{}).
		Distinct("model_id").
		Where("model_id IN ?", modelIDs).
		Pluck("model_id", &ids).Error; err != nil {
		return nil, err
	}
	for _, id := range ids {
		restricted[id] = true
	}
	return restricted, nil
}

// ListModelsForOrganization returns one page of the models the
// organization may use, newest first, with the total count (AC11): every
// published model with zero grants, plus the restricted models the
// organization is granted. Feature #48: an unpublished external model is
// hidden from this user projection.
func (r *Repository) ListModelsForOrganization(ctx context.Context, organizationID string, offset, limit int) ([]*Model, int64, error) {
	const cond = `published = ? AND (NOT EXISTS (SELECT 1 FROM model_authorizations a WHERE a.model_id = models.id)` +
		` OR EXISTS (SELECT 1 FROM model_authorizations a WHERE a.model_id = models.id AND a.organization_id = ?))`
	return r.Paginate(ctx, offset, limit, []any{cond, true, organizationID}, "created_at DESC", "id DESC")
}
