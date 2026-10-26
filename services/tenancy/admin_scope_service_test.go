package tenancy

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	tenancyv1 "github.com/go-taas/go-taas/proto/taas/tenancy/v1"

	apierrors "github.com/go-taas/go-taas/pkg/errors"
)

// fakeRolesResolver is a test SessionRolesResolver.
type fakeRolesResolver struct{ roles []string }

func (f *fakeRolesResolver) SessionRoles(context.Context) ([]string, error) {
	return f.roles, nil
}

// seedOrgNamed inserts an active organization row with a display name.
func seedOrgNamed(t *testing.T, svc *Service, orgID, name string) {
	t.Helper()
	repo := NewRepository(svc.repo.db.DB(context.Background()))
	require.NoError(t, repo.CreateOrganization(context.Background(), &Organization{
		ID: orgID, DisplayName: name, State: StateActive,
	}))
}

func TestServiceGetMyAdminScopes(t *testing.T) {
	svc := newMembershipService(t)
	svc.SetSessionRolesResolver(&fakeRolesResolver{roles: []string{RolePlatformAdmin}})
	ctx := context.Background()

	seedOrgNamed(t, svc, "org-a", "Acme")
	seedOrgNamed(t, svc, "org-b", "Beta")
	seedMember(t, svc, "org-a", "u-admin", RoleOwner)
	seedMember(t, svc, "org-b", "u-admin", RoleMember)
	seedProject(t, svc, "proj-a", "org-a")
	seedProjectMember(t, svc, "proj-a", "u-admin", RoleAdmin)

	resp, err := svc.GetMyAdminScopes(ctx, &tenancyv1.GetMyAdminScopesRequest{})
	require.NoError(t, err)

	// Platform + org-a (owner) + proj-a (admin). org-b is member, so it
	// is not an admin scope.
	require.Len(t, resp.Scopes, 3)
	byType := map[string]*tenancyv1.AdminScope{}
	for _, s := range resp.Scopes {
		byType[s.GetScopeType()] = s
	}
	assert.Equal(t, RolePlatformAdmin, byType[ScopePlatform].GetRole())
	assert.Equal(t, "org-a", byType[ScopeOrganization].GetOrganizationId())
	assert.Equal(t, "Acme", byType[ScopeOrganization].GetDisplayName())
	assert.Equal(t, "proj-a", byType[ScopeProject].GetProjectId())
	assert.Equal(t, "org-a", byType[ScopeProject].GetOrganizationId())
}

func TestServiceGetMyAdminScopesNoPlatform(t *testing.T) {
	svc := newMembershipService(t)
	svc.SetSessionRolesResolver(&fakeRolesResolver{roles: []string{RoleAdmin}})
	seedOrgNamed(t, svc, "org-a", "Acme")
	seedMember(t, svc, "org-a", "u-admin", RoleAdmin)

	resp, err := svc.GetMyAdminScopes(context.Background(), &tenancyv1.GetMyAdminScopesRequest{})
	require.NoError(t, err)
	require.Len(t, resp.Scopes, 1)
	assert.Equal(t, ScopeOrganization, resp.Scopes[0].GetScopeType())
}

func TestServiceGetMyAdminScopesNoSession(t *testing.T) {
	svc := newMembershipService(t)
	svc.SetSessionResolver(nil)
	_, err := svc.GetMyAdminScopes(context.Background(), &tenancyv1.GetMyAdminScopesRequest{})
	require.Error(t, err)
	assert.Equal(t, apierrors.CodeSessionInvalid, apierrors.CodeOf(err))
}

func TestServiceListJoinableOrganizations(t *testing.T) {
	svc := newMembershipService(t)
	svc.SetSessionResolver(&fakeSessionResolver{userID: "u-new", email: "new@x.com"})
	ctx := context.Background()

	seedOrgNamed(t, svc, "platform", "Platform")
	seedOrgNamed(t, svc, "org-a", "Acme")
	seedOrgNamed(t, svc, "org-b", "Beta")

	resp, err := svc.ListJoinableOrganizations(ctx, &tenancyv1.ListJoinableOrganizationsRequest{})
	require.NoError(t, err)
	// The platform org is excluded; both tenant orgs are offered.
	require.Len(t, resp.Organizations, 2)

	// Once a member of an organization, no organization is joinable.
	seedMember(t, svc, "org-a", "u-new", RoleMember)
	resp, err = svc.ListJoinableOrganizations(ctx, &tenancyv1.ListJoinableOrganizationsRequest{})
	require.NoError(t, err)
	assert.Empty(t, resp.Organizations)
}

