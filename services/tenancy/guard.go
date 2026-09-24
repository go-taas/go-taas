package tenancy

import (
	"context"
	"errors"

	"gorm.io/gorm"

	apierrors "github.com/go-taas/go-taas/pkg/errors"
)

// OrgGuard validates the transitional organization context of
// org-scoped APIs (feature #6). It is a read-only interface over the
// organizations table, injected into consuming services (auth, infer,
// metering, billing) at wiring time — the SetDeleteModelGuard pattern.
// One indexed primary-key read per request; no cache in v1.
type OrgGuard struct {
	repo *Repository
}

// NewOrgGuard constructs an OrgGuard bound to a GORM database.
func NewOrgGuard(db *gorm.DB) *OrgGuard {
	return &OrgGuard{repo: NewRepository(db)}
}

// RequireExists returns CodeOrganizationNotFound when the organization
// does not exist. Used by read paths: history stays visible for
// organizations of any state.
func (g *OrgGuard) RequireExists(ctx context.Context, orgID string) error {
	_, err := g.repo.FindOrganization(ctx, orgID)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return apierrors.New(apierrors.CodeOrganizationNotFound)
	}
	return err
}

// RequireActive returns CodeOrganizationNotFound when the organization
// is unknown and CodeOrganizationDisabled when it is disabled. Used by
// the gated write paths (CreateAPIKey, CreateInferenceService).
func (g *OrgGuard) RequireActive(ctx context.Context, orgID string) error {
	org, err := g.repo.FindOrganization(ctx, orgID)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return apierrors.New(apierrors.CodeOrganizationNotFound)
	}
	if err != nil {
		return err
	}
	if org.State != StateActive {
		return apierrors.New(apierrors.CodeOrganizationDisabled)
	}
	return nil
}
