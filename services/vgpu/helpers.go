// Package vgpu implements the HAMI-based vGPU slicing-pool module
// (feature #52): pool CRUD, label-driven node membership, slice
// templates, capacity accounting, and the deploy-time slice validation
// seam.
package vgpu

import (
	"context"
	"strings"

	"github.com/google/uuid"
	"google.golang.org/grpc/metadata"

	apierrors "github.com/go-taas/go-taas/pkg/errors"
)

// organizationMetadataKey is the gRPC metadata key carrying the
// transitional X-Organization-Id header (the auth module's pattern).
const organizationMetadataKey = "x-organization-id"

// newUUID returns a new UUID v4 string.
func newUUID() string {
	return uuid.NewString()
}

// metadataFromIncomingContext reads the incoming gRPC metadata.
func metadataFromIncomingContext(ctx context.Context) (metadata.MD, bool) {
	return metadata.FromIncomingContext(ctx)
}

// resolveOrganizationID reads the transitional caller organization from
// the x-organization-id gRPC metadata (set by the gateway from the
// X-Organization-Id HTTP header). Missing or empty values are
// unauthorized.
func resolveOrganizationID(ctx context.Context) (string, error) {
	md, ok := metadataFromIncomingContext(ctx)
	if !ok {
		return "", apierrors.New(apierrors.CodeUnauthorized)
	}
	values := md.Get(organizationMetadataKey)
	if len(values) == 0 || strings.TrimSpace(values[0]) == "" {
		return "", apierrors.New(apierrors.CodeUnauthorized)
	}
	return strings.TrimSpace(values[0]), nil
}