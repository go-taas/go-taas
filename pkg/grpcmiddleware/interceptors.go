// Package grpcmiddleware provides gRPC server interceptors shared by all
// services: panic recovery, access logging and error normalization.
package grpcmiddleware

import (
	"context"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	apierrors "github.com/go-taas/go-taas/pkg/errors"
	"github.com/go-taas/go-taas/pkg/logger"
)

// UnaryServerInterceptor chains the standard interceptors applied to every
// unary RPC: recovery, access logging and error normalization.
func UnaryServerInterceptor() grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		logger.S().Debugw("grpc request", "method", info.FullMethod)
		resp, err := handler(ctx, req)
		return resp, normalizeError(err)
	}
}

// normalizeError converts business APIError values into gRPC status errors
// with the business code preserved in the status message, so the HTTP
// gateway can render the unified error envelope. Unknown errors collapse
// to codes.Internal without leaking internals.
func normalizeError(err error) error {
	if err == nil {
		return nil
	}
	if ae, ok := apierrors.As(err); ok {
		// Business codes are transported verbatim as the gRPC status
		// code; the gateway re-renders them into the unified envelope.
		//nolint:gosec // G115: business codes are small positive ints.
		return status.Error(codes.Code(ae.Code), ae.Message)
	}
	if _, ok := status.FromError(err); ok {
		return err
	}
	logger.S().Warnw("grpc internal error", "err", err)
	return status.Error(codes.Internal, "internal error")
}
