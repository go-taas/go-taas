package image

import (
	"context"
	"strings"

	commonv1 "github.com/go-taas/go-taas/proto/taas/common/v1"
	imagev1 "github.com/go-taas/go-taas/proto/taas/image/v1"

	apierrors "github.com/go-taas/go-taas/pkg/errors"
	"github.com/go-taas/go-taas/services/model"
)

// CompatibilityChecker returns the compatibility status of a
// (model, engine, card_type) combination. It is the narrow cross-module
// read seam the infer module consumes for deploy-time enforcement
// (feature #19, AD13), mirroring the SetDeleteGuard injection pattern.
type CompatibilityChecker func(ctx context.Context, modelID, engine, cardType string) (string, error)

// GetCompatibilityStatus returns the compatibility status of a
// (model, engine, card_type) combination, materializing a default row
// lazily if missing (AD3). It is the narrow interface injected into
// infer (AD13).
func (s *Service) GetCompatibilityStatus(ctx context.Context, modelID, engine, cardType string) (string, error) {
	repo, err := s.compatibilityRepository()
	if err != nil {
		return "", err
	}
	cardTypes, err := s.cardTypes()
	if err != nil {
		return "", err
	}
	cell, err := repo.GetCell(ctx, modelID, engine, cardType, cardTypes, s.lazySeedDefault())
	if err != nil {
		return "", err
	}
	return cell.Status, nil
}

// NewCompatibilityChecker builds a CompatibilityChecker over the image
// service. It is the constructor the composing layer (apps/taas-server)
// uses to inject the deploy-time enforcement into infer.
func NewCompatibilityChecker(s *Service) CompatibilityChecker {
	return s.GetCompatibilityStatus
}

// ModelCompatibilitySummary returns a compact string of the model's
// supported/experimental engine/card-type combos (e.g. "vLLM · A800,
// H800"), or "" when the model has none (feature #19, AD12). It is the
// narrow read the model module consumes for the masked model-list
// summary.
func (s *Service) ModelCompatibilitySummary(ctx context.Context, modelID string) (string, error) {
	repo, err := s.compatibilityRepository()
	if err != nil {
		return "", err
	}
	cells, _, _, err := repo.GetModelCompatibility(ctx, modelID)
	if err != nil {
		return "", err
	}
	if len(cells) == 0 {
		return "", nil
	}
	// Group card types by engine, preserving engine order.
	engineOrder := []string{}
	byEngine := map[string][]string{}
	for _, c := range cells {
		if _, ok := byEngine[c.Engine]; !ok {
			engineOrder = append(engineOrder, c.Engine)
		}
		byEngine[c.Engine] = append(byEngine[c.Engine], c.CardType)
	}
	parts := make([]string, 0, len(engineOrder))
	for _, e := range engineOrder {
		parts = append(parts, e+" · "+strings.Join(byEngine[e], ", "))
	}
	return strings.Join(parts, "; "), nil
}

// NewModelCompatibilitySummaryProvider builds a CompatibilityProvider
// over the image service. It is the constructor the composing layer
// (apps/taas-server) uses to inject the masked model-list summary into
// the model module.
func NewModelCompatibilitySummaryProvider(s *Service) model.CompatibilityProvider {
	return model.CompatibilityProviderFunc(s.ModelCompatibilitySummary)
}

// cardTypes returns the live card-type set from the injected provider,
// or an empty set when the provider is not wired.
func (s *Service) cardTypes() ([]CardType, error) {
	if s.cardTypesProvider == nil {
		return []CardType{}, nil
	}
	return s.cardTypesProvider.ListCardTypes(context.Background())
}

// lazySeedDefault returns the configured lazy-seed default status.
func (s *Service) lazySeedDefault() string {
	if s.compatLazyDefault != "" {
		return s.compatLazyDefault
	}
	return StatusExperimental
}

// ListCompatibilityMatrix returns matrix cells with model/engine/card/
// status filters, model-name search, and pagination (AC2).
func (s *Service) ListCompatibilityMatrix(ctx context.Context, req *imagev1.ListCompatibilityMatrixRequest) (*imagev1.ListCompatibilityMatrixResponse, error) {
	repo, err := s.compatibilityRepository()
	if err != nil {
		return nil, err
	}
	cardTypes, err := s.cardTypes()
	if err != nil {
		return nil, err
	}
	offset, limit := normalizeCompatibilityPage(req.GetPage())
	status := strings.TrimSpace(req.GetStatus())
	if status != "" && !IsValidCompatibilityStatus(status) {
		status = ""
	}
	cells, total, err := repo.ListCells(ctx,
		strings.TrimSpace(req.GetModelId()),
		strings.TrimSpace(req.GetEngine()),
		strings.TrimSpace(req.GetCardType()),
		status,
		req.GetSearch(),
		offset, limit, cardTypes, s.lazySeedDefault())
	if err != nil {
		return nil, err
	}
	modelName := map[string]string{}
	models, err := repo.listModels(ctx)
	if err != nil {
		return nil, err
	}
	for _, m := range models {
		modelName[m.ID] = m.Name
	}
	cardVendor := map[string]string{}
	for _, ct := range cardTypes {
		cardVendor[ct.CardType] = ct.Vendor
	}
	inFleet := map[string]bool{}
	for _, ct := range cardTypes {
		inFleet[ct.CardType] = true
	}
	out := make([]*imagev1.CompatibilityCell, 0, len(cells))
	for _, c := range cells {
		out = append(out, &imagev1.CompatibilityCell{
			ModelId:    c.ModelID,
			ModelName:  modelName[c.ModelID],
			Engine:     c.Engine,
			CardType:   c.CardType,
			CardVendor: cardVendor[c.CardType],
			Status:     c.Status,
			Note:       c.Note,
			NotInFleet: !inFleet[c.CardType],
			UpdatedAt:  c.UpdatedAt.Unix(),
		})
	}
	return &imagev1.ListCompatibilityMatrixResponse{
		Response: okImageResponse(),
		Cells:    out,
		PageMeta: &commonv1.PageMeta{Total: total, Offset: int64(offset), Limit: clampToInt32(limit)},
	}, nil
}

