package infer

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/metadata"

	inferv1 "github.com/go-taas/go-taas/proto/taas/infer/v1"

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
		metadata.Pairs("x-request-path", "/api/v1/admin/inference-services", organizationMetadataKey, "org-a"))
}

// userCtx returns a context carrying the user request path metadata and
// the caller org.
func userCtx(orgID string) context.Context {
	return metadata.NewIncomingContext(context.Background(),
		metadata.Pairs("x-request-path", "/api/v1/playground/compare", organizationMetadataKey, orgID))
}

// TestListServiceLogPodsRoleGuard verifies the admin pod-enumeration RPC
// is gated by the caller's org role (feature #33, §3.3): a non-member
// admin session receives 10036.
func TestListServiceLogPodsRoleGuard(t *testing.T) {
	seedImageRegistry(t)
	svc, _, _ := newInferTestService(t)
	repo, err := svc.repository()
	require.NoError(t, err)
	svcRow := seedService(t, repo, "org-a", "svc-a", StateRunning, time.Now())

	svc.SetSessionUserResolver(fakeSessionUserResolver{user: "user-1"})
	svc.SetSessionOrgResolver(fakeSessionOrgResolver{org: "org-a"})

	// Non-member: the role guard denies the admin pod RPC with 10036.
	svc.SetRoleGuard(fakeRoleGuard{allowed: false})
	_, err = svc.ListServiceLogPods(adminCtx(), &inferv1.ListServiceLogPodsRequest{ServiceId: svcRow.ID})
	require.Error(t, err)
	assert.Equal(t, apierrors.CodeForbidden, apierrors.CodeOf(err))

	// Member: the admin pod RPC proceeds (fails closed on the missing
	// log fetcher, not on the role check).
	svc.SetRoleGuard(fakeRoleGuard{allowed: true})
	_, err = svc.ListServiceLogPods(adminCtx(), &inferv1.ListServiceLogPodsRequest{ServiceId: svcRow.ID})
	require.Error(t, err)
	assert.NotEqual(t, apierrors.CodeForbidden, apierrors.CodeOf(err))
}

// TestGetServiceLogsRoleGuard verifies the admin service-log RPC is gated
// by the caller's org role (feature #33, §3.3): a non-member admin
// session receives 10036; a member succeeds.
func TestGetServiceLogsRoleGuard(t *testing.T) {
	seedImageRegistry(t)
	svc, _, db := newInferTestService(t)
	seedModel(t, db, "qwen-3b", "v1")
	repo, err := svc.repository()
	require.NoError(t, err)
	svcRow := seedService(t, repo, "org-a", "svc-a", StateRunning, time.Now())

	svc.SetSessionUserResolver(fakeSessionUserResolver{user: "user-1"})
	svc.SetSessionOrgResolver(fakeSessionOrgResolver{org: "org-a"})

	// Non-member: the role guard denies the admin log RPC with 10036.
	svc.SetRoleGuard(fakeRoleGuard{allowed: false})
	_, err = svc.GetServiceLogs(adminCtx(), &inferv1.GetServiceLogsRequest{ServiceId: svcRow.ID})
	require.Error(t, err)
	assert.Equal(t, apierrors.CodeForbidden, apierrors.CodeOf(err))

	// Member: the admin log RPC proceeds (fails closed on the missing
	// log fetcher, not on the role check).
	svc.SetRoleGuard(fakeRoleGuard{allowed: true})
	_, err = svc.GetServiceLogs(adminCtx(), &inferv1.GetServiceLogsRequest{ServiceId: svcRow.ID})
	require.Error(t, err)
	assert.NotEqual(t, apierrors.CodeForbidden, apierrors.CodeOf(err))
}

// TestListDeploymentEventsRoleGuard verifies the admin deployment RPC is
// gated by the caller's org role (feature #34, §3.3): a non-member admin
// session receives 10036.
func TestListDeploymentEventsRoleGuard(t *testing.T) {
	seedImageRegistry(t)
	svc, _, _ := newInferTestService(t)
	repo, err := svc.repository()
	require.NoError(t, err)
	svcRow := seedService(t, repo, "org-a", "svc-a", StateRunning, time.Now())

	svc.SetSessionUserResolver(fakeSessionUserResolver{user: "user-1"})
	svc.SetSessionOrgResolver(fakeSessionOrgResolver{org: "org-a"})

	// Non-member: the role guard denies the admin deployment RPC with
	// 10036.
	svc.SetRoleGuard(fakeRoleGuard{allowed: false})
	_, err = svc.ListDeploymentEvents(adminCtx(), &inferv1.ListDeploymentEventsRequest{ServiceId: svcRow.ID})
	require.Error(t, err)
	assert.Equal(t, apierrors.CodeForbidden, apierrors.CodeOf(err))

	// Member: the admin deployment RPC succeeds.
	svc.SetRoleGuard(fakeRoleGuard{allowed: true})
	resp, err := svc.ListDeploymentEvents(adminCtx(), &inferv1.ListDeploymentEventsRequest{ServiceId: svcRow.ID})
	require.NoError(t, err)
	require.NotNil(t, resp)
}