func TestServiceListJoinableProjects(t *testing.T) {
	svc := newMembershipService(t)
	svc.SetSessionResolver(&fakeSessionResolver{userID: "u-new", email: "new@x.com"})
	ctx := context.Background()

	seedOrgNamed(t, svc, "org-a", "Acme")
	seedMember(t, svc, "org-a", "u-new", RoleMember)
	seedProject(t, svc, "proj-a", "org-a")
	seedProject(t, svc, "proj-b", "org-a")

	resp, err := svc.ListJoinableProjects(ctx, &tenancyv1.ListJoinableProjectsRequest{
		OrganizationId: "org-a",
	})
	require.NoError(t, err)
	require.Len(t, resp.Projects, 2)

	// A project member sees no joinable projects.
	seedProjectMember(t, svc, "proj-a", "u-new", RoleMember)
	resp, err = svc.ListJoinableProjects(ctx, &tenancyv1.ListJoinableProjectsRequest{
		OrganizationId: "org-a",
	})
	require.NoError(t, err)
	assert.Empty(t, resp.Projects)
}

func TestServiceListJoinableProjectsResolvesOrg(t *testing.T) {
	svc := newMembershipService(t)
	svc.SetSessionResolver(&fakeSessionResolver{userID: "u-new", email: "new@x.com"})
	ctx := context.Background()

	seedOrgNamed(t, svc, "org-a", "Acme")
	seedMember(t, svc, "org-a", "u-new", RoleMember)
	seedProject(t, svc, "proj-a", "org-a")

	// No organization_id: resolved from the caller's membership.
	resp, err := svc.ListJoinableProjects(ctx, &tenancyv1.ListJoinableProjectsRequest{})
	require.NoError(t, err)
	require.Len(t, resp.Projects, 1)
	assert.Equal(t, "proj-a", resp.Projects[0].GetProjectId())
}

func TestServiceListJoinableProjectsNoOrg(t *testing.T) {
	svc := newMembershipService(t)
	svc.SetSessionResolver(&fakeSessionResolver{userID: "u-new", email: "new@x.com"})

	resp, err := svc.ListJoinableProjects(context.Background(), &tenancyv1.ListJoinableProjectsRequest{})
	require.NoError(t, err)
	assert.Empty(t, resp.Projects)
}

func TestRealOrgMemberships(t *testing.T) {
	in := []*OrgMember{
		{OrganizationID: "platform", UserID: "u", Role: RolePlatformAdmin},
		{OrganizationID: "org-a", UserID: "u", Role: RoleMember},
	}
	out := realOrgMemberships(in)
	require.Len(t, out, 1)
	assert.Equal(t, "org-a", out[0].OrganizationID)

	assert.Empty(t, realOrgMemberships(nil))
}

// seedJoinRequest inserts a pending join request directly.
func seedJoinRequest(t *testing.T, svc *Service, userID, targetType, targetID string) {
	t.Helper()
	repo := NewMembershipRepository(svc.repo.db.DB(context.Background()))
	require.NoError(t, repo.CreateJoinRequest(context.Background(), &JoinRequest{
		ID: "jr-" + targetID, UserID: userID, TargetType: targetType,
		TargetID: targetID, Status: JoinStatusPending,
	}))
}

func TestServiceListJoinableExcludesPending(t *testing.T) {
	svc := newMembershipService(t)
	svc.SetSessionResolver(&fakeSessionResolver{userID: "u-new", email: "new@x.com"})
	ctx := context.Background()

	seedOrgNamed(t, svc, "org-a", "Acme")
	seedOrgNamed(t, svc, "org-b", "Beta")
	// A pending organization request hides that organization.
	seedJoinRequest(t, svc, "u-new", JoinTargetOrganization, "org-a")

	resp, err := svc.ListJoinableOrganizations(ctx, &tenancyv1.ListJoinableOrganizationsRequest{})
	require.NoError(t, err)
	require.Len(t, resp.Organizations, 1)
	assert.Equal(t, "org-b", resp.Organizations[0].GetOrganizationId())

	seedMember(t, svc, "org-a", "u-new", RoleMember)
	seedProject(t, svc, "proj-a", "org-a")
	seedProject(t, svc, "proj-b", "org-a")
	// A pending project request hides that project.
	seedJoinRequest(t, svc, "u-new", JoinTargetProject, "proj-a")

	presp, err := svc.ListJoinableProjects(ctx, &tenancyv1.ListJoinableProjectsRequest{
		OrganizationId: "org-a",
	})
	require.NoError(t, err)
	require.Len(t, presp.Projects, 1)
	assert.Equal(t, "proj-b", presp.Projects[0].GetProjectId())
}

func TestServiceGetMyAdminScopesSkipsNonAdminProject(t *testing.T) {
	svc := newMembershipService(t)
	svc.SetSessionRolesResolver(&fakeRolesResolver{roles: []string{RoleAdmin}})
	seedOrgNamed(t, svc, "org-a", "Acme")
	seedMember(t, svc, "org-a", "u-admin", RoleAdmin)
	seedProject(t, svc, "proj-a", "org-a")
	// A plain project member is not a project-admin scope.
	seedProjectMember(t, svc, "proj-a", "u-admin", RoleMember)

	resp, err := svc.GetMyAdminScopes(context.Background(), &tenancyv1.GetMyAdminScopesRequest{})
	require.NoError(t, err)
	require.Len(t, resp.Scopes, 1)
	assert.Equal(t, ScopeOrganization, resp.Scopes[0].GetScopeType())
}
