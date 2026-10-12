package provider

import (
	"context"
	"strings"
	"time"

	"github.com/google/uuid"

	commonv1 "github.com/go-taas/go-taas/proto/taas/common/v1"
	providerv1 "github.com/go-taas/go-taas/proto/taas/provider/v1"

	apierrors "github.com/go-taas/go-taas/pkg/errors"
)

// ListProviderModels returns the curated upstream models of a provider
// (AC5).
func (s *Service) ListProviderModels(ctx context.Context, req *providerv1.ListProviderModelsRequest) (*providerv1.ListProviderModelsResponse, error) {
	if err := s.requireAdminRole(ctx); err != nil {
		return nil, err
	}
	providerRepo, err := s.providers()
	if err != nil {
		return nil, err
	}
	if _, err := providerRepo.GetByID(ctx, req.GetProviderId()); err != nil {
		return nil, err
	}
	modelRepo, err := s.models()
	if err != nil {
		return nil, err
	}
	offset, limit := normalizePage(req.GetPage())
	rows, total, err := modelRepo.List(ctx, req.GetProviderId(), ProviderModelFilter{
		UpstreamModelID: req.GetUpstreamModelId(),
		Enabled:         req.GetEnabled(),
		Published:       req.GetPublished(),
		Offset:          offset,
		Limit:           limit,
	})
	if err != nil {
		return nil, err
	}
	out := make([]*providerv1.ProviderModel, 0, len(rows))
	for _, row := range rows {
		out = append(out, s.providerModelToProto(ctx, row))
	}
	return &providerv1.ListProviderModelsResponse{
		Response: okResponse(),
		Models:   out,
		PageMeta: &commonv1.PageMeta{
			Total:  total,
			Offset: int64(offset),
			Limit:  int32(limit), //nolint:gosec // limit is capped at 100 by normalizePage.
		},
	}, nil
}

// AddProviderModel adds a curated upstream model row (AC5).
func (s *Service) AddProviderModel(ctx context.Context, req *providerv1.AddProviderModelRequest) (*providerv1.AddProviderModelResponse, error) {
	if err := s.requireAdminRole(ctx); err != nil {
		return nil, err
	}
	providerRepo, err := s.providers()
	if err != nil {
		return nil, err
	}
	if _, err := providerRepo.GetByID(ctx, req.GetProviderId()); err != nil {
		return nil, err
	}
	upstreamID := strings.TrimSpace(req.GetUpstreamModelId())
	if err := validateUpstreamModelID(upstreamID); err != nil {
		return nil, err
	}
	displayName := strings.TrimSpace(req.GetDisplayName())
	if len(displayName) > maxDisplayNameLen {
		return nil, apierrors.New(apierrors.CodeProviderModelInvalid)
	}
	if err := validateContextWindow(req.GetContextWindow()); err != nil {
		return nil, err
	}
	modelRepo, err := s.models()
	if err != nil {
		return nil, err
	}
	if existing, err := modelRepo.FindByUpstream(ctx, req.GetProviderId(), upstreamID); err != nil {
		return nil, err
	} else if existing != nil {
		return nil, apierrors.New(apierrors.CodeProviderModelExists)
	}
	now := time.Now().UTC()
	enabled := true
	if req.Enabled != nil {
		enabled = req.GetEnabled()
	}
	row := &ProviderModel{
		ID:              uuid.NewString(),
		ProviderID:      req.GetProviderId(),
		UpstreamModelID: upstreamID,
		DisplayName:     displayName,
		ContextWindow:   req.GetContextWindow(),
		Enabled:         enabled,
		CreatedAt:       now,
		UpdatedAt:       now,
	}
	if err := modelRepo.Create(ctx, row); err != nil {
		return nil, err
	}
	s.recordAudit(ctx, "provider.model_added", row.ID, "success")
	return &providerv1.AddProviderModelResponse{
		Response: okResponse(),
		Model:    s.providerModelToProto(ctx, row),
	}, nil
}

