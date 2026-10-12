package model

import (
	"context"
	"time"

	"github.com/google/uuid"

	apierrors "github.com/go-taas/go-taas/pkg/errors"
)

// ExternalModel is the model module's external-model projection used by the
// provider module's publish and resolve seam (feature #48). It carries no
// credential, base URL or key material.
type ExternalModel struct {
	// ID is the catalog model id.
	ID string
	// Name is the catalog model name (the tenant contract).
	Name string
	// External is always true for this projection.
	External bool
	// Published is the published state.
	Published bool
	// ProviderID is the owning provider's id (admin-only).
	ProviderID string
	// UpstreamModelID is the provider's model id (admin-only).
	UpstreamModelID string
}

// CreateExternalModel creates a catalog model with external=true (or
// re-enables a same-named unpublished one). A live name conflict returns
// 13014 (design FR5.2/FR5.3). No model_versions row is created (AD13).
func (s *Service) CreateExternalModel(ctx context.Context, name, providerID, upstreamModelID string) (*ExternalModel, error) {
	repo, err := s.repository()
	if err != nil {
		return nil, err
	}
	existing, err := repo.FindByName(ctx, name)
	if err == nil {
		// The name exists: a live (published) model is a conflict;
		// an unpublished one is re-enabled.
		if existing.Published {
			return nil, apierrors.New(apierrors.CodeProviderPublishConflict)
		}
		if err := repo.SetExternalModelFields(ctx, existing.ID, providerID, upstreamModelID, true); err != nil {
			return nil, err
		}
		return &ExternalModel{
			ID:              existing.ID,
			Name:            existing.Name,
			External:        true,
			Published:       true,
			ProviderID:      providerID,
			UpstreamModelID: upstreamModelID,
		}, nil
	}
	if apierrors.CodeOf(err) != apierrors.CodeModelNotFound {
		return nil, err
	}
	now := time.Now().UTC()
	row := &Model{
		ID:              uuid.NewString(),
		Name:            name,
		External:        true,
		ProviderID:      providerID,
		UpstreamModelID: upstreamModelID,
		Published:       true,
		CreatedAt:       now,
		UpdatedAt:       now,
	}
	if err := repo.CreateModel(ctx, row); err != nil {
		if apierrors.CodeOf(err) == apierrors.CodeModelExists {
			return nil, apierrors.New(apierrors.CodeProviderPublishConflict)
		}
		return nil, err
	}
	return &ExternalModel{
		ID:              row.ID,
		Name:            row.Name,
		External:        true,
		Published:       true,
		ProviderID:      providerID,
		UpstreamModelID: upstreamModelID,
	}, nil
}

// SetExternalModelPublished toggles an external catalog model's published
// state. An unknown or non-external model returns 13015 (fail closed).
func (s *Service) SetExternalModelPublished(ctx context.Context, modelID string, published bool) error {
	repo, err := s.repository()
	if err != nil {
		return err
	}
	m, err := repo.GetModel(ctx, modelID)
	if err != nil || !m.External {
		return apierrors.New(apierrors.CodeProviderNotPublished)
	}
	return repo.SetModelPublished(ctx, modelID, published)
}

// LookupExternalModel returns the external catalog model by id (preferred)
// or name. A miss or a non-external model returns 13015 (fail closed).
func (s *Service) LookupExternalModel(ctx context.Context, modelID, modelName string) (*ExternalModel, error) {
	repo, err := s.repository()
	if err != nil {
		return nil, err
	}
	var m *Model
	if modelID != "" {
		m, err = repo.GetModel(ctx, modelID)
	} else if modelName != "" {
		m, err = repo.FindByName(ctx, modelName)
	} else {
		return nil, apierrors.New(apierrors.CodeProviderNotPublished)
	}
	if err != nil || !m.External {
		return nil, apierrors.New(apierrors.CodeProviderNotPublished)
	}
	return &ExternalModel{
		ID:              m.ID,
		Name:            m.Name,
		External:        true,
		Published:       m.Published,
		ProviderID:      m.ProviderID,
		UpstreamModelID: m.UpstreamModelID,
	}, nil
}

// SetExternalModelFields sets the external binding columns on a model.
func (r *Repository) SetExternalModelFields(ctx context.Context, id, providerID, upstreamModelID string, published bool) error {
	return r.DB(ctx).Model(&Model{}).Where("id = ?", id).
		Updates(map[string]any{
			"external":          true,
			"provider_id":       providerID,
			"upstream_model_id": upstreamModelID,
			"published":         published,
			"updated_at":        time.Now().UTC(),
		}).Error
}

// SetModelPublished toggles the published column on a model.
func (r *Repository) SetModelPublished(ctx context.Context, id string, published bool) error {
	return r.DB(ctx).Model(&Model{}).Where("id = ?", id).
		Updates(map[string]any{
			"published":  published,
			"updated_at": time.Now().UTC(),
		}).Error
}

// providerName resolves an external model's provider name best-effort; an
// unwired resolver or an error yields "".
func (s *Service) providerName(ctx context.Context, m *Model) string {
	if m == nil || !m.External || m.ProviderID == "" || s.providerNameResolver == nil {
		return ""
	}
	name, err := s.providerNameResolver.ProviderName(ctx, m.ProviderID)
	if err != nil {
		return ""
	}
	return name
}
