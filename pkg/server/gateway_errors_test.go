package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	apierrors "github.com/go-taas/go-taas/pkg/errors"
	"github.com/grpc-ecosystem/grpc-gateway/v2/runtime"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// TestGatewayErrorHandlerAnswersBadRequestForInputCodes proves a caller mistake
// is a 400 that keeps the business code and message, so the console can show
// the precise reason instead of an opaque server fault.
func TestGatewayErrorHandlerAnswersBadRequestForInputCodes(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/images:import", nil)
	err := status.Error(codes.Code(apierrors.CodeImageReferenceInvalid), "image: source_password must be at most 1024 characters")

	gatewayErrorHandler(context.Background(), runtime.NewServeMux(), &runtime.JSONPb{}, rec, req, err)

	require.Equal(t, http.StatusBadRequest, rec.Code)
	var body map[string]any
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&body))
	assert.Equal(t, float64(apierrors.CodeImageReferenceInvalid), body["code"])
	assert.Equal(t, "image: source_password must be at most 1024 characters", body["message"])
}

// TestGatewayErrorHandlerKeepsServerFaultsAsFiveHundred proves the mapping is
// narrow: a genuine server fault must stay a 500 so operators do not learn to
// treat every internal error as their own bad input.
func TestGatewayErrorHandlerKeepsServerFaultsAt500(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/models", nil)
	err := status.Error(codes.Internal, "internal error")

	gatewayErrorHandler(context.Background(), runtime.NewServeMux(), &runtime.JSONPb{}, rec, req, err)

	assert.Equal(t, http.StatusInternalServerError, rec.Code)
}
