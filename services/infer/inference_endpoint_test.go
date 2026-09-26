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
// trimmed, and an empty configuration returns the standard internal
// error.
func TestGetInferenceEndpoint(t *testing.T) {
	svc := New(nil)

	// Empty configuration → CodeInternal (500).
	config.SetConfigForTest(&config.Configuration{})
	t.Cleanup(func() { config.SetConfigForTest(&config.Configuration{}) })
	_, err := svc.GetInferenceEndpoint(context.Background(), &inferv1.GetInferenceEndpointRequest{})
	assert.EqualValues(t, apierrors.CodeInternal, apierrors.CodeOf(err))

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

	// Whitespace-only configuration is treated as empty → CodeInternal.
	cfg.Infer.EndpointBaseURL = "   "
	config.SetConfigForTest(cfg)
	_, err = svc.GetInferenceEndpoint(context.Background(), &inferv1.GetInferenceEndpointRequest{})
	assert.EqualValues(t, apierrors.CodeInternal, apierrors.CodeOf(err))
}
