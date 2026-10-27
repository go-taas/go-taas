package tenancy

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

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

func TestRequireOrgAdminOrPlatform(t *testing.T) {
	ctx := context.Background()
	svc := newMembershipService(t)
	seedMember(t, svc, "org-a", "u-admin", RoleOwner)

	// Platform administrator bypasses the org-membership check.
	svc.SetSessionRolesResolver(&fakeRolesResolver{roles: []string{RolePlatformAdmin}})
	require.NoError(t, svc.requireOrgAdminOrPlatform(ctx, "org-a"))
	require.NoError(t, svc.requireOrgAdminOrPlatform(ctx, "org-unrelated"))

	// An org owner is allowed.
	svc.SetSessionRolesResolver(&fakeRolesResolver{})
	require.NoError(t, svc.requireOrgAdminOrPlatform(ctx, "org-a"))

	// A plain member is forbidden.
	svc.SetSessionResolver(&fakeSessionResolver{userID: "u-member", email: "m@x.com"})
	seedMember(t, svc, "org-a", "u-member", RoleMember)
	err := svc.requireOrgAdminOrPlatform(ctx, "org-a")
	require.Error(t, err)
	assert.Equal(t, apierrors.CodeForbidden, apierrors.CodeOf(err))
}

func TestRequireOrgAdminWrite(t *testing.T) {
	ctx := context.Background()
	svc := newMembershipService(t)
	seedMember(t, svc, "org-a", "u-admin", RoleAdmin)
	require.NoError(t, svc.requireOrgAdminWrite(ctx, "org-a"))

	svc.SetSessionResolver(&fakeSessionResolver{userID: "u-member", email: "m@x.com"})
	seedMember(t, svc, "org-a", "u-member", RoleMember)
	err := svc.requireOrgAdminWrite(ctx, "org-a")
	require.Error(t, err)
	assert.Equal(t, apierrors.CodeForbidden, apierrors.CodeOf(err))

	// A nil role guard is permissive (unit-test convention).
	svc.SetRoleGuard(nil)
	require.NoError(t, svc.requireOrgAdminWrite(ctx, "org-a"))
}


// ensureCountTables creates the minimal platform-asset tables the
// organization summary counts. Real deployments get them from the
// auth/infer modules; the tenancy-only fixture does not.
func ensureCountTables(t *testing.T, svc *Service) {
	t.Helper()
	db := svc.repo.db.DB(context.Background())
	require.NoError(t, db.Exec(`CREATE TABLE IF NOT EXISTS api_keys (
		id TEXT PRIMARY KEY, organization_id TEXT, revoked BOOLEAN DEFAULT 0)`).Error)
	require.NoError(t, db.Exec(`CREATE TABLE IF NOT EXISTS inference_services (
		id TEXT PRIMARY KEY, organization_id TEXT, state TEXT)`).Error)
}

// TestCreateOrganizationGeneratesUUID verifies an empty id asks the
// server for a UUID (the console never asks a person to type one).
func TestCreateOrganizationGeneratesUUID(t *testing.T) {
	svc := newMembershipService(t)
	ensureCountTables(t, svc)
	svc.SetSessionRolesResolver(&fakeRolesResolver{roles: []string{RolePlatformAdmin}})

	resp, err := svc.CreateOrganization(context.Background(), &tenancyv1.CreateOrganizationRequest{
		DisplayName: "Acme",
	})
	require.NoError(t, err)
	id := resp.GetOrganization().GetOrganizationId()
	require.NotEmpty(t, id)
	assert.Regexp(t, `^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`, id)
	assert.Equal(t, "Acme", resp.GetOrganization().GetDisplayName())

	// A caller-supplied id is still honoured for scripts.
	resp, err = svc.CreateOrganization(context.Background(), &tenancyv1.CreateOrganizationRequest{
		OrganizationId: "script-org", DisplayName: "Scripted",
	})
	require.NoError(t, err)
	assert.Equal(t, "script-org", resp.GetOrganization().GetOrganizationId())
}