// UpdateProviderModel edits display name, context window and enabled
// (AC5). The upstream id is immutable.
func (s *Service) UpdateProviderModel(ctx context.Context, req *providerv1.UpdateProviderModelRequest) (*providerv1.UpdateProviderModelResponse, error) {
	if err := s.requireAdminRole(ctx); err != nil {
		return nil, err
	}
	modelRepo, err := s.models()
	if err != nil {
		return nil, err
	}
	row, err := modelRepo.GetByID(ctx, req.GetProviderId(), req.GetModelId())
	if err != nil {
		return nil, err
	}
	displayName := strings.TrimSpace(req.GetDisplayName())
	if len(displayName) > maxDisplayNameLen {
		return nil, apierrors.New(apierrors.CodeProviderModelInvalid)
	}
	if err := validateContextWindow(req.GetContextWindow()); err != nil {
		return nil, err
	}
	row.DisplayName = displayName
	row.ContextWindow = req.GetContextWindow()
	if req.Enabled != nil {
		row.Enabled = req.GetEnabled()
	}
	if err := modelRepo.Update(ctx, row); err != nil {
		return nil, err
	}
	s.recordAudit(ctx, "provider.model_updated", row.ID, "success")
	return &providerv1.UpdateProviderModelResponse{
		Response: okResponse(),
		Model:    s.providerModelToProto(ctx, row),
	}, nil
}

// RemoveProviderModel removes a curated row (AC5). A published row cannot
// be removed (13016).
func (s *Service) RemoveProviderModel(ctx context.Context, req *providerv1.RemoveProviderModelRequest) (*providerv1.RemoveProviderModelResponse, error) {
	if err := s.requireAdminRole(ctx); err != nil {
		return nil, err
	}
	modelRepo, err := s.models()
	if err != nil {
		return nil, err
	}
	row, err := modelRepo.GetByID(ctx, req.GetProviderId(), req.GetModelId())
	if err != nil {
		return nil, err
	}
	if row.Published {
		return nil, apierrors.New(apierrors.CodeProviderInUse)
	}
	if err := modelRepo.Delete(ctx, row.ID); err != nil {
		return nil, err
	}
	s.recordAudit(ctx, "provider.model_removed", row.ID, "success")
	return &providerv1.RemoveProviderModelResponse{Response: okResponse()}, nil
}

// PublishProviderModel publishes a curated, enabled row as a catalog model
// with external=true (AC6). A live name conflict returns 13014. Publishing
// is idempotent.
func (s *Service) PublishProviderModel(ctx context.Context, req *providerv1.PublishProviderModelRequest) (*providerv1.PublishProviderModelResponse, error) {
	if err := s.requireAdminRole(ctx); err != nil {
		return nil, err
	}
	if s.catalog == nil {
		return nil, apierrors.Newf(apierrors.CodeInternal, "provider: model catalog unavailable")
	}
	providerRepo, err := s.providers()
	if err != nil {
		return nil, err
	}
	provider, err := providerRepo.GetByID(ctx, req.GetProviderId())
	if err != nil {
		return nil, err
	}
	modelRepo, err := s.models()
	if err != nil {
		return nil, err
	}
	row, err := modelRepo.GetByID(ctx, req.GetProviderId(), req.GetModelId())
	if err != nil {
		return nil, err
	}
	if !row.Enabled {
		return nil, apierrors.New(apierrors.CodeProviderModelInvalid)
	}
	name := strings.TrimSpace(req.GetTenantFacingName())
	if name == "" {
		name = strings.TrimSpace(row.DisplayName)
	}
	if name == "" {
		name = row.UpstreamModelID
	}
	if !tenantFacingNamePattern.MatchString(name) || len(name) > maxTenantFacingNameLen {
		return nil, apierrors.New(apierrors.CodeProviderModelInvalid)
	}

	// Idempotent: a row already bound to a catalog model re-enables it.
	if row.CatalogModelID != "" {
		if err := s.catalog.SetExternalModelPublished(ctx, row.CatalogModelID, true); err != nil {
			return nil, err
		}
		row.Published = true
		if err := modelRepo.SetPublication(ctx, row.ID, row.CatalogModelID, true); err != nil {
			return nil, err
		}
		s.recordAudit(ctx, "provider.model_published", row.ID, "success")
		return &providerv1.PublishProviderModelResponse{
			Response:         okResponse(),
			CatalogModelId:   row.CatalogModelID,
			CatalogModelName: name,
			Model:            s.providerModelToProto(ctx, row),
		}, nil
	}

	catalogModel, err := s.catalog.CreateExternalModel(ctx, name, provider.ID, row.UpstreamModelID)
	if err != nil {
		return nil, err
	}
	row.Published = true
	row.CatalogModelID = catalogModel.ID
	if err := modelRepo.SetPublication(ctx, row.ID, catalogModel.ID, true); err != nil {
		return nil, err
	}
	s.recordAudit(ctx, "provider.model_published", row.ID, "success")
	return &providerv1.PublishProviderModelResponse{
		Response:         okResponse(),
		CatalogModelId:   catalogModel.ID,
		CatalogModelName: catalogModel.Name,
		Model:            s.providerModelToProto(ctx, row),
	}, nil
}

