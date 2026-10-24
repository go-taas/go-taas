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

// seedProject adds a project directly.
func seedProject(t *testing.T, svc *Service, projectID, orgID string) {
	t.Helper()
	repo := NewRepository(svc.repo.db.DB(context.Background()))
	require.NoError(t, repo.CreateProject(context.Background(), &Project{
		ID: projectID, OrganizationID: orgID, DisplayName: projectID, State: StateActive,
	}))
}

// seedProjectMember adds a project member directly.
func seedProjectMember(t *testing.T, svc *Service, projectID, userID, role string) {
	t.Helper()
	repo := NewMembershipRepository(svc.repo.db.DB(context.Background()))
	require.NoError(t, repo.AddProjectMember(context.Background(), &ProjectMember{
		ProjectID: projectID, UserID: userID, Role: role, JoinedAt: time.Now().UTC(),
	}))
}

func TestServiceAddProjectMember(t *testing.T) {
	svc := newMembershipService(t)
	ctx := context.Background()

	// The caller (u-admin) is an org admin; the project belongs to org-a.
	seedMember(t, svc, "org-a", "u-admin", RoleAdmin)
	seedProject(t, svc, "proj-a", "org-a")

	resp, err := svc.AddProjectMember(ctx, &tenancyv1.AddProjectMemberRequest{
		ProjectId: "proj-a", UserId: "u-1", Role: RoleMember,
	})
	require.NoError(t, err)
	assert.Equal(t, "u-1", resp.Member.UserId)
	assert.Equal(t, RoleMember, resp.Member.Role)

	// Duplicate → 10029.
	_, err = svc.AddProjectMember(ctx, &tenancyv1.AddProjectMemberRequest{
		ProjectId: "proj-a", UserId: "u-1", Role: RoleAdmin,
	})
	assert.EqualValues(t, apierrors.CodeMemberExists, apierrors.CodeOf(err))

	// Invalid role → 10031.
	_, err = svc.AddProjectMember(ctx, &tenancyv1.AddProjectMemberRequest{
		ProjectId: "proj-a", UserId: "u-2", Role: "owner",
	})
	assert.EqualValues(t, apierrors.CodeRoleInvalid, apierrors.CodeOf(err))

	// Unknown project → 10006.
	_, err = svc.AddProjectMember(ctx, &tenancyv1.AddProjectMemberRequest{
		ProjectId: "proj-missing", UserId: "u-2", Role: RoleMember,
	})
	assert.EqualValues(t, apierrors.CodeProjectNotFound, apierrors.CodeOf(err))
}

func TestServiceListProjectMembers(t *testing.T) {
	svc := newMembershipService(t)
	ctx := context.Background()

	seedMember(t, svc, "org-a", "u-admin", RoleAdmin)
	seedProject(t, svc, "proj-a", "org-a")
	seedProjectMember(t, svc, "proj-a", "u-1", RoleMember)
	seedProjectMember(t, svc, "proj-a", "u-2", RoleViewer)

	resp, err := svc.ListProjectMembers(ctx, &tenancyv1.ListProjectMembersRequest{ProjectId: "proj-a"})
	require.NoError(t, err)
	assert.EqualValues(t, 2, resp.PageMeta.Total)
	assert.Len(t, resp.Members, 2)
}

func TestServiceSetProjectMemberRole(t *testing.T) {
	svc := newMembershipService(t)
	ctx := context.Background()

	seedMember(t, svc, "org-a", "u-admin", RoleAdmin)
	seedProject(t, svc, "proj-a", "org-a")
	seedProjectMember(t, svc, "proj-a", "u-1", RoleMember)

	resp, err := svc.SetProjectMemberRole(ctx, &tenancyv1.SetProjectMemberRoleRequest{
		ProjectId: "proj-a", UserId: "u-1", Role: RoleAdmin,
	})
	require.NoError(t, err)
	assert.Equal(t, RoleAdmin, resp.Member.Role)

	// Unknown member → 10030.
	_, err = svc.SetProjectMemberRole(ctx, &tenancyv1.SetProjectMemberRoleRequest{
		ProjectId: "proj-a", UserId: "u-missing", Role: RoleAdmin,
	})
	assert.EqualValues(t, apierrors.CodeMemberNotFound, apierrors.CodeOf(err))
}

func TestServiceRemoveProjectMember(t *testing.T) {
	svc := newMembershipService(t)
	ctx := context.Background()

	seedMember(t, svc, "org-a", "u-admin", RoleAdmin)
	seedProject(t, svc, "proj-a", "org-a")
	seedProjectMember(t, svc, "proj-a", "u-1", RoleMember)

	_, err := svc.RemoveProjectMember(ctx, &tenancyv1.RemoveProjectMemberRequest{
		ProjectId: "proj-a", UserId: "u-1",
	})
	require.NoError(t, err)

	// Unknown member → 10030.
	_, err = svc.RemoveProjectMember(ctx, &tenancyv1.RemoveProjectMemberRequest{
		ProjectId: "proj-a", UserId: "u-1",
	})
	assert.EqualValues(t, apierrors.CodeMemberNotFound, apierrors.CodeOf(err))
}

func TestServiceProjectMemberForbidden(t *testing.T) {
	svc := newMembershipService(t)
	ctx := context.Background()

	// The caller is only a member (not admin/owner) in org-a.
	seedMember(t, svc, "org-a", "u-admin", RoleMember)
	seedProject(t, svc, "proj-a", "org-a")

	_, err := svc.AddProjectMember(ctx, &tenancyv1.AddProjectMemberRequest{
		ProjectId: "proj-a", UserId: "u-1", Role: RoleMember,
	})
	assert.EqualValues(t, apierrors.CodeForbidden, apierrors.CodeOf(err))
}

// fakeUserResolver resolves a username to a canonical id.
type fakeUserResolver struct{}

func (fakeUserResolver) ResolveUserID(_ context.Context, idOrUsername string) (string, error) {
	if idOrUsername == "alice" {
		return "user-1", nil
	}
	if idOrUsername == "user-1" {
		return "user-1", nil
	}
	return "", apierrors.New(apierrors.CodeUserNotFound)
}

// A username is resolved to the canonical user id before insert.
func TestServiceAddProjectMemberResolvesUsername(t *testing.T) {
	svc := newMembershipService(t)
	svc.SetUserResolver(fakeUserResolver{})
	ctx := context.Background()

	seedMember(t, svc, "org-a", "u-admin", RoleAdmin)
	seedProject(t, svc, "proj-a", "org-a")

	// Add by username.
	resp, err := svc.AddProjectMember(ctx, &tenancyv1.AddProjectMemberRequest{
		ProjectId: "proj-a", UserId: "alice", Role: RoleMember,
	})
	require.NoError(t, err)
	assert.Equal(t, "user-1", resp.Member.UserId)

	// Unknown username → 10003.
	_, err = svc.AddProjectMember(ctx, &tenancyv1.AddProjectMemberRequest{
		ProjectId: "proj-a", UserId: "nobody", Role: RoleMember,
	})
	assert.EqualValues(t, apierrors.CodeUserNotFound, apierrors.CodeOf(err))
}