// TestCreateProjectGeneratesUUID verifies project ids are generated too.
func TestCreateProjectGeneratesUUID(t *testing.T) {
	svc := newMembershipService(t)
	seedOrg(t, svc, "org-a")
	seedMember(t, svc, "org-a", "u-admin", RoleOwner)

	resp, err := svc.CreateProject(context.Background(), &tenancyv1.CreateProjectRequest{
		OrganizationId: "org-a", DisplayName: "Proj",
	})
	require.NoError(t, err)
	assert.Regexp(t, `^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`,
		resp.GetProject().GetProjectId())
}

// TestDeleteOrganization verifies the delete removes the organization and
// every tenancy-side row it owns, and refuses the platform organization.
func TestDeleteOrganization(t *testing.T) {
	svc := newMembershipService(t)
	ensureCountTables(t, svc)
	svc.SetSessionRolesResolver(&fakeRolesResolver{roles: []string{RolePlatformAdmin}})
	ctx := context.Background()

	seedOrg(t, svc, "org-a")
	seedOrg(t, svc, "org-b")
	seedMember(t, svc, "org-a", "u-admin", RoleOwner)
	seedProject(t, svc, "proj-a", "org-a")
	seedProjectMember(t, svc, "proj-a", "u-admin", RoleAdmin)
	seedJoinRequest(t, svc, "u-new", JoinTargetOrganization, "org-a")
	seedJoinRequest(t, svc, "u-new", JoinTargetProject, "proj-a")

	_, err := svc.DeleteOrganization(ctx, &tenancyv1.DeleteOrganizationRequest{OrganizationId: "org-a"})
	require.NoError(t, err)

	repo := NewRepository(svc.repo.db.DB(ctx))
	_, err = repo.FindOrganization(ctx, "org-a")
	assert.Error(t, err)
	if _, err := repo.FindProject(ctx, "proj-a"); err == nil {
		t.Fatal("project should be deleted with the organization")
	}
	// The sibling organization is untouched.
	if _, err := repo.FindOrganization(ctx, "org-b"); err != nil {
		t.Fatalf("unrelated organization must survive: %v", err)
	}

	// Unknown organization -> 10005.
	_, err = svc.DeleteOrganization(ctx, &tenancyv1.DeleteOrganizationRequest{OrganizationId: "nope"})
	require.Error(t, err)
	assert.Equal(t, apierrors.CodeOrganizationNotFound, apierrors.CodeOf(err))

	// The dedicated platform organization is protected, even with force,
	// and reports a dedicated error rather than a generic validation one.
	seedOrg(t, svc, "platform")
	_, err = svc.DeleteOrganization(ctx, &tenancyv1.DeleteOrganizationRequest{OrganizationId: "platform"})
	require.Error(t, err)
	assert.Equal(t, apierrors.CodeOrganizationProtected, apierrors.CodeOf(err))
	_, err = svc.DeleteOrganization(ctx, &tenancyv1.DeleteOrganizationRequest{
		OrganizationId: "platform", Force: true,
	})
	require.Error(t, err)
	assert.Equal(t, apierrors.CodeOrganizationProtected, apierrors.CodeOf(err))
}

// TestDeleteOrganizationForbidden verifies only a platform administrator
// may delete an organization.
func TestDeleteOrganizationForbidden(t *testing.T) {
	svc := newMembershipService(t)
	svc.SetSessionRolesResolver(&fakeRolesResolver{roles: []string{RoleAdmin}})
	seedOrg(t, svc, "org-a")
	seedMember(t, svc, "org-a", "u-admin", RoleOwner)

	_, err := svc.DeleteOrganization(context.Background(),
		&tenancyv1.DeleteOrganizationRequest{OrganizationId: "org-a"})
	require.Error(t, err)
	assert.Equal(t, apierrors.CodeForbidden, apierrors.CodeOf(err))
}