// TestRollbackDeploymentRoleGuard verifies the admin rollback RPC is
// gated by the caller's org role (feature #34, §3.3): a non-member admin
// session receives 10036.
func TestRollbackDeploymentRoleGuard(t *testing.T) {
	seedImageRegistry(t)
	svc, _, _ := newInferTestService(t)
	repo, err := svc.repository()
	require.NoError(t, err)
	svcRow := seedService(t, repo, "org-a", "svc-a", StateRunning, time.Now())

	svc.SetSessionUserResolver(fakeSessionUserResolver{user: "user-1"})
	svc.SetSessionOrgResolver(fakeSessionOrgResolver{org: "org-a"})

	// Non-member: the role guard denies the admin rollback RPC with
	// 10036.
	svc.SetRoleGuard(fakeRoleGuard{allowed: false})
	_, err = svc.RollbackDeployment(adminCtx(), &inferv1.RollbackDeploymentRequest{ServiceId: svcRow.ID, EventId: "evt-1"})
	require.Error(t, err)
	assert.Equal(t, apierrors.CodeForbidden, apierrors.CodeOf(err))
}

// TestCompareModelsRoleGuard verifies the user-realm compare RPC is gated
// by the caller's org role (feature #35, §3.3): a non-member user session
// receives 10036.
func TestCompareModelsRoleGuard(t *testing.T) {
	seedImageRegistry(t)
	svc, _, db := newInferTestService(t)
	modelA := seedModel(t, db, "qwen-3b", "v1")
	modelB := seedModel(t, db, "qwen-7b", "v1")

	svc.SetSessionUserResolver(fakeSessionUserResolver{user: "user-1"})
	svc.SetSessionOrgResolver(fakeSessionOrgResolver{org: "org-a"})

	// Non-member: the role guard denies the compare RPC with 10036.
	svc.SetRoleGuard(fakeRoleGuard{allowed: false})
	_, err := svc.CompareModels(userCtx("org-a"), &inferv1.CompareModelsRequest{
		ModelIds: []string{modelA, modelB}, ApiKeyId: "key-1", Prompt: "hi",
	})
	require.Error(t, err)
	assert.Equal(t, apierrors.CodeForbidden, apierrors.CodeOf(err))

	// Member: the compare RPC proceeds past the role check (it may
	// return results with per-model error markers, never 10036).
	svc.SetRoleGuard(fakeRoleGuard{allowed: true})
	_, err = svc.CompareModels(userCtx("org-a"), &inferv1.CompareModelsRequest{
		ModelIds: []string{modelA, modelB}, ApiKeyId: "key-1", Prompt: "hi",
	})
	if err != nil {
		assert.NotEqual(t, apierrors.CodeForbidden, apierrors.CodeOf(err))
	}
}

// TestRoleGuardNoop verifies the role check is a no-op when the guard is
// not wired or no session is present (transitional path).
func TestRoleGuardNoop(t *testing.T) {
	seedImageRegistry(t)
	svc, _, _ := newInferTestService(t)
	repo, err := svc.repository()
	require.NoError(t, err)
	svcRow := seedService(t, repo, "org-a", "svc-a", StateRunning, time.Now())

	// No role guard wired: the admin RPC is not role-gated.
	_, err = svc.ListDeploymentEvents(adminCtx(), &inferv1.ListDeploymentEventsRequest{ServiceId: svcRow.ID})
	require.NoError(t, err)

	// Role guard wired but no session user resolver: no session, so the
	// check is skipped (the transitional X-Organization-Id header is the
	// access boundary).
	svc.SetRoleGuard(fakeRoleGuard{allowed: false})
	_, err = svc.ListDeploymentEvents(adminCtx(), &inferv1.ListDeploymentEventsRequest{ServiceId: svcRow.ID})
	require.NoError(t, err)
}
// fakePlatformGuard answers the platform-administrator gate.
type fakePlatformGuard struct {
	platform bool
}

func (f fakePlatformGuard) RequirePlatformAdmin(context.Context) error {
	if !f.platform {
		return apierrors.New(apierrors.CodeForbidden)
	}
	return nil
}

// TestAdminRPCsPlatformAdminOnly verifies the inference fleet is a
// platform-owned asset: when the platform gate is wired an organization
// administrator cannot reach the admin RPCs at all, and a platform
// administrator proceeds past the gate.
func TestAdminRPCsPlatformAdminOnly(t *testing.T) {
	seedImageRegistry(t)
	svc, _, _ := newInferTestService(t)
	repo, err := svc.repository()
	require.NoError(t, err)
	svcRow := seedService(t, repo, "org-a", "svc-a", StateRunning, time.Now())

	svc.SetSessionUserResolver(fakeSessionUserResolver{user: "user-1"})
	svc.SetSessionOrgResolver(fakeSessionOrgResolver{org: "org-a"})
	// The org administrator would pass the org role guard; the platform
	// gate must still refuse them.
	svc.SetRoleGuard(fakeRoleGuard{allowed: true})

	svc.SetPlatformAdminGuard(fakePlatformGuard{platform: false})
	_, err = svc.ListServiceLogPods(adminCtx(), &inferv1.ListServiceLogPodsRequest{ServiceId: svcRow.ID})
	require.Error(t, err)
	assert.Equal(t, apierrors.CodeForbidden, apierrors.CodeOf(err))

	svc.SetPlatformAdminGuard(fakePlatformGuard{platform: true})
	_, err = svc.ListServiceLogPods(adminCtx(), &inferv1.ListServiceLogPodsRequest{ServiceId: svcRow.ID})
	require.Error(t, err)
	assert.NotEqual(t, apierrors.CodeForbidden, apierrors.CodeOf(err))
}
