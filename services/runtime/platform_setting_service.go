package runtime

import (
	"context"
	"encoding/json"
	"net"
	"regexp"
	"strings"

	runtimev1 "github.com/go-taas/go-taas/proto/taas/runtime/v1"

	"github.com/go-taas/go-taas/pkg/config"
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

// actorLabel resolves a stored actor id to a display label, falling back to
// the id itself when no resolver is wired or the user is gone.
func (s *Service) actorLabel(ctx context.Context, userID string) string {
	if userID == "" || s.userLabelResolver == nil {
		return userID
	}
	labels, err := s.userLabelResolver.ResolveUserLabels(ctx, []string{userID})
	if err != nil {
		return userID
	}
	if label := labels[userID]; label != "" {
		return label
	}
	return userID
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
	projected := platformSettingsToProto(s.effectiveHostname(row))
	projected.UpdatedBy = s.actorLabel(ctx, row.UpdatedBy)
	projected.ServedHostnames = s.servedHostnames(ctx)
	return &runtimev1.GetPlatformSettingsResponse{
		Response: okResponse(),
		Settings: projected,
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

	// Publishing comes before persistence: a hostname that no route serves
	// would be advertised to every tenant as the endpoint to call, and the
	// gateway would answer 404. Failing here keeps the previously working
	// hostname in place.
	if err := s.publishHostname(ctx, hostname); err != nil {
		return nil, err
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
	projected := platformSettingsToProto(updated)
	projected.UpdatedBy = s.actorLabel(ctx, updated.UpdatedBy)
	projected.ServedHostnames = s.servedHostnames(ctx)
	return &runtimev1.UpdatePlatformSettingsResponse{
		Response: okResponse(),
		Settings: projected,
	}, nil
}

// dataPlaneConfig returns the install-time data-plane settings, applying
// the namespace default.
func dataPlaneConfig() config.RuntimeDataPlaneConfig {
	cfg := config.GetConfig()
	if cfg == nil {
		return config.RuntimeDataPlaneConfig{}
	}
	dp := cfg.Runtime.DataPlane
	if dp.RouteNamespace == "" {
		dp.RouteNamespace = cfg.Controller.Namespace
	}
	return dp
}

// publishHostname makes the data-plane route serve the saved hostname.
// An empty hostname is a no-op (clearing the setting must not take the
// data plane offline), and a deployment without a router seam stores the
// value without touching the cluster.
func (s *Service) publishHostname(ctx context.Context, hostname string) error {
	if hostname == "" || s.dataPlaneRouter == nil {
		return nil
	}
	dp := dataPlaneConfig()
	if !dp.Enabled {
		return apierrors.Newf(apierrors.CodeRuntimeRouteNotPublished,
			"runtime: publishing the data plane is disabled; set runtime.dataPlane.enabled (the chart's dataPlane.enabled)")
	}
	if err := s.dataPlaneRouter.Ensure(ctx, hostname, dp); err != nil {
		return apierrors.Newf(apierrors.CodeRuntimeRouteNotPublished,
			"runtime: could not publish the inference hostname %q: %v", hostname, err)
	}
	return nil
}

// servedHostnames reports what the data-plane route serves, for the
// console to compare with the saved setting.
func (s *Service) servedHostnames(ctx context.Context) []string {
	if s.dataPlaneRouter == nil {
		return nil
	}
	hostnames, err := s.dataPlaneRouter.ServedHostnames(ctx, dataPlaneConfig().RouteNamespace)
	if err != nil {
		return nil
	}
	return hostnames
}

// EnsureDataPlane makes the data-plane route serve the configured hostname:
// the saved value, or the chart's initial value when nothing is saved yet.
// It is safe to call repeatedly and is the runner's resync.
func (s *Service) EnsureDataPlane(ctx context.Context) error {
	if s.dataPlaneRouter == nil {
		return nil
	}
	dp := dataPlaneConfig()
	if !dp.Enabled {
		return nil
	}
	repo, err := s.repository()
	if err != nil {
		return err
	}
	row, err := repo.GetPlatformSettings(ctx)
	if err != nil {
		return err
	}
	hostname := row.InferenceGatewayHostname
	if hostname == "" {
		hostname = dp.Hostname
	}
	if hostname == "" {
		return nil
	}
	return s.dataPlaneRouter.Ensure(ctx, hostname, dp)
}

// effectiveHostname is the hostname the platform reports: the saved value,
// or the chart's initial value while nothing has been saved. Reporting the
// initial value means a fresh install already shows the address users can
// call, and saving it makes it explicit.
func (s *Service) effectiveHostname(row *PlatformSetting) *PlatformSetting {
	if row == nil || row.InferenceGatewayHostname != "" {
		return row
	}
	dp := dataPlaneConfig()
	if dp.Hostname == "" {
		return row
	}
	out := *row
	out.InferenceGatewayHostname = dp.Hostname
	if out.InferenceGatewayScheme == "" {
		out.InferenceGatewayScheme = GatewaySchemeHTTPS
	}
	return &out
}
