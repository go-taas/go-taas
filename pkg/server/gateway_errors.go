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
	"encoding/json"
	"net/http"
	"strings"

	apierrors "github.com/go-taas/go-taas/pkg/errors"
	"github.com/grpc-ecosystem/grpc-gateway/v2/runtime"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

func gatewayErrorHandler(ctx context.Context, mux *runtime.ServeMux, marshaler runtime.Marshaler, w http.ResponseWriter, r *http.Request, err error) {
	// Input errors answer 400 with the business code and message preserved;
	// everything else delegates to the default renderer, which serializes
	// the gRPC status as the unified envelope.
	if st, ok := status.FromError(err); ok && isClientInputCode(int32(st.Code())) { //nolint:gosec // G115: business codes are small positive ints
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"code":    int32(st.Code()), //nolint:gosec // G115: business codes are small positive ints
			"message": st.Message(),
			"details": []any{},
		})
		return
	}
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

// pathMetadataKey is the gRPC metadata key carrying the request path.
// The webhook service derives its surface from it (feature #23, §3.4).
const pathMetadataKey = "x-request-path"

// PathMetadataAnnotator forwards the HTTP request path into the gRPC
// metadata as x-request-path, so services can derive the console surface
// from the request path (feature #23, §3.4). It is exported for
// full-verification tests that build their own gateway mux.
func PathMetadataAnnotator(_ context.Context, r *http.Request) metadata.MD {
	return metadata.Pairs(pathMetadataKey, r.URL.Path)
}

// isClientInputCode reports whether a business code means the caller sent
// malformed input. The gateway answers those with HTTP 400 while keeping the
// business code and message in the envelope, so the console still shows the
// precise reason instead of a generic server fault.
func isClientInputCode(code int32) bool {
	switch apierrors.Code(code) {
	case apierrors.CodeImageDigestInvalid,
		apierrors.CodeImageReferenceInvalid,
		apierrors.CodeCompatibilityDimensionInvalid,
		apierrors.CodeCompatibilityStatusInvalid:
		return true
	}
	return false
}