// TestDeleteOrganizationInUse verifies an organization that still owns
// platform assets is not deleted.
func TestDeleteOrganizationInUse(t *testing.T) {
	svc := newMembershipService(t)
	ensureCountTables(t, svc)
	svc.SetSessionRolesResolver(&fakeRolesResolver{roles: []string{RolePlatformAdmin}})
	ctx := context.Background()
	seedOrg(t, svc, "org-a")

	db := svc.repo.db.DB(ctx)
	require.NoError(t, db.Exec(
		`INSERT INTO api_keys (id, organization_id, revoked) VALUES ('k1','org-a',0)`).Error)

	_, err := svc.DeleteOrganization(ctx, &tenancyv1.DeleteOrganizationRequest{OrganizationId: "org-a"})
	require.Error(t, err)
	assert.Equal(t, apierrors.CodeOrganizationInUse, apierrors.CodeOf(err))

	// The organization survives the refused delete.
	repo := NewRepository(db)
	if _, err := repo.FindOrganization(ctx, "org-a"); err != nil {
		t.Fatalf("organization must survive: %v", err)
	}
}

// TestDeleteOrganizationEmpty verifies deleting an organization with no
// projects, members or requests (the empty-cascade path).
func TestDeleteOrganizationEmpty(t *testing.T) {
	svc := newMembershipService(t)
	ensureCountTables(t, svc)
	svc.SetSessionRolesResolver(&fakeRolesResolver{roles: []string{RolePlatformAdmin}})
	seedOrg(t, svc, "org-empty")

	_, err := svc.DeleteOrganization(context.Background(),
		&tenancyv1.DeleteOrganizationRequest{OrganizationId: "org-empty"})
	require.NoError(t, err)
	repo := NewRepository(svc.repo.db.DB(context.Background()))
	if _, err := repo.FindOrganization(context.Background(), "org-empty"); err == nil {
		t.Fatal("organization should be gone")
	}
}

// TestDeleteOrganizationInvalidID verifies an empty id is rejected.
func TestDeleteOrganizationInvalidID(t *testing.T) {
	svc := newMembershipService(t)
	svc.SetSessionRolesResolver(&fakeRolesResolver{roles: []string{RolePlatformAdmin}})

	_, err := svc.DeleteOrganization(context.Background(), &tenancyv1.DeleteOrganizationRequest{})
	require.Error(t, err)
	assert.Equal(t, apierrors.CodeTenancyInvalid, apierrors.CodeOf(err))
}

// TestRepositoryDeleteOrganizationMissing verifies a delete of an absent
// row reports not-found so the service maps it to 10005.
func TestRepositoryDeleteOrganizationMissing(t *testing.T) {
	db := newTestDB(t)
	repo := NewRepository(db)
	err := repo.DeleteOrganization(context.Background(), "00000000-0000-0000-0000-000000000000")
	require.Error(t, err)
	assert.ErrorIs(t, err, gorm.ErrRecordNotFound)
}

// TestForceDeleteOrganization verifies a force delete wipes the
// organization's platform assets and tenancy rows, where a plain delete
// refuses while assets exist.
func TestForceDeleteOrganization(t *testing.T) {
	svc := newMembershipService(t)
	ensureCountTables(t, svc)
	svc.SetSessionRolesResolver(&fakeRolesResolver{roles: []string{RolePlatformAdmin}})
	ctx := context.Background()
	seedOrg(t, svc, "org-a")
	seedMember(t, svc, "org-a", "u-admin", RoleOwner)
	seedProject(t, svc, "proj-a", "org-a")
	db := svc.repo.db.DB(ctx)
	require.NoError(t, db.Exec(
		`INSERT INTO api_keys (id, organization_id, revoked) VALUES ('k1','org-a',0)`).Error)
	require.NoError(t, db.Exec(
		`INSERT INTO inference_services (id, organization_id, state) VALUES ('s1','org-a','running')`).Error)

	// Without force the organization is refused.
	_, err := svc.DeleteOrganization(ctx, &tenancyv1.DeleteOrganizationRequest{OrganizationId: "org-a"})
	require.Error(t, err)
	assert.Equal(t, apierrors.CodeOrganizationInUse, apierrors.CodeOf(err))

	// With force it is removed together with its assets.
	_, err = svc.DeleteOrganization(ctx, &tenancyv1.DeleteOrganizationRequest{
		OrganizationId: "org-a", Force: true,
	})
	require.NoError(t, err)

	repo := NewRepository(db)
	if _, err := repo.FindOrganization(ctx, "org-a"); err == nil {
		t.Fatal("organization should be gone")
	}
	var keys, svcs int64
	require.NoError(t, db.Table("api_keys").Where("organization_id = ?", "org-a").Count(&keys).Error)
	require.NoError(t, db.Table("inference_services").Where("organization_id = ?", "org-a").Count(&svcs).Error)
	assert.Zero(t, keys, "api keys must be removed by a force delete")
	assert.Zero(t, svcs, "inference services must be removed by a force delete")
}

