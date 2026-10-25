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
// organization.
//
// Resolution order:
//  1. infer.endpointBaseURL (an operator's explicit public URL);
//  2. the serving gateway the resolver discovers in the cluster, so a
//     fresh install works without hand-editing configuration;
//  3. a 10304 whose message names the setting to configure — the
//     quickstart page surfaces it verbatim, and a bare "internal error"
//     would leave the operator with nothing to act on.
func (s *Service) GetInferenceEndpoint(ctx context.Context, _ *inferv1.GetInferenceEndpointRequest) (*inferv1.GetInferenceEndpointResponse, error) {
	baseURL := ""
	if cfg := config.GetConfig(); cfg != nil {
		baseURL = strings.TrimSpace(cfg.Infer.EndpointBaseURL)
	}
	if baseURL == "" && s.gatewayResolver != nil {
		baseURL = strings.TrimSpace(s.gatewayResolver.InferenceGatewayURL(ctx))
	}
	if baseURL == "" {
		return nil, apierrors.Newf(apierrors.CodeInferEndpointNotFound,
			"infer: no inference gateway base URL is available; set infer.endpointBaseURL to the serving gateway's public URL")
	}
	return &inferv1.GetInferenceEndpointResponse{
		Response: okResponse(),
		BaseUrl:  strings.TrimSuffix(baseURL, "/") + "/v1",
	}, nil
}
