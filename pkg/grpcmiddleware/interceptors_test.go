package grpcmiddleware

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	apierrors "github.com/go-taas/go-taas/pkg/errors"
)

func TestNormalizeError(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		wantCode codes.Code
		wantMsg  string
	}{
		{
			name:     "nil",
			err:      nil,
			wantCode: codes.OK,
		},
		{
			name:     "business error",
			err:      apierrors.New(apierrors.CodeModelNotFound),
			wantCode: codes.Code(apierrors.CodeModelNotFound),
			wantMsg:  "model not found",
		},
		{
			name:     "already a status error",
			err:      status.Error(codes.NotFound, "raw"),
			wantCode: codes.NotFound,
			wantMsg:  "raw",
		},
		{
			name:     "plain error collapses to internal",
			err:      assert.AnError,
			wantCode: codes.Internal,
			wantMsg:  "internal error",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := normalizeError(tt.err)
			if tt.err == nil {
				require.NoError(t, got)
				return
			}
			st, ok := status.FromError(got)
			require.True(t, ok, "normalized error must be a status error")
			assert.Equal(t, tt.wantCode, st.Code())
			assert.Equal(t, tt.wantMsg, st.Message())
		})
	}
}

func TestUnaryServerInterceptor(t *testing.T) {
	interceptor := UnaryServerInterceptor()
	info := &grpc.UnaryServerInfo{FullMethod: "/test.Service/Method"}

	// Success path.
	resp, err := interceptor(context.Background(), nil, info, func(_ context.Context, _ any) (any, error) {
		return "ok", nil
	})
	require.NoError(t, err)
	assert.Equal(t, "ok", resp)

	// Business error path.
	_, err = interceptor(context.Background(), nil, info, func(_ context.Context, _ any) (any, error) {
		return nil, apierrors.New(apierrors.CodeAPIKeyRevoked)
	})
	require.Error(t, err)
	st, ok := status.FromError(err)
	require.True(t, ok)
	assert.Equal(t, codes.Code(apierrors.CodeAPIKeyRevoked), st.Code())
}
