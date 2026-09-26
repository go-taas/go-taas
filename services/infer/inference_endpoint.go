package infer

import (
	"context"
	"strings"

	inferv1 "github.com/go-taas/go-taas/proto/taas/infer/v1"

	"github.com/go-taas/go-taas/pkg/config"
	apierrors "github.com/go-taas/go-taas/pkg/errors"
)

// GetInferenceEndpoint returns the tenant-facing inference base URL
// ("<inference-gateway>/v1") from server configuration (feature #21,
// AD9). It is never a client constant. The read requires no organization
// context: it returns the configured base URL regardless of the caller's
// organization. A missing/empty infer.endpointBaseURL returns the
// standard internal error, which the quickstart page maps to "Could not
// load the inference endpoint."
func (s *Service) GetInferenceEndpoint(_ context.Context, _ *inferv1.GetInferenceEndpointRequest) (*inferv1.GetInferenceEndpointResponse, error) {
	cfg := config.GetConfig()
	if cfg == nil || strings.TrimSpace(cfg.Infer.EndpointBaseURL) == "" {
		return nil, apierrors.New(apierrors.CodeInternal)
	}
	return &inferv1.GetInferenceEndpointResponse{
		Response: okResponse(),
		BaseUrl:  strings.TrimSuffix(cfg.Infer.EndpointBaseURL, "/") + "/v1",
	}, nil
}
