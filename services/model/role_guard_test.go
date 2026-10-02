package model

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/metadata"

	modelv1 "github.com/go-taas/go-taas/proto/taas/model/v1"

	apierrors "github.com/go-taas/go-taas/pkg/errors"
)

// fakeRoleGuard enforces a minimum role; it returns CodeForbidden when
// the caller is not allowed.
type fakeRoleGuard struct {
	allowed bool
}

func (f fakeRoleGuard) RequireRole(_ context.Context, _, _, _ string) error {
	if !f.allowed {
		return apierrors.New(apierrors.CodeForbidden)
	}
	return nil
}

// fakeSessionUserResolver returns a fixed session user id.
type fakeSessionUserResolver struct {
	user string
}

func (f fakeSessionUserResolver) SessionUserID(context.Context) (string, error) {
	return f.user, nil
}

// adminCtx returns a context carrying the admin request path metadata so
// surfaceFromContext resolves admin, plus the caller org.
func adminCtx() context.Context {
	return metadata.NewIncomingContext(context.Background(),
		metadata.Pairs("x-request-path", "/api/v1/admin/models", organizationMetadataKey, "org-a"))
}

// TestListModelVersionsRoleGuard verifies the admin version RPC is gated
// by the caller's org role (feature #32, §3.3): a non-member admin
// session receives 10036.
func TestListModelVersionsRoleGuard(t *testing.T) {
	svc := newModelTestService(t)
	reg, err := svc.RegisterModel(context.Background(), &modelv1.RegisterModelRequest{
		Name: "qwen-3b", Version: "v1", WeightPath: "qwen/v1",
	})
	require.NoError(t, err)

	svc.SetSessionUserResolver(fakeSessionUserResolver{user: "user-1"})
	svc.SetSessionOrgResolver(fakeSessionOrgResolver{org: "org-a"})

	// Non-member: the role guard denies the admin version RPC with 10036.
	svc.SetRoleGuard(fakeRoleGuard{allowed: false})
	_, err = svc.ListModelVersions(adminCtx(), &modelv1.ListModelVersionsRequest{ModelId: reg.GetModelId()})
	require.Error(t, err)
	assert.Equal(t, apierrors.CodeForbidden, apierrors.CodeOf(err))

	// Member: the admin version RPC succeeds.
	svc.SetRoleGuard(fakeRoleGuard{allowed: true})
	resp, err := svc.ListModelVersions(adminCtx(), &modelv1.ListModelVersionsRequest{ModelId: reg.GetModelId()})
	require.NoError(t, err)
	require.NotNil(t, resp)
}

// TestActivateModelVersionRoleGuard verifies the admin activate RPC is
// gated by the caller's org role (feature #32, §3.3): a non-member admin
// session receives 10036.
func TestActivateModelVersionRoleGuard(t *testing.T) {
	svc := newModelTestService(t)
	reg, err := svc.RegisterModel(context.Background(), &modelv1.RegisterModelRequest{
		Name: "qwen-3b", Version: "v1", WeightPath: "qwen/v1",
	})
	require.NoError(t, err)

	svc.SetSessionUserResolver(fakeSessionUserResolver{user: "user-1"})
	svc.SetSessionOrgResolver(fakeSessionOrgResolver{org: "org-a"})

	// Non-member: the role guard denies the admin activate RPC with
	// 10036.
	svc.SetRoleGuard(fakeRoleGuard{allowed: false})
	_, err = svc.ActivateModelVersion(adminCtx(), &modelv1.ActivateModelVersionRequest{
		ModelId: reg.GetModelId(), Version: "v1",
	})
	require.Error(t, err)
	assert.Equal(t, apierrors.CodeForbidden, apierrors.CodeOf(err))

	// Member: the admin activate RPC succeeds.
	svc.SetRoleGuard(fakeRoleGuard{allowed: true})
	resp, err := svc.ActivateModelVersion(adminCtx(), &modelv1.ActivateModelVersionRequest{
		ModelId: reg.GetModelId(), Version: "v1",
	})
	require.NoError(t, err)
	assert.Equal(t, "v1", resp.GetActiveVersion())
}

// TestModelVersionRoleGuardNoop verifies the role check is a no-op when
// the guard is not wired or no session is present (transitional path).
func TestModelVersionRoleGuardNoop(t *testing.T) {
	svc := newModelTestService(t)
	reg, err := svc.RegisterModel(context.Background(), &modelv1.RegisterModelRequest{
		Name: "qwen-3b", Version: "v1", WeightPath: "qwen/v1",
	})
	require.NoError(t, err)

	// No role guard wired: the admin RPC is not role-gated.
	_, err = svc.ListModelVersions(adminCtx(), &modelv1.ListModelVersionsRequest{ModelId: reg.GetModelId()})
	require.NoError(t, err)

	// Role guard wired but no session user resolver: no session, so the
	// check is skipped (the transitional X-Organization-Id header is the
	// access boundary).
	svc.SetRoleGuard(fakeRoleGuard{allowed: false})
	_, err = svc.ListModelVersions(adminCtx(), &modelv1.ListModelVersionsRequest{ModelId: reg.GetModelId()})
	require.NoError(t, err)
}
