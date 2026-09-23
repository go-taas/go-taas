package server

// gatewayErrorHandler renders gRPC errors as the unified API error
// envelope. The gRPC server transports business errors as status errors
// whose code is the business code itself (see grpcmiddleware); the
// gateway re-renders them so REST clients see the same {code, message}
// shape as gRPC clients.
//
// incomingHeaderMatcher passes the transitional X-Organization-Id HTTP
// header through to gRPC metadata as x-organization-id, on top of the
// gateway's default matcher. It is removed when session-derived
// identity lands (feature #7).

import (
	"context"
	"net/http"
	"strings"

	"github.com/grpc-ecosystem/grpc-gateway/v2/runtime"
)

func gatewayErrorHandler(ctx context.Context, mux *runtime.ServeMux, marshaler runtime.Marshaler, w http.ResponseWriter, r *http.Request, err error) {
	// Delegate to the default renderer; it serializes the gRPC status
	// (code + message) as JSON, which matches the unified envelope.
	runtime.DefaultHTTPErrorHandler(ctx, mux, marshaler, w, r, err)
}

// organizationHeaderKey is the transitional caller-identity header.
const organizationHeaderKey = "X-Organization-Id"

// incomingHeaderMatcher extends runtime.DefaultHeaderMatcher with the
// transitional X-Organization-Id pass-through.
func incomingHeaderMatcher(key string) (string, bool) {
	if strings.EqualFold(key, organizationHeaderKey) {
		return "x-organization-id", true
	}
	return runtime.DefaultHeaderMatcher(key)
}

// FVTHeaderMatcher exposes the gateway's incoming header matcher for
// full-verification tests that build their own gateway mux and must
// reproduce the production header pass-through.
func FVTHeaderMatcher(key string) (string, bool) {
	return incomingHeaderMatcher(key)
}