// ListCompatibilityDimensions returns the three axis lists and the
// per-status counts (AC3).
func (s *Service) ListCompatibilityDimensions(ctx context.Context, _ *imagev1.ListCompatibilityDimensionsRequest) (*imagev1.ListCompatibilityDimensionsResponse, error) {
	repo, err := s.compatibilityRepository()
	if err != nil {
		return nil, err
	}
	cardTypes, err := s.cardTypes()
	if err != nil {
		return nil, err
	}
	models, engines, cardTypes, counts, err := repo.ListDimensions(ctx, cardTypes, s.lazySeedDefault())
	if err != nil {
		return nil, err
	}
	modelOut := make([]*imagev1.CompatibilityDimensionModel, 0, len(models))
	for _, m := range models {
		modelOut = append(modelOut, &imagev1.CompatibilityDimensionModel{ModelId: m.ID, ModelName: m.Name})
	}
	engineOut := make([]*imagev1.CompatibilityDimensionEngine, 0, len(engines))
	for _, e := range engines {
		engineOut = append(engineOut, &imagev1.CompatibilityDimensionEngine{Engine: e.Engine, Accelerator: e.Accelerator})
	}
	cardOut := make([]*imagev1.CompatibilityDimensionCardType, 0, len(cardTypes))
	inFleet := map[string]bool{}
	for _, ct := range cardTypes {
		inFleet[ct.CardType] = true
	}
	for _, ct := range cardTypes {
		cardOut = append(cardOut, &imagev1.CompatibilityDimensionCardType{
			CardType: ct.CardType,
			Vendor:   ct.Vendor,
			InFleet:  inFleet[ct.CardType],
		})
	}
	return &imagev1.ListCompatibilityDimensionsResponse{
		Response:  okImageResponse(),
		Models:    modelOut,
		Engines:   engineOut,
		CardTypes: cardOut,
		StatusCounts: &imagev1.CompatibilityStatusCounts{
			Supported:    counts[StatusSupported],
			Experimental: counts[StatusExperimental],
			Unsupported:  counts[StatusUnsupported],
			NotInFleet:   counts["not_in_fleet"],
		},
	}, nil
}

// GetCompatibilityCell returns one cell; a missing cell returns 10209
// (AC4).
func (s *Service) GetCompatibilityCell(ctx context.Context, req *imagev1.GetCompatibilityCellRequest) (*imagev1.GetCompatibilityCellResponse, error) {
	repo, err := s.compatibilityRepository()
	if err != nil {
		return nil, err
	}
	cardTypes, err := s.cardTypes()
	if err != nil {
		return nil, err
	}
	cell, err := repo.GetCell(ctx, req.GetModelId(), req.GetEngine(), req.GetCardType(), cardTypes, s.lazySeedDefault())
	if err != nil {
		return nil, err
	}
	return &imagev1.GetCompatibilityCellResponse{
		Response: okImageResponse(),
		Cell:     cellToProto(cell, cardTypes),
	}, nil
}

// SetCompatibilityStatus sets a cell's status and optional note (AC4).
func (s *Service) SetCompatibilityStatus(ctx context.Context, req *imagev1.SetCompatibilityStatusRequest) (*imagev1.SetCompatibilityStatusResponse, error) {
	repo, err := s.compatibilityRepository()
	if err != nil {
		return nil, err
	}
	cardTypes, err := s.cardTypes()
	if err != nil {
		return nil, err
	}
	cell, err := repo.SetStatus(ctx, req.GetModelId(), req.GetEngine(), req.GetCardType(), req.GetStatus(), req.GetNote(), cardTypes, s.lazySeedDefault())
	if err != nil {
		return nil, err
	}
	return &imagev1.SetCompatibilityStatusResponse{
		Response: okImageResponse(),
		Cell:     cellToProto(cell, cardTypes),
	}, nil
}

