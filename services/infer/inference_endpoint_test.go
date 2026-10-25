package infer

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	inferv1 "github.com/go-taas/go-taas/proto/taas/infer/v1"

	"github.com/go-taas/go-taas/pkg/config"
	apierrors "github.com/go-taas/go-taas/pkg/errors"
)

// TestGetInferenceEndpoint covers the user-realm inference-endpoint read
// (feature #21, AD9): the base_url is derived from the configured
// infer.endpointBaseURL (never a client constant), a trailing slash is
// trimmed, and an empty configuration with no discoverable gateway fails
// with an actionable 10304 rather than a bare 500.
func TestGetInferenceEndpoint(t *testing.T) {
	svc := New(nil)

	// Empty configuration and no gateway → 10304 with a message that
	// names the setting to fix (a bare "internal error" is not
	// actionable for the operator reading the quickstart page).
	config.SetConfigForTest(&config.Configuration{})
	t.Cleanup(func() { config.SetConfigForTest(&config.Configuration{}) })
	_, err := svc.GetInferenceEndpoint(context.Background(), &inferv1.GetInferenceEndpointRequest{})
	assert.EqualValues(t, apierrors.CodeInferEndpointNotFound, apierrors.CodeOf(err))
	ae, ok := apierrors.As(err)
	require.True(t, ok)
	assert.Contains(t, ae.Message, "infer.endpointBaseURL")

	// Configured base URL → base_url = TrimSuffix(base, "/") + "/v1".
	cfg := &config.Configuration{}
	cfg.Infer.EndpointBaseURL = "https://infer.example.com"
	config.SetConfigForTest(cfg)
	resp, err := svc.GetInferenceEndpoint(context.Background(), &inferv1.GetInferenceEndpointRequest{})
	require.NoError(t, err)
	assert.Equal(t, "https://infer.example.com/v1", resp.GetBaseUrl())
	assert.Equal(t, int32(0), resp.GetResponse().GetCode())

	// A trailing slash is trimmed before appending /v1.
	cfg.Infer.EndpointBaseURL = "https://infer.example.com/"
	config.SetConfigForTest(cfg)
	resp, err = svc.GetInferenceEndpoint(context.Background(), &inferv1.GetInferenceEndpointRequest{})
	require.NoError(t, err)
	assert.Equal(t, "https://infer.example.com/v1", resp.GetBaseUrl())

	// Whitespace-only configuration is treated as empty → 10304.
	cfg.Infer.EndpointBaseURL = "   "
	config.SetConfigForTest(cfg)
	_, err = svc.GetInferenceEndpoint(context.Background(), &inferv1.GetInferenceEndpointRequest{})
	assert.EqualValues(t, apierrors.CodeInferEndpointNotFound, apierrors.CodeOf(err))
}

// fakeGatewayResolver returns a fixed in-cluster gateway URL.
type fakeGatewayResolver struct{ url string }

func (f fakeGatewayResolver) InferenceGatewayURL(context.Context) string { return f.url }

// TestGetInferenceEndpointDiscoversGateway verifies a fresh install works
// without manual configuration: when infer.endpointBaseURL is unset the
// endpoint is derived from the serving gateway the resolver discovers. An
// explicit configuration still wins.
func TestGetInferenceEndpointDiscoversGateway(t *testing.T) {
	svc := New(nil)
	config.SetConfigForTest(&config.Configuration{})
	t.Cleanup(func() { config.SetConfigForTest(&config.Configuration{}) })

	svc.SetGatewayResolver(fakeGatewayResolver{url: "http://10.43.253.124/"})
	resp, err := svc.GetInferenceEndpoint(context.Background(), &inferv1.GetInferenceEndpointRequest{})
	require.NoError(t, err)
	assert.Equal(t, "http://10.43.253.124/v1", resp.GetBaseUrl())

	// An explicit configuration wins over discovery.
	cfg := &config.Configuration{}
	cfg.Infer.EndpointBaseURL = "https://infer.example.com"
	config.SetConfigForTest(cfg)
	resp, err = svc.GetInferenceEndpoint(context.Background(), &inferv1.GetInferenceEndpointRequest{})
	require.NoError(t, err)
	assert.Equal(t, "https://infer.example.com/v1", resp.GetBaseUrl())

	// A resolver with no gateway keeps the actionable failure.
	svc.SetGatewayResolver(fakeGatewayResolver{})
	cfg.Infer.EndpointBaseURL = ""
	config.SetConfigForTest(cfg)
	_, err = svc.GetInferenceEndpoint(context.Background(), &inferv1.GetInferenceEndpointRequest{})
	assert.EqualValues(t, apierrors.CodeInferEndpointNotFound, apierrors.CodeOf(err))
}

