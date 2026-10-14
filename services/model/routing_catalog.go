package model

import (
	"context"

	apierrors "github.com/go-taas/go-taas/pkg/errors"
	"github.com/go-taas/go-taas/services/routing"
)

// GetCatalogModel returns the catalog model by id as the routing
// module's read-only projection (feature #49, the ModelCatalog seam
// implemented by the model module). A miss returns 10101.
func (s *Service) GetCatalogModel(ctx context.Context, modelID string) (*routing.CatalogModel, error) {
	repo, err := s.repository()
	if err != nil {
		return nil, err
	}
	m, err := repo.GetModel(ctx, modelID)
	if err != nil {
		return nil, err
	}
	return &routing.CatalogModel{
		ID:        m.ID,
		Name:      m.Name,
		External:  m.External,
		Published: m.Published,
	}, nil
}

// CatalogModelNameExists reports whether a catalog model with the given
// name exists (feature #49, the reserved-name check 13113).
func (s *Service) CatalogModelNameExists(ctx context.Context, name string) (bool, error) {
	repo, err := s.repository()
	if err != nil {
		return false, err
	}
	existing, err := repo.FindByName(ctx, name)
	if err != nil {
		if apierrors.CodeOf(err) == apierrors.CodeModelNotFound {
			return false, nil
		}
		return false, err
	}
	return existing != nil, nil
}

// IsModelAuthorized implements the #13 default-allow org-grant check for
// the routing module's eligibility filter (feature #49).
func (s *Service) IsModelAuthorized(ctx context.Context, modelID, organizationID string) (bool, error) {
	repo, err := s.repository()
	if err != nil {
		return false, err
	}
	return repo.IsModelAuthorized(ctx, modelID, organizationID)
}
