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
