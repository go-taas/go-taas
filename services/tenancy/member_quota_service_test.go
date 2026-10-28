package tenancy

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	apierrors "github.com/go-taas/go-taas/pkg/errors"
	tenancyv1 "github.com/go-taas/go-taas/proto/taas/tenancy/v1"
)

// TestServiceProjectMemberQuotas verifies a project administrator budgets
// each member separately: the list carries every member, and an update
// touches only the named one.
func TestServiceProjectMemberQuotas(t *testing.T) {
	svc := newMembershipService(t)
	ctx := context.Background()

	require.NoError(t, svc.repo.CreateOrganization(ctx, &Organization{ID: "org-q", DisplayName: "Q", State: StateActive}))
	// The caller (u-admin) administers the organization, which is what lets
	// it budget the project's members.
	seedMember(t, svc, "org-q", "u-admin", RoleAdmin)
	require.NoError(t, svc.repo.CreateProject(ctx, &Project{ID: "proj-q", OrganizationID: "org-q", DisplayName: "P", State: StateActive}))
	mrepo := NewMembershipRepository(svc.repo.db.DB(ctx))
	require.NoError(t, mrepo.AddProjectMember(ctx, &ProjectMember{ProjectID: "proj-q", UserID: "u-a", Role: RoleMember}))
	require.NoError(t, mrepo.AddProjectMember(ctx, &ProjectMember{ProjectID: "proj-q", UserID: "u-b", Role: RoleViewer}))

	// Both members start unlimited.
	list, err := svc.ListProjectMemberQuotas(ctx, &tenancyv1.ListProjectMemberQuotasRequest{ProjectId: "proj-q"})
	require.NoError(t, err)
	require.Len(t, list.Quotas, 2)
	for _, q := range list.Quotas {
		assert.EqualValues(t, 0, q.GetMaxConcurrentRequests())
	}

	// Budget one member only.
	updated, err := svc.UpdateProjectMemberQuota(ctx, &tenancyv1.UpdateProjectMemberQuotaRequest{
		ProjectId:             "proj-q",
		UserId:                "u-a",
		MaxConcurrentRequests: ptrInt64(50),
		MaxInferenceServices:  ptrInt64(2),
	})
	require.NoError(t, err)
	assert.EqualValues(t, 50, updated.Quota.GetMaxConcurrentRequests())
	assert.EqualValues(t, 2, updated.Quota.GetMaxInferenceServices())

	// The other member is untouched.
	list, err = svc.ListProjectMemberQuotas(ctx, &tenancyv1.ListProjectMemberQuotasRequest{ProjectId: "proj-q"})
	require.NoError(t, err)
	for _, q := range list.Quotas {
		if q.GetUserId() == "u-b" {
			assert.EqualValues(t, 0, q.GetMaxConcurrentRequests(), "the sibling member keeps its own quota")
		}
		if q.GetUserId() == "u-a" {
			assert.EqualValues(t, 50, q.GetMaxConcurrentRequests())
		}
	}
}

// TestServiceProjectMemberQuotaRequiresMember verifies a quota cannot be
// assigned to somebody outside the project: that would create a phantom
// roster entry.
func TestServiceProjectMemberQuotaRequiresMember(t *testing.T) {
	svc := newMembershipService(t)
	ctx := context.Background()

	require.NoError(t, svc.repo.CreateOrganization(ctx, &Organization{ID: "org-q2", DisplayName: "Q", State: StateActive}))
	seedMember(t, svc, "org-q2", "u-admin", RoleAdmin)
	require.NoError(t, svc.repo.CreateProject(ctx, &Project{ID: "proj-q2", OrganizationID: "org-q2", DisplayName: "P", State: StateActive}))

	_, err := svc.UpdateProjectMemberQuota(ctx, &tenancyv1.UpdateProjectMemberQuotaRequest{
		ProjectId: "proj-q2", UserId: "u-stranger", MaxConcurrentRequests: ptrInt64(10),
	})
	require.Error(t, err)
	assert.EqualValues(t, apierrors.CodeUserNotFound, apierrors.CodeOf(err))
}

func ptrInt64(v int64) *int64 { return &v }