// UnpublishProviderModel marks the catalog model unpublished (AC7). Grants
// are retained but inert. Unpublishing is idempotent.
func (s *Service) UnpublishProviderModel(ctx context.Context, req *providerv1.UnpublishProviderModelRequest) (*providerv1.UnpublishProviderModelResponse, error) {
	if err := s.requireAdminRole(ctx); err != nil {
		return nil, err
	}
	modelRepo, err := s.models()
	if err != nil {
		return nil, err
	}
	row, err := modelRepo.GetByID(ctx, req.GetProviderId(), req.GetModelId())
	if err != nil {
		return nil, err
	}
	if row.CatalogModelID != "" {
		if s.catalog == nil {
			return nil, apierrors.Newf(apierrors.CodeInternal, "provider: model catalog unavailable")
		}
		if err := s.catalog.SetExternalModelPublished(ctx, row.CatalogModelID, false); err != nil {
			return nil, err
		}
	}
	row.Published = false
	if err := modelRepo.SetPublication(ctx, row.ID, row.CatalogModelID, false); err != nil {
		return nil, err
	}
	s.recordAudit(ctx, "provider.model_unpublished", row.ID, "success")
	return &providerv1.UnpublishProviderModelResponse{Response: okResponse()}, nil
}

// providerModelToProto maps a curated row, resolving the catalog model name
// best-effort through the injected catalog seam.
func (s *Service) providerModelToProto(ctx context.Context, row *ProviderModel) *providerv1.ProviderModel {
	out := &providerv1.ProviderModel{
		ModelId:         row.ID,
		UpstreamModelId: row.UpstreamModelID,
		DisplayName:     row.DisplayName,
		ContextWindow:   row.ContextWindow,
		Enabled:         row.Enabled,
		Published:       row.Published,
		CatalogModelId:  row.CatalogModelID,
		CreatedAt:       row.CreatedAt.Unix(),
		UpdatedAt:       row.UpdatedAt.Unix(),
	}
	if row.CatalogModelID != "" && s.catalog != nil {
		if cm, err := s.catalog.LookupExternalModel(ctx, row.CatalogModelID, ""); err == nil && cm != nil {
			out.CatalogModelName = cm.Name
		}
	}
	return out
}

// validateUpstreamModelID validates a curated upstream model id (13010).
func validateUpstreamModelID(id string) error {
	if id == "" || len(id) > maxUpstreamModelIDLen {
		return apierrors.New(apierrors.CodeProviderModelInvalid)
	}
	return nil
}

// validateContextWindow validates the context-window bounds (13010).
func validateContextWindow(v int64) error {
	if v < 0 || v > maxContextWindow {
		return apierrors.New(apierrors.CodeProviderModelInvalid)
	}
	return nil
}