// TestForceDeleteOrganizationMissing verifies a force delete of an absent
// organization reports not-found.
func TestForceDeleteOrganizationMissing(t *testing.T) {
	svc := newMembershipService(t)
	svc.SetSessionRolesResolver(&fakeRolesResolver{roles: []string{RolePlatformAdmin}})

	_, err := svc.DeleteOrganization(context.Background(), &tenancyv1.DeleteOrganizationRequest{
		OrganizationId: "00000000-0000-0000-0000-000000000000", Force: true,
	})
	require.Error(t, err)
	assert.Equal(t, apierrors.CodeOrganizationNotFound, apierrors.CodeOf(err))
}

// TestPlatformAdminManagesAnyOrgRoster verifies a platform administrator
// can list and add members of an organization they do not belong to, and
// assign that organization's admin (requirements: manage users, add them
// to a chosen organization, set every organization's administrator).
func TestPlatformAdminManagesAnyOrgRoster(t *testing.T) {
	svc := newMembershipService(t)
	svc.SetSessionRolesResolver(&fakeRolesResolver{roles: []string{RolePlatformAdmin}})
	ctx := context.Background()
	seedOrg(t, svc, "org-x")
	// The caller (u-admin) has no membership in org-x.

	added, err := svc.AddOrgMember(ctx, &tenancyv1.AddOrgMemberRequest{
		OrganizationId: "org-x", UserId: "u-new", Role: RoleAdmin,
	})
	require.NoError(t, err)
	assert.Equal(t, RoleAdmin, added.GetMember().GetRole())

	list, err := svc.ListOrgMembers(ctx, &tenancyv1.ListOrgMembersRequest{OrganizationId: "org-x"})
	require.NoError(t, err)
	require.Len(t, list.Members, 1)
	assert.Equal(t, "u-new", list.Members[0].GetUserId())

	// Promoting an existing member to admin also works.
	_, err = svc.SetOrgMemberRole(ctx, &tenancyv1.SetOrgMemberRoleRequest{
		OrganizationId: "org-x", UserId: "u-new", Role: RoleAdmin,
	})
	require.NoError(t, err)
}

// TestNonPlatformCallerCannotManageForeignOrgRoster verifies the platform
// bypass does not widen a plain caller's reach: a member of org-a still
// cannot touch org-x.
func TestNonPlatformCallerCannotManageForeignOrgRoster(t *testing.T) {
	svc := newMembershipService(t)
	svc.SetSessionRolesResolver(&fakeRolesResolver{roles: []string{RoleAdmin}})
	ctx := context.Background()
	seedOrg(t, svc, "org-a")
	seedOrg(t, svc, "org-x")
	seedMember(t, svc, "org-a", "u-admin", RoleOwner)

	_, err := svc.AddOrgMember(ctx, &tenancyv1.AddOrgMemberRequest{
		OrganizationId: "org-x", UserId: "u-new", Role: RoleMember,
	})
	require.Error(t, err)
	assert.Equal(t, apierrors.CodeForbidden, apierrors.CodeOf(err))
}

// TestPlatformAdminCannotManageProjects verifies the roster bypass does not
// leak into project management: a platform administrator still has no
// authority over an organization's projects.
func TestPlatformAdminCannotManageProjects(t *testing.T) {
	svc := newMembershipService(t)
	svc.SetSessionRolesResolver(&fakeRolesResolver{roles: []string{RolePlatformAdmin}})
	ctx := context.Background()
	seedOrg(t, svc, "org-p")

	_, err := svc.CreateProject(ctx, &tenancyv1.CreateProjectRequest{
		ProjectId: "proj-p", OrganizationId: "org-p", DisplayName: "P",
	})
	require.Error(t, err)
	assert.Equal(t, apierrors.CodeForbidden, apierrors.CodeOf(err))
}
