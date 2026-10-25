package infer

import (
	"context"
	"strings"

	inferv1 "github.com/go-taas/go-taas/proto/taas/infer/v1"

	"github.com/go-taas/go-taas/pkg/config"
	apierrors "github.com/go-taas/go-taas/pkg/errors"
)

// gatewaySettings is the resolved data-plane endpoint in two forms: the
// public base URL users call, and the URL the platform itself calls. They
// differ because a public hostname is not necessarily resolvable from
// inside the cluster.
type gatewaySettings struct {
	// publicBaseURL is "<scheme>://<hostname>" (no "/v1"): the address a
	// client uses.
	publicBaseURL string
	// internalBaseURL is the origin the server itself reaches the gateway
	// at. Empty when neither a setting nor discovery provided one.
	internalBaseURL string
}

// resolveGatewaySettings applies the precedence the platform documents:
//
//	public URL:   operator setting → infer.endpointBaseURL → discovered gateway
//	internal URL: operator setting → discovered gateway → infer.endpointBaseURL → public URL
//
// The operator setting comes first because the console is where an
// administrator can correct the value without a redeploy. For forwarding,
// discovery comes before the static configuration so the server talks to
// the gateway directly instead of back through the public ingress.
func (s *Service) resolveGatewaySettings(ctx context.Context) gatewaySettings {
	var out gatewaySettings

	hostname, scheme, internalURL := "", "", ""
	if s.platformSettings != nil {
		// A settings read failure must not break the endpoint: fall
		// through to the static configuration instead.
		if h, sc, in, err := s.platformSettings.InferenceGateway(ctx); err == nil {
			hostname, scheme, internalURL = strings.TrimSpace(h), strings.TrimSpace(sc), strings.TrimSpace(in)
		}
	}
	if hostname != "" {
		if scheme == "" {
			scheme = "https"
		}
		out.publicBaseURL = scheme + "://" + strings.TrimSuffix(hostname, "/")
	}

	configured := ""
	if cfg := config.GetConfig(); cfg != nil {
		configured = strings.TrimSuffix(strings.TrimSpace(cfg.Infer.EndpointBaseURL), "/")
	}
	discovered := ""
	if s.gatewayResolver != nil {
		discovered = strings.TrimSuffix(strings.TrimSpace(s.gatewayResolver.InferenceGatewayURL(ctx)), "/")
	}

	if out.publicBaseURL == "" {
		out.publicBaseURL = configured
	}
	if out.publicBaseURL == "" {
		out.publicBaseURL = discovered
	}

	switch {
	case internalURL != "":
		out.internalBaseURL = strings.TrimSuffix(internalURL, "/")
	case discovered != "":
		out.internalBaseURL = discovered
	case configured != "":
		out.internalBaseURL = configured
	default:
		out.internalBaseURL = out.publicBaseURL
	}
	return out
}

// GetInferenceEndpoint returns the tenant-facing inference base URL
// ("<gateway>/v1"). It is never a client constant. The read requires no
// organization context: it returns the configured base URL regardless of
// the caller's organization.
//
// Resolution order:
//  1. the platform settings an administrator saved in the console;
//  2. infer.endpointBaseURL (an operator's explicit public URL);
//  3. the serving gateway the resolver discovers in the cluster, so a
//     fresh install works without hand-editing configuration;
//  4. a 10304 whose message names the setting to configure — the
//     quickstart page surfaces it verbatim, and a bare "internal error"
//     would leave the operator with nothing to act on.
func (s *Service) GetInferenceEndpoint(ctx context.Context, _ *inferv1.GetInferenceEndpointRequest) (*inferv1.GetInferenceEndpointResponse, error) {
	resolved := s.resolveGatewaySettings(ctx)
	if resolved.publicBaseURL == "" {
		return nil, apierrors.Newf(apierrors.CodeInferEndpointNotFound,
			"infer: no inference gateway is configured; set the inference gateway hostname in the platform settings, or infer.endpointBaseURL")
	}
	return &inferv1.GetInferenceEndpointResponse{
		Response: okResponse(),
		BaseUrl:  resolved.publicBaseURL + "/v1",
	}, nil
}
