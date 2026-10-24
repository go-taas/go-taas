package tenancy

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	tenancyv1 "github.com/go-taas/go-taas/proto/taas/tenancy/v1"

	apierrors "github.com/go-taas/go-taas/pkg/errors"
)

func TestCreateJoinRequestOrganization(t *testing.T) {
	svc := newMembershipService(t)
	ctx := context.Background()
	seedOrg(t, svc, "org-a")

	resp, err := svc.CreateJoinRequest(ctx, &tenancyv1.CreateJoinRequestRequest{
		TargetType: JoinTargetOrganization, TargetId: "org-a", Message: "please",
	})
	require.NoError(t, err)
	assert.Equal(t, JoinStatusPending, resp.Request.Status)

	// Duplicate pending → 10029.
	_, err = svc.CreateJoinRequest(ctx, &tenancyv1.CreateJoinRequestRequest{
		TargetType: JoinTargetOrganization, TargetId: "org-a",
	})
	assert.EqualValues(t, apierrors.CodeMemberExists, apierrors.CodeOf(err))
}

func TestCreateJoinRequestOneOrgRule(t *testing.T) {
	svc := newMembershipService(t)
	ctx := context.Background()
	seedOrg(t, svc, "org-a")
	seedOrg(t, svc, "org-b")
	// The caller (u-admin) is already a member of org-a.
	seedMember(t, svc, "org-a", "u-admin", RoleMember)

	_, err := svc.CreateJoinRequest(ctx, &tenancyv1.CreateJoinRequestRequest{
		TargetType: JoinTargetOrganization, TargetId: "org-b",
	})
	assert.EqualValues(t, apierrors.CodeMemberExists, apierrors.CodeOf(err),
		"a user may belong to exactly one organization")
}

func TestCreateJoinRequestProjectRequiresOrgMembership(t *testing.T) {
	svc := newMembershipService(t)
	ctx := context.Background()
	seedOrg(t, svc, "org-a")
	seedProject(t, svc, "proj-a", "org-a")

	// No org membership → forbidden.
	_, err := svc.CreateJoinRequest(ctx, &tenancyv1.CreateJoinRequestRequest{
		TargetType: JoinTargetProject, TargetId: "proj-a",
	})
	assert.EqualValues(t, apierrors.CodeForbidden, apierrors.CodeOf(err))

	// With org membership → allowed.
	seedMember(t, svc, "org-a", "u-admin", RoleMember)
	resp, err := svc.CreateJoinRequest(ctx, &tenancyv1.CreateJoinRequestRequest{
		TargetType: JoinTargetProject, TargetId: "proj-a",
	})
	require.NoError(t, err)
	assert.Equal(t, JoinStatusPending, resp.Request.Status)
}

func TestApproveJoinRequestAddsMember(t *testing.T) {
	svc := newMembershipService(t)
	ctx := context.Background()
	seedOrg(t, svc, "org-a")
	// The caller administers org-a.
	seedMember(t, svc, "org-a", "u-admin", RoleAdmin)

	repo := NewMembershipRepository(svc.repo.db.DB(ctx))
	require.NoError(t, repo.CreateJoinRequest(ctx, &JoinRequest{
		ID: "jr-1", UserID: "u-new", TargetType: JoinTargetOrganization,
		TargetID: "org-a", Status: JoinStatusPending, CreatedAt: time.Now().UTC(),
	}))

	resp, err := svc.ApproveJoinRequest(ctx, &tenancyv1.ApproveJoinRequestRequest{RequestId: "jr-1"})
	require.NoError(t, err)
	assert.Equal(t, JoinStatusApproved, resp.Request.Status)

	// The applicant is now a member.
	m, err := repo.FindMember(ctx, "org-a", "u-new")
	require.NoError(t, err)
	require.NotNil(t, m)
	assert.Equal(t, RoleMember, m.Role)
}

func TestApproveJoinRequestRechecksOneOrgRule(t *testing.T) {
	svc := newMembershipService(t)
	ctx := context.Background()
	seedOrg(t, svc, "org-a")
	seedOrg(t, svc, "org-b")
	// The caller administers the target org (org-b).
	seedMember(t, svc, "org-b", "u-admin", RoleAdmin)

	repo := NewMembershipRepository(svc.repo.db.DB(ctx))
	require.NoError(t, repo.CreateJoinRequest(ctx, &JoinRequest{
		ID: "jr-1", UserID: "u-new", TargetType: JoinTargetOrganization,
		TargetID: "org-b", Status: JoinStatusPending,
	}))
	// The applicant joined another org (org-a) while pending.
	require.NoError(t, repo.AddMember(ctx, &OrgMember{
		OrganizationID: "org-a", UserID: "u-new", Role: RoleMember, JoinedAt: time.Now().UTC(),
	}))

	_, err := svc.ApproveJoinRequest(ctx, &tenancyv1.ApproveJoinRequestRequest{RequestId: "jr-1"})
	assert.EqualValues(t, apierrors.CodeMemberExists, apierrors.CodeOf(err))
}

func TestRejectJoinRequest(t *testing.T) {
	svc := newMembershipService(t)
	ctx := context.Background()
	seedOrg(t, svc, "org-a")
	seedMember(t, svc, "org-a", "u-admin", RoleAdmin)

	repo := NewMembershipRepository(svc.repo.db.DB(ctx))
	require.NoError(t, repo.CreateJoinRequest(ctx, &JoinRequest{
		ID: "jr-1", UserID: "u-new", TargetType: JoinTargetOrganization,
		TargetID: "org-a", Status: JoinStatusPending,
	}))

	resp, err := svc.RejectJoinRequest(ctx, &tenancyv1.RejectJoinRequestRequest{RequestId: "jr-1"})
	require.NoError(t, err)
	assert.Equal(t, JoinStatusRejected, resp.Request.Status)

	// Already decided → 10033.
	_, err = svc.RejectJoinRequest(ctx, &tenancyv1.RejectJoinRequestRequest{RequestId: "jr-1"})
	assert.EqualValues(t, apierrors.CodeInvitationNotFound, apierrors.CodeOf(err))
}

func TestProjectQuota(t *testing.T) {
	svc := newMembershipService(t)
	ctx := context.Background()
	seedOrg(t, svc, "org-a")
	seedProject(t, svc, "proj-a", "org-a")
	// The caller is a project admin (not an org admin).
	seedProjectMember(t, svc, "proj-a", "u-admin", RoleAdmin)

	limit := int64(42)
	resp, err := svc.UpdateProjectQuota(ctx, &tenancyv1.UpdateProjectQuotaRequest{
		ProjectId: "proj-a", MaxConcurrentRequests: &limit,
	})
	require.NoError(t, err)
	assert.EqualValues(t, 42, resp.Quota.MaxConcurrentRequests)

	got, err := svc.GetProjectQuota(ctx, &tenancyv1.GetProjectQuotaRequest{ProjectId: "proj-a"})
	require.NoError(t, err)
	assert.EqualValues(t, 42, got.Quota.MaxConcurrentRequests)

	// A non-admin project member is forbidden.
	seedProjectMember(t, svc, "proj-a", "u-viewer", RoleMember)
	svc.SetSessionResolver(&fakeSessionResolver{userID: "u-viewer", email: "v@x.com"})
	_, err = svc.GetProjectQuota(ctx, &tenancyv1.GetProjectQuotaRequest{ProjectId: "proj-a"})
	assert.EqualValues(t, apierrors.CodeForbidden, apierrors.CodeOf(err))
}
