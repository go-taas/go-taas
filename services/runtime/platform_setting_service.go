package runtime

import (
	"context"
	"encoding/json"
	"net"
	"regexp"
	"strings"

	runtimev1 "github.com/go-taas/go-taas/proto/taas/runtime/v1"

	apierrors "github.com/go-taas/go-taas/pkg/errors"
	"github.com/go-taas/go-taas/services/audit"
)

// hostnamePattern accepts a DNS name with an optional port. A scheme, a
// path, a query or whitespace is rejected: the scheme is its own field
// and the rest would be silently discarded when composing a URL.
var hostnamePattern = regexp.MustCompile(`^[a-zA-Z0-9]([a-zA-Z0-9._-]*[a-zA-Z0-9])?(:[0-9]{1,5})?$`)

// normaliseHostname lowercases a hostname and drops a trailing dot.
func normaliseHostname(raw string) (string, error) {
	host := strings.TrimSpace(raw)
	if host == "" {
		return "", nil
	}
	if strings.Contains(host, "://") {
		return "", apierrors.Newf(apierrors.CodeRuntimeSettingInvalid,
			"runtime: inference gateway hostname must not include a scheme; set the scheme separately")
	}
	if strings.ContainsAny(host, "/?#@ ") {
		return "", apierrors.Newf(apierrors.CodeRuntimeSettingInvalid,
			"runtime: inference gateway hostname must be a plain host name, optionally with a port")
	}
	host = strings.TrimSuffix(strings.ToLower(host), ".")
	if !hostnamePattern.MatchString(host) {
		return "", apierrors.Newf(apierrors.CodeRuntimeSettingInvalid,
			"runtime: inference gateway hostname %q is not a valid host name", raw)
	}
	return host, nil
}

// normaliseScheme accepts http, https or empty (https).
func normaliseScheme(raw string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", GatewaySchemeHTTPS:
		return GatewaySchemeHTTPS, nil
	case GatewaySchemeHTTP:
		return GatewaySchemeHTTP, nil
	default:
		return "", apierrors.Newf(apierrors.CodeRuntimeSettingInvalid,
			"runtime: inference gateway scheme must be http or https, got %q", raw)
	}
}

// normaliseInternalURL validates the in-cluster gateway URL, which is a
// plain origin ("http://host:port") with no path.
func normaliseInternalURL(raw string) (string, error) {
	url := strings.TrimSuffix(strings.TrimSpace(raw), "/")
	if url == "" {
		return "", nil
	}
	if !strings.HasPrefix(url, "http://") && !strings.HasPrefix(url, "https://") {
		return "", apierrors.Newf(apierrors.CodeRuntimeSettingInvalid,
			"runtime: internal gateway URL must start with http:// or https://")
	}
	rest := strings.TrimPrefix(strings.TrimPrefix(url, "https://"), "http://")
	if rest == "" || strings.Contains(rest, "/") {
		return "", apierrors.Newf(apierrors.CodeRuntimeSettingInvalid,
			"runtime: internal gateway URL must be an origin (scheme://host[:port]) without a path")
	}
	return url, nil
}

// platformSettingsToProto projects the stored row onto the wire shape.
func platformSettingsToProto(row *PlatformSetting) *runtimev1.PlatformSettings {
	if row == nil {
		return nil
	}
	return &runtimev1.PlatformSettings{
		InferenceGatewayHostname:    row.InferenceGatewayHostname,
		InferenceGatewayScheme:      row.GatewayScheme(),
		InferenceGatewayInternalUrl: row.InferenceGatewayInternalURL,
		InferenceBaseUrl:            row.InferenceBaseURL(),
		UpdatedBy:                   row.UpdatedBy,
		UpdatedAt:                   row.UpdatedAt.Unix(),
	}
}

