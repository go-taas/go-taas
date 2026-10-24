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

func TestServiceGetMyOrganizations(t *testing.T) {
	svc := newMembershipService(t)
	ctx := context.Background()

	seedMember(t, svc, "org-a", "u-admin", RoleAdmin)
	seedMember(t, svc, "org-b", "u-admin", RoleMember)

	resp, err := svc.GetMyOrganizations(ctx, &tenancyv1.GetMyOrganizationsRequest{})
	require.NoError(t, err)
	assert.Len(t, resp.Organizations, 2)

	roles := map[string]string{}
	for _, o := range resp.Organizations {
		roles[o.OrganizationId] = o.Role
	}
	assert.Equal(t, RoleAdmin, roles["org-a"])
	assert.Equal(t, RoleMember, roles["org-b"])
}

func TestServiceGetMyProjects(t *testing.T) {
	svc := newMembershipService(t)
	ctx := context.Background()

	seedProject(t, svc, "proj-a", "org-a")
	seedProject(t, svc, "proj-b", "org-a")
	seedProjectMember(t, svc, "proj-a", "u-admin", RoleAdmin)
	seedProjectMember(t, svc, "proj-b", "u-admin", RoleViewer)

	resp, err := svc.GetMyProjects(ctx, &tenancyv1.GetMyProjectsRequest{})
	require.NoError(t, err)
	assert.Len(t, resp.Projects, 2)

	roles := map[string]string{}
	for _, p := range resp.Projects {
		roles[p.ProjectId] = p.Role
	}
	assert.Equal(t, RoleAdmin, roles["proj-a"])
	assert.Equal(t, RoleViewer, roles["proj-b"])
}

func TestServiceGetMyOrganizationsNoSession(t *testing.T) {
	svc := newMembershipService(t)
	// Clear the session resolver so session resolution fails.
	svc.SetSessionResolver(nil)
	ctx := context.Background()

	_, err := svc.GetMyOrganizations(ctx, &tenancyv1.GetMyOrganizationsRequest{})
	assert.EqualValues(t, apierrors.CodeSessionInvalid, apierrors.CodeOf(err))

	_, err = svc.GetMyProjects(ctx, &tenancyv1.GetMyProjectsRequest{})
	assert.EqualValues(t, apierrors.CodeSessionInvalid, apierrors.CodeOf(err))
}

func TestMembershipListMemberProjects(t *testing.T) {
	db := newTestDB(t)
	repo := NewMembershipRepository(db)
	ctx := context.Background()

	require.NoError(t, repo.AddProjectMember(ctx, &ProjectMember{ProjectID: "proj-a", UserID: "u-1", Role: RoleMember, JoinedAt: time.Now().UTC()}))
	require.NoError(t, repo.AddProjectMember(ctx, &ProjectMember{ProjectID: "proj-b", UserID: "u-1", Role: RoleViewer, JoinedAt: time.Now().UTC()}))

	rows, err := repo.ListMemberProjects(ctx, "u-1")
	require.NoError(t, err)
	assert.Len(t, rows, 2)

	// A user with no memberships returns an empty slice.
	rows, err = repo.ListMemberProjects(ctx, "u-nobody")
	require.NoError(t, err)
	assert.Len(t, rows, 0)
}