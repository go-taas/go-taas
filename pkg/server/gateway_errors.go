package server

// gatewayErrorHandler renders gRPC errors as the unified API error
// envelope. The gRPC server transports business errors as status errors
// whose code is the business code itself (see grpcmiddleware); the
// gateway re-renders them so REST clients see the same {code, message}
// shape as gRPC clients.

import (
	"context"
	"net/http"

	"github.com/grpc-ecosystem/grpc-gateway/v2/runtime"
)

func gatewayErrorHandler(ctx context.Context, mux *runtime.ServeMux, marshaler runtime.Marshaler, w http.ResponseWriter, r *http.Request, err error) {
	// Delegate to the default renderer; it serializes the gRPC status
	// (code + message) as JSON, which matches the unified envelope.
	runtime.DefaultHTTPErrorHandler(ctx, mux, marshaler, w, r, err)
}