// InferenceGateway returns the operator-configured data-plane endpoint.
// It implements the infer module's settings seam so the endpoint reported
// to users and the platform's own forwarding use the same values.
func (s *Service) InferenceGateway(ctx context.Context) (hostname, scheme, internalURL string, err error) {
	repo, err := s.repository()
	if err != nil {
		return "", "", "", err
	}
	row, err := repo.GetPlatformSettings(ctx)
	if err != nil {
		return "", "", "", err
	}
	return row.InferenceGatewayHostname, row.GatewayScheme(), row.InferenceGatewayInternalURL, nil
}

// GetPlatformSettings returns the operator-editable platform settings.
func (s *Service) GetPlatformSettings(ctx context.Context, _ *runtimev1.GetPlatformSettingsRequest) (*runtimev1.GetPlatformSettingsResponse, error) {
	if err := s.requireAdminRole(ctx); err != nil {
		return nil, err
	}
	repo, err := s.repository()
	if err != nil {
		return nil, err
	}
	row, err := repo.GetPlatformSettings(ctx)
	if err != nil {
		return nil, err
	}
	return &runtimev1.GetPlatformSettingsResponse{
		Response: okResponse(),
		Settings: platformSettingsToProto(row),
	}, nil
}

// UpdatePlatformSettings saves the platform settings. The three fields
// are validated before anything is written, so a rejected value never
// leaves the installation pointing at a broken gateway.
func (s *Service) UpdatePlatformSettings(ctx context.Context, req *runtimev1.UpdatePlatformSettingsRequest) (*runtimev1.UpdatePlatformSettingsResponse, error) {
	if err := s.requireAdminRole(ctx); err != nil {
		return nil, err
	}
	hostname, err := normaliseHostname(req.GetInferenceGatewayHostname())
	if err != nil {
		return nil, err
	}
	scheme, err := normaliseScheme(req.GetInferenceGatewayScheme())
	if err != nil {
		return nil, err
	}
	internalURL, err := normaliseInternalURL(req.GetInferenceGatewayInternalUrl())
	if err != nil {
		return nil, err
	}
	// A host without a port must be reachable as a name; reject the
	// obvious mistake of pasting an IP with a scheme-less port range.
	if host, _, splitErr := net.SplitHostPort(hostname); splitErr == nil && host == "" {
		return nil, apierrors.Newf(apierrors.CodeRuntimeSettingInvalid,
			"runtime: inference gateway hostname %q has no host part", req.GetInferenceGatewayHostname())
	}

	repo, err := s.repository()
	if err != nil {
		return nil, err
	}
	before, err := repo.GetPlatformSettings(ctx)
	if err != nil {
		return nil, err
	}
	actor := s.actor(ctx)
	updated, err := repo.UpdatePlatformSettings(ctx, &PlatformSetting{
		InferenceGatewayHostname:    hostname,
		InferenceGatewayScheme:      scheme,
		InferenceGatewayInternalURL: internalURL,
		UpdatedBy:                   actor,
	})
	if err != nil {
		return nil, err
	}
	if s.auditRecorder != nil {
		beforeJSON, _ := json.Marshal(map[string]string{
			"hostname":    before.InferenceGatewayHostname,
			"scheme":      before.GatewayScheme(),
			"internalUrl": before.InferenceGatewayInternalURL,
		})
		afterJSON, _ := json.Marshal(map[string]string{
			"hostname":    updated.InferenceGatewayHostname,
			"scheme":      updated.GatewayScheme(),
			"internalUrl": updated.InferenceGatewayInternalURL,
		})
		s.auditRecorder.Record(ctx, &audit.AuditEvent{
			ActorUserID:  actor,
			ActorType:    "user",
			Action:       "runtime.platform_settings_changed",
			ResourceType: "runtime",
			ResourceID:   platformSettingID,
			Result:       "success",
			Metadata:     string(mustJSON(map[string]string{"before": string(beforeJSON), "after": string(afterJSON)})),
		})
	}
	return &runtimev1.UpdatePlatformSettingsResponse{
		Response: okResponse(),
		Settings: platformSettingsToProto(updated),
	}, nil
}
