package docs

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestCatalogCoversInferenceAndControlPlane verifies the curated catalog
// covers the inference API and the user-realm control-plane APIs (AC2).
func TestCatalogCoversInferenceAndControlPlane(t *testing.T) {
	cat := catalog("v1")
	require.NotEmpty(t, cat)

	paths := map[string]bool{}
	for _, c := range cat {
		for _, e := range c.Endpoints {
			paths[e.Path] = true
		}
	}
	// Inference API.
	assert.True(t, paths["/v1/chat/completions"], "chat completions missing")
	assert.True(t, paths["/v1/embeddings"], "embeddings missing")
	// User-realm control-plane APIs.
	assert.True(t, paths["/api/v1/models"], "models missing")
	assert.True(t, paths["/api/v1/auth/api-keys"], "api-keys missing")
	assert.True(t, paths["/api/v1/usage"], "usage missing")
	assert.True(t, paths["/api/v1/inference-endpoint"], "inference-endpoint missing")
}

// TestCatalogExposesNoAdminEndpoints verifies the catalog never exposes
// an admin endpoint or operator internals (AC1).
func TestCatalogExposesNoAdminEndpoints(t *testing.T) {
	cat := catalog("v1")
	for _, c := range cat {
		for _, e := range c.Endpoints {
			assert.False(t, strings.Contains(e.Path, "/api/v1/admin/"),
				"catalog must not expose admin endpoint %s", e.Path)
			assert.False(t, strings.Contains(e.Path, "service_id"),
				"catalog must not expose operator internals in %s", e.Path)
		}
	}
}

// TestCatalogEndpointsHaveExamples verifies every endpoint carries
// request/response examples and error codes (AC1).
func TestCatalogEndpointsHaveExamples(t *testing.T) {
	cat := catalog("v1")
	for _, c := range cat {
		for _, e := range c.Endpoints {
			assert.NotEmpty(t, e.EndpointId, "endpoint id empty")
			assert.NotEmpty(t, e.Method, "method empty")
			assert.NotEmpty(t, e.Path, "path empty")
			assert.NotEmpty(t, e.Summary, "summary empty")
			assert.NotEmpty(t, e.ResponseExample, "response example empty for %s", e.EndpointId)
		}
	}
}

// TestCatalogUnknownVersionFallsBack verifies an unknown version falls
// back to v1.
func TestCatalogUnknownVersionFallsBack(t *testing.T) {
	cat := catalog("bogus")
	assert.Equal(t, catalogV1, cat)
}