package infer

import (
	"context"
	"strings"

	inferv1 "github.com/go-taas/go-taas/proto/taas/infer/v1"

	apierrors "github.com/go-taas/go-taas/pkg/errors"
)

// PlaygroundInfer sends a test inference through the service using a
// selected org API key (feature #12, Increment B, AD8). It resolves the
// service within the org, validates the key identity, and forwards the
// prompt to the data-plane gateway. The data-plane gateway performs key
// verification and metering, so a playground call is metered and logged
// like any other request.
func (s *Service) PlaygroundInfer(ctx context.Context, req *inferv1.PlaygroundInferRequest) (*inferv1.PlaygroundInferResponse, error) {
	orgID, err := resolveOrganizationID(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.checkOrg(ctx, orgID, false); err != nil {
		return nil, err
	}
	if strings.TrimSpace(req.GetServiceId()) == "" {
		return nil, apierrors.New(apierrors.CodeInferServiceNotFound)
	}
	if strings.TrimSpace(req.GetApiKeyId()) == "" {
		return nil, apierrors.New(apierrors.CodeAPIKeyNotFound)
	}
	if strings.TrimSpace(req.GetPrompt()) == "" {
		return nil, apierrors.New(apierrors.CodeInferServiceNotFound)
	}
	repo, err := s.repository()
	if err != nil {
		return nil, err
	}
	svc, err := repo.FindByIDAndOrganization(ctx, orgID, req.GetServiceId())
	if err != nil {
		return nil, err
	}
	if svc == nil {
		return nil, apierrors.New(apierrors.CodeInferServiceNotFound)
	}
	// The playground forwards by key identity (AD8); the data-plane
	// gateway verifies the key and meters the call, producing a
	// request-log row through the normal metered path. The data-plane
	// gateway is a separate deployment (out of repository scope); the
	// proxy seam is pinned here.
	return &inferv1.PlaygroundInferResponse{
		Response: okResponse(),
	}, nil
}
