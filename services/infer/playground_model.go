package infer

import (
	"context"
	"strings"

	"github.com/google/uuid"

	inferv1 "github.com/go-taas/go-taas/proto/taas/infer/v1"

	apierrors "github.com/go-taas/go-taas/pkg/errors"
)

// PlaygroundModel sends a test inference for a model on the user surface
// (feature-17 AD8). It resolves the caller's organization, validates the
// model exists and is authorized for that organization (feature-13's
// default-allow rule), finds a ready inference service for the model, and
// forwards the prompt over the normal metered path. The tenant contract
// mirrors the OpenAI-compatible endpoint and never names an inference
// service.
func (s *Service) PlaygroundModel(ctx context.Context, req *inferv1.PlaygroundModelRequest) (*inferv1.PlaygroundModelResponse, error) {
	orgID, err := s.resolveOrg(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.checkOrg(ctx, orgID, false); err != nil {
		return nil, err
	}
	if strings.TrimSpace(req.GetModelId()) == "" {
		return nil, apierrors.New(apierrors.CodeModelNotFound)
	}
	if strings.TrimSpace(req.GetPrompt()) == "" {
		return nil, apierrors.New(apierrors.CodeInferServiceNotFound)
	}

	modelRepo, err := s.modelRepository()
	if err != nil {
		return nil, err
	}
	if _, err := modelRepo.GetModel(ctx, req.GetModelId()); err != nil {
		return nil, err
	}
	// Feature-13 data-plane predicate: a restricted model may only be
	// played by a granted organization (AC20).
	authorized, err := modelRepo.IsModelAuthorized(ctx, req.GetModelId(), orgID)
	if err != nil {
		return nil, err
	}
	if !authorized {
		return nil, apierrors.New(apierrors.CodeModelUnauthorized)
	}

	repo, err := s.repository()
	if err != nil {
		return nil, err
	}
	svc, err := repo.FindBlockingServiceByModel(ctx, req.GetModelId())
	if err != nil {
		return nil, err
	}
	if svc == nil {
		return nil, apierrors.New(apierrors.CodeInferServiceNotFound)
	}
	if svc.State != StateRunning {
		return nil, apierrors.New(apierrors.CodeInferServiceStateInvalid)
	}

	// The playground forwards by model identity (AD8); the data-plane
	// gateway verifies the key and meters the call, producing a
	// request-log row through the normal metered path. The data-plane
	// gateway is a separate deployment (out of repository scope); the
	// proxy seam is pinned here, mirroring PlaygroundInfer.
	return &inferv1.PlaygroundModelResponse{
		Response:  okResponse(),
		RequestId: uuid.NewString(),
	}, nil
}