// fakePlatformSettings is an injected PlatformSettingsResolver.
type fakePlatformSettings struct {
	hostname, scheme, internalURL string
	err                           error
}

func (f fakePlatformSettings) InferenceGateway(context.Context) (string, string, string, error) {
	return f.hostname, f.scheme, f.internalURL, f.err
}

// TestGetInferenceEndpointUsesPlatformSettings verifies the operator
// setting is what users are given, ahead of the static configuration, and
// that the in-cluster URL is kept separate for the server's own calls.
func TestGetInferenceEndpointUsesPlatformSettings(t *testing.T) {
	svc := New(nil)
	cfg := &config.Configuration{}
	cfg.Infer.EndpointBaseURL = "https://static.example.com"
	config.SetConfigForTest(cfg)
	t.Cleanup(func() { config.SetConfigForTest(&config.Configuration{}) })

	svc.SetGatewayResolver(fakeGatewayResolver{url: "http://10.43.253.124"})
	svc.SetPlatformSettings(fakePlatformSettings{
		hostname: "api.example.com", scheme: "https", internalURL: "",
	})

	resp, err := svc.GetInferenceEndpoint(context.Background(), &inferv1.GetInferenceEndpointRequest{})
	require.NoError(t, err)
	assert.Equal(t, "https://api.example.com/v1", resp.GetBaseUrl(),
		"the console setting outranks the static configuration")

	// The server's own calls prefer the in-cluster gateway, not the
	// public hostname (which a pod may not resolve).
	assert.Equal(t, "http://10.43.253.124", svc.servingGatewayBaseURL(context.Background()))

	// An explicit internal URL wins over discovery.
	svc.SetPlatformSettings(fakePlatformSettings{
		hostname: "api.example.com", scheme: "http", internalURL: "http://gateway.internal:8080/",
	})
	resp, err = svc.GetInferenceEndpoint(context.Background(), &inferv1.GetInferenceEndpointRequest{})
	require.NoError(t, err)
	assert.Equal(t, "http://api.example.com/v1", resp.GetBaseUrl())
	assert.Equal(t, "http://gateway.internal:8080", svc.servingGatewayBaseURL(context.Background()))

	// With no internal URL and no discovery, forwarding falls back to the
	// public hostname rather than failing.
	svc.SetGatewayResolver(fakeGatewayResolver{})
	cfg.Infer.EndpointBaseURL = ""
	config.SetConfigForTest(cfg)
	svc.SetPlatformSettings(fakePlatformSettings{hostname: "api.example.com", scheme: "https"})
	assert.Equal(t, "https://api.example.com", svc.servingGatewayBaseURL(context.Background()))

	// A stored scheme other than http is reported as https.
	svc.SetPlatformSettings(fakePlatformSettings{hostname: "api.example.com"})
	resp, err = svc.GetInferenceEndpoint(context.Background(), &inferv1.GetInferenceEndpointRequest{})
	require.NoError(t, err)
	assert.Equal(t, "https://api.example.com/v1", resp.GetBaseUrl())

	// An empty hostname clears the override and falls back.
	svc.SetPlatformSettings(fakePlatformSettings{})
	svc.SetGatewayResolver(fakeGatewayResolver{url: "http://10.43.253.124"})
	resp, err = svc.GetInferenceEndpoint(context.Background(), &inferv1.GetInferenceEndpointRequest{})
	require.NoError(t, err)
	assert.Equal(t, "http://10.43.253.124/v1", resp.GetBaseUrl())
}

// TestGetInferenceEndpointSurvivesSettingsFailure keeps a settings-store
// outage from breaking the endpoint read.
func TestGetInferenceEndpointSurvivesSettingsFailure(t *testing.T) {
	svc := New(nil)
	config.SetConfigForTest(&config.Configuration{})
	t.Cleanup(func() { config.SetConfigForTest(&config.Configuration{}) })

	svc.SetPlatformSettings(fakePlatformSettings{err: apierrors.New(apierrors.CodeInternal)})
	svc.SetGatewayResolver(fakeGatewayResolver{url: "http://10.43.253.124"})
	resp, err := svc.GetInferenceEndpoint(context.Background(), &inferv1.GetInferenceEndpointRequest{})
	require.NoError(t, err)
	assert.Equal(t, "http://10.43.253.124/v1", resp.GetBaseUrl())
}