// BulkSetCompatibilityStatus updates a list of cells atomically and
// returns the count (AC5).
func (s *Service) BulkSetCompatibilityStatus(ctx context.Context, req *imagev1.BulkSetCompatibilityStatusRequest) (*imagev1.BulkSetCompatibilityStatusResponse, error) {
	repo, err := s.compatibilityRepository()
	if err != nil {
		return nil, err
	}
	cardTypes, err := s.cardTypes()
	if err != nil {
		return nil, err
	}
	refs := make([]CellRef, 0, len(req.GetCells()))
	for _, c := range req.GetCells() {
		refs = append(refs, CellRef{ModelID: c.GetModelId(), Engine: c.GetEngine(), CardType: c.GetCardType()})
	}
	updated, err := repo.BulkSetStatus(ctx, refs, req.GetStatus(), req.GetNote(), cardTypes, s.lazySeedDefault())
	if err != nil {
		return nil, err
	}
	return &imagev1.BulkSetCompatibilityStatusResponse{
		Response: okImageResponse(),
		Updated:  updated,
	}, nil
}

// GetModelCompatibility returns the masked user-realm projection: the
// model's supported/experimental engines and card types (AC7). An
// unknown model returns 10101; a model the tenant is not authorized for
// returns 10105.
func (s *Service) GetModelCompatibility(ctx context.Context, req *imagev1.GetModelCompatibilityRequest) (*imagev1.GetModelCompatibilityResponse, error) {
	modelID := strings.TrimSpace(req.GetModelId())
	if modelID == "" {
		return nil, apierrors.New(apierrors.CodeModelNotFound)
	}
	// The model must exist.
	modelRepo, err := s.modelRepository()
	if err != nil {
		return nil, err
	}
	if _, err := modelRepo.GetModel(ctx, modelID); err != nil {
		return nil, err
	}
	// Resolve the caller's organization and apply the model-authorization
	// default-allow rule (feature #13): a restricted model the tenant is
	// not granted returns 10105.
	orgID, err := s.resolveOrg(ctx)
	if err != nil {
		return nil, err
	}
	authorized, err := modelRepo.IsModelAuthorized(ctx, modelID, orgID)
	if err != nil {
		return nil, err
	}
	if !authorized {
		return nil, apierrors.New(apierrors.CodeModelUnauthorized)
	}
	repo, err := s.compatibilityRepository()
	if err != nil {
		return nil, err
	}
	cells, supported, experimental, err := repo.GetModelCompatibility(ctx, modelID)
	if err != nil {
		return nil, err
	}
	cardTypes, err := s.cardTypes()
	if err != nil {
		return nil, err
	}
	cardVendor := map[string]string{}
	for _, ct := range cardTypes {
		cardVendor[ct.CardType] = ct.Vendor
	}
	entries := make([]*imagev1.ModelCompatibilityEntry, 0, len(cells))
	for _, c := range cells {
		entries = append(entries, &imagev1.ModelCompatibilityEntry{
			Engine:     c.Engine,
			CardType:   c.CardType,
			CardVendor: cardVendor[c.CardType],
			Status:     c.Status,
		})
	}
	return &imagev1.GetModelCompatibilityResponse{
		Response:          okImageResponse(),
		Entries:           entries,
		SupportedCount:    supported,
		ExperimentalCount: experimental,
	}, nil
}

// cellToProto maps a stored cell to its wire form, deriving card_vendor
// and not_in_fleet from the live card-type set.
func cellToProto(c *CompatibilityCell, cardTypes []CardType) *imagev1.CompatibilityCell {
	cardVendor := ""
	inFleet := false
	for _, ct := range cardTypes {
		if ct.CardType == c.CardType {
			cardVendor = ct.Vendor
			inFleet = true
			break
		}
	}
	return &imagev1.CompatibilityCell{
		ModelId:    c.ModelID,
		Engine:     c.Engine,
		CardType:   c.CardType,
		CardVendor: cardVendor,
		Status:     c.Status,
		Note:       c.Note,
		NotInFleet: !inFleet,
		UpdatedAt:  c.UpdatedAt.Unix(),
	}
}

// clampToInt32 bounds v to the int32 range proto fields accept.
func clampToInt32(v int) int32 {
	if v > int(^uint32(0)>>1) {
		return int32(^uint32(0) >> 1)
	}
	if v < -int(^uint32(0)>>1)-1 {
		return -int32(^uint32(0)>>1) - 1
	}
	return int32(v)
}

// normalizeCompatibilityPage applies the pagination defaults: limit 20,
// cap 100, negative offset clamped to 0.
func normalizeCompatibilityPage(page *commonv1.PageRequest) (offset, limit int) {
	offset = int(page.GetOffset())
	limit = int(page.GetLimit())
	if offset < 0 {
		offset = 0
	}
	if limit <= 0 {
		limit = 20
	}
	if limit > 100 {
		limit = 100
	}
	return offset, limit
}

// okImageResponse builds the success envelope.
func okImageResponse() *commonv1.Response {
	return &commonv1.Response{Code: 0, Message: "OK"}
}
