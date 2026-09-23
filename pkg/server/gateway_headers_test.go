package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/grpc-ecosystem/grpc-gateway/v2/runtime"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/metadata"
)

func TestIncomingHeaderMatcherPassesOrganizationHeader(t *testing.T) {
	// The matcher must pass X-Organization-Id through as
	// x-organization-id metadata, and keep the default behavior for
	// permanent headers.
	key, ok := incomingHeaderMatcher("X-Organization-Id")
	require.True(t, ok)
	assert.Equal(t, "x-organization-id", key)

	// Default behavior preserved for permanent headers.
	key, ok = incomingHeaderMatcher("Authorization")
	require.True(t, ok)
	assert.Equal(t, "grpcgateway-Authorization", key)

	// Unknown custom headers are still dropped by default.
	_, ok = incomingHeaderMatcher("X-Custom-Dropped")
	assert.False(t, ok)
}

func TestGatewayMuxForwardsOrganizationHeader(t *testing.T) {
	// Exercise the annotation path used by generated gateway handlers:
	// the HTTP header must arrive as incoming gRPC metadata.
	mux := runtime.NewServeMux(
		runtime.WithErrorHandler(gatewayErrorHandler),
		runtime.WithIncomingHeaderMatcher(incomingHeaderMatcher),
	)
	req := httptest.NewRequest(http.MethodGet, "/test-org-header", nil)
	req.Header.Set("X-Organization-Id", "org-42")
	ctx, err := runtime.AnnotateIncomingContext(context.Background(), mux, req, "/test.Service/Method")
	require.NoError(t, err)
	md, ok := metadata.FromIncomingContext(ctx)
	require.True(t, ok)
	assert.Equal(t, []string{"org-42"}, md.Get("x-organization-id"))
}
