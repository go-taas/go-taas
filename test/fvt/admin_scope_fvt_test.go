package fvt

import (
	"context"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/go-taas/go-taas/services/tenancy"
)

// fakeRBACRoles is a SessionRolesResolver returning a fixed role set, so
// the platform-scope detection can be exercised without a real session.
type fakeRBACRoles struct{ roles []string }

func (f fakeRBACRoles) SessionRoles(context.Context) ([]string, error) { return f.roles, nil }

// seedRBACOrg inserts an active organization row directly.
func (e *rbacEnv) seedRBACOrg(t *testing.T, orgID, name string) {
	t.Helper()
	require.NoError(t, e.db.Create(&tenancy.Organization{
		ID: orgID, DisplayName: name, State: tenancy.StateActive,
	}).Error)
}

// TestFVTMyAdminScopes verifies the console scope chooser sees the
// platform, organization and project scopes the caller administers
// (requirement 6).
func TestFVTMyAdminScopes(t *testing.T) {
	env := newRBACEnv(t)
	env.tenancySvc.SetSessionRolesResolver(fakeRBACRoles{roles: []string{tenancy.RolePlatformAdmin}})
	env.seedRBACOrg(t, "org-a", "Acme")
	env.seedRBACMember(t, "org-a", "u-admin", tenancy.RoleOwner)
	env.seedRBACProject(t, "proj-a", "org-a")
	env.seedRBACProjectMember(t, "proj-a", "u-admin", tenancy.RoleAdmin)

	code, body := env.call(t, http.MethodGet, "/api/v1/tenancy/my-admin-scopes", nil)
	require.Equal(t, http.StatusOK, code, "body: %v", body)
	scopes, _ := body["scopes"].([]any)
	require.Len(t, scopes, 3)

	seen := map[string]map[string]any{}
	for _, raw := range scopes {
		s := raw.(map[string]any)
		seen[s["scopeType"].(string)] = s
	}
	require.Contains(t, seen, "platform")
	require.Contains(t, seen, "organization")
	require.Contains(t, seen, "project")
	assert.Equal(t, "org-a", seen["organization"]["organizationId"])
	assert.Equal(t, "Acme", seen["organization"]["displayName"])
	assert.Equal(t, "proj-a", seen["project"]["projectId"])
	assert.Equal(t, "org-a", seen["project"]["organizationId"])
}

// TestFVTJoinableLists verifies the join pick-lists (requirement 5): a
// user with no organization sees the joinable organizations, and once a
// member sees none; the project list is scoped to the organization.
func TestFVTJoinableLists(t *testing.T) {
	env := newRBACEnv(t)
	env.seedRBACOrg(t, "org-a", "Acme")
	env.seedRBACOrg(t, "org-b", "Beta")
	env.seedRBACProject(t, "proj-a", "org-a")
	env.seedRBACProject(t, "proj-b", "org-a")
	env.tenancySvc.SetSessionResolver(&fakeRBACSession{userID: "u-new", email: "new@x.com"})

	code, body := env.call(t, http.MethodGet, "/api/v1/tenancy/joinable-organizations", nil)
	require.Equal(t, http.StatusOK, code, "body: %v", body)
	assert.Len(t, body["organizations"].([]any), 2)

	// Joining an organization removes the joinable list (one-org rule).
	env.seedRBACMember(t, "org-a", "u-new", tenancy.RoleMember)
	code, body = env.call(t, http.MethodGet, "/api/v1/tenancy/joinable-organizations", nil)
	require.Equal(t, http.StatusOK, code, "body: %v", body)
	assert.Len(t, body["organizations"].([]any), 0)

	// Joinable projects are the organization's active projects.
	code, body = env.call(t, http.MethodGet, "/api/v1/tenancy/joinable-projects?organizationId=org-a", nil)
	require.Equal(t, http.StatusOK, code, "body: %v", body)
	projects, _ := body["projects"].([]any)
	require.Len(t, projects, 2)
	for _, raw := range projects {
		assert.Equal(t, "org-a", raw.(map[string]any)["organizationId"])
	}

	// A project member sees no joinable projects.
	env.seedRBACProjectMember(t, "proj-a", "u-new", tenancy.RoleMember)
	code, body = env.call(t, http.MethodGet, "/api/v1/tenancy/joinable-projects?organizationId=org-a", nil)
	require.Equal(t, http.StatusOK, code, "body: %v", body)
	assert.Len(t, body["projects"].([]any), 0)
}

// TestFVTOneProjectRule verifies a user cannot join a second project
// (requirement 5).
func TestFVTOneProjectRule(t *testing.T) {
	env := newRBACEnv(t)
	env.seedRBACOrg(t, "org-a", "Acme")
	env.seedRBACProject(t, "proj-a", "org-a")
	env.seedRBACProject(t, "proj-b", "org-a")
	env.seedRBACMember(t, "org-a", "u-new", tenancy.RoleMember)
	env.seedRBACProjectMember(t, "proj-a", "u-new", tenancy.RoleMember)
	env.tenancySvc.SetSessionResolver(&fakeRBACSession{userID: "u-new", email: "new@x.com"})

	_, body := env.call(t, http.MethodPost, "/api/v1/tenancy/join-requests", map[string]any{
		"targetType": "project", "targetId": "proj-b",
	})
	assert.Equal(t, float64(10029), body["code"], "body: %v", body)
}

// TestFVTPlatformOrgNotJoinable verifies the dedicated platform
// organization is excluded from the joinable list.
func TestFVTPlatformOrgNotJoinable(t *testing.T) {
	env := newRBACEnv(t)
	env.seedRBACOrg(t, "platform", "Platform")
	env.seedRBACOrg(t, "org-a", "Acme")
	env.tenancySvc.SetSessionResolver(&fakeRBACSession{userID: "u-new", email: "new@x.com"})

	code, body := env.call(t, http.MethodGet, "/api/v1/tenancy/joinable-organizations", nil)
	require.Equal(t, http.StatusOK, code, "body: %v", body)
	orgs := body["organizations"].([]any)
	require.Len(t, orgs, 1)
	assert.Equal(t, "org-a", orgs[0].(map[string]any)["organizationId"])

	// Applying to the platform org is rejected as not-found.
	_, body = env.call(t, http.MethodPost, "/api/v1/tenancy/join-requests", map[string]any{
		"targetType": "organization", "targetId": "platform",
	})
	assert.Equal(t, float64(10005), body["code"], "body: %v", body)
}

// TestFVTOrgMetadataAuthz verifies a plain org member cannot edit the
// organization metadata (requirement 3). The allowed path is covered by
// the unit tests, because the success response summarizes live resource
// counts that this tenancy-only fixture does not provision.
func TestFVTOrgMetadataAuthz(t *testing.T) {
	env := newRBACEnv(t)
	// Non-nil roles resolver without platform-admin so the platform
	// bypass is not taken.
	env.tenancySvc.SetSessionRolesResolver(fakeRBACRoles{})
	env.seedRBACOrg(t, "org-a", "Acme")
	env.seedRBACMember(t, "org-a", "u-member", tenancy.RoleMember)
	env.tenancySvc.SetSessionResolver(&fakeRBACSession{userID: "u-member", email: "m@x.com"})

	_, body := env.call(t, http.MethodPatch,
		"/api/v1/admin/tenancy/organizations/org-a",
		map[string]any{"displayName": "Hijacked"})
	assert.Equal(t, float64(10036), body["code"], "body: %v", body)
}

// TestFVTProjectWriteAuthz verifies project writes require organization
// administration: platform admins have no project management
// (requirement 2), and a plain member is forbidden (requirement 3).
func TestFVTProjectWriteAuthz(t *testing.T) {
	env := newRBACEnv(t)
	env.tenancySvc.SetSessionRolesResolver(fakeRBACRoles{})
	env.seedRBACOrg(t, "org-a", "Acme")
	env.seedRBACMember(t, "org-a", "u-admin", tenancy.RoleOwner)
	env.seedRBACMember(t, "org-a", "u-member", tenancy.RoleMember)

	// Owner creates a project.
	code, body := env.call(t, http.MethodPost, "/api/v1/admin/tenancy/projects", map[string]any{
		"projectId": "proj-new", "organizationId": "org-a", "displayName": "New",
	})
	require.Equal(t, http.StatusOK, code, "body: %v", body)

	// A plain member cannot create one.
	env.tenancySvc.SetSessionResolver(&fakeRBACSession{userID: "u-member", email: "m@x.com"})
	_, body = env.call(t, http.MethodPost, "/api/v1/admin/tenancy/projects", map[string]any{
		"projectId": "proj-bad", "organizationId": "org-a", "displayName": "Bad",
	})
	assert.Equal(t, float64(10036), body["code"], "body: %v", body)

	// A plain member cannot disable one either.
	_, body = env.call(t, http.MethodPost, "/api/v1/admin/tenancy/projects/proj-new:disable", nil)
	assert.Equal(t, float64(10036), body["code"], "body: %v", body)

	// A platform administrator (no org membership) cannot manage the
	// project.
	env.tenancySvc.SetSessionRolesResolver(fakeRBACRoles{roles: []string{tenancy.RolePlatformAdmin}})
	env.tenancySvc.SetSessionResolver(&fakeRBACSession{userID: "u-platform", email: "p@x.com"})
	_, body = env.call(t, http.MethodPost, "/api/v1/admin/tenancy/projects/proj-new:disable", nil)
	assert.Equal(t, float64(10036), body["code"], "body: %v", body)
}

// TestFVTDeleteOrganization verifies the platform can delete an
// organization with its projects and rosters, the platform organization
// is protected, and a non-platform admin is refused.
func TestFVTDeleteOrganization(t *testing.T) {
	env := newRBACEnv(t)
	env.tenancySvc.SetSessionRolesResolver(fakeRBACRoles{roles: []string{tenancy.RolePlatformAdmin}})
	env.seedRBACOrg(t, "org-del", "Deletable")
	env.seedRBACOrg(t, "platform", "Platform")
	env.seedRBACProject(t, "proj-del", "org-del")

	code, body := env.call(t, http.MethodDelete,
		"/api/v1/admin/tenancy/organizations/org-del", nil)
	require.Equal(t, http.StatusOK, code, "body: %v", body)

	// Listing no longer contains it.
	_, body = env.call(t, http.MethodGet, "/api/v1/admin/tenancy/organizations", nil)
	for _, raw := range body["organizations"].([]any) {
		assert.NotEqual(t, "org-del", raw.(map[string]any)["organizationId"])
	}

	// The platform organization cannot be deleted, even with force, and
	// reports a dedicated code so the console can explain why.
	_, body = env.call(t, http.MethodDelete,
		"/api/v1/admin/tenancy/organizations/platform", nil)
	assert.Equal(t, float64(10042), body["code"], "body: %v", body)
	_, body = env.call(t, http.MethodDelete,
		"/api/v1/admin/tenancy/organizations/platform?force=true", nil)
	assert.Equal(t, float64(10042), body["code"], "body: %v", body)

	// A plain org admin cannot delete organizations.
	env.tenancySvc.SetSessionRolesResolver(fakeRBACRoles{roles: []string{tenancy.RoleAdmin}})
	env.seedRBACOrg(t, "org-keep", "Keep")
	_, body = env.call(t, http.MethodDelete,
		"/api/v1/admin/tenancy/organizations/org-keep", nil)
	assert.Equal(t, float64(10036), body["code"], "body: %v", body)
}

// TestFVTForceDeleteOrganization verifies force removes an organization
// that still owns API keys and inference services, while the plain delete
// refuses it.
func TestFVTForceDeleteOrganization(t *testing.T) {
	env := newRBACEnv(t)
	env.tenancySvc.SetSessionRolesResolver(fakeRBACRoles{roles: []string{tenancy.RolePlatformAdmin}})
	env.seedRBACOrg(t, "org-force", "Force Me")
	env.seedRBACProject(t, "proj-force", "org-force")
	require.NoError(t, env.db.Exec(
		`INSERT INTO api_keys (id, organization_id, revoked) VALUES ('k-force','org-force',0)`).Error)
	require.NoError(t, env.db.Exec(
		`INSERT INTO inference_services (id, organization_id, state) VALUES ('s-force','org-force','running')`).Error)

	// Without force: refused with 10041.
	_, body := env.call(t, http.MethodDelete,
		"/api/v1/admin/tenancy/organizations/org-force", nil)
	assert.EqualValues(t, float64(10041), body["code"], "body: %v", body)

	// With force: deleted, assets included.
	code, body := env.call(t, http.MethodDelete,
		"/api/v1/admin/tenancy/organizations/org-force?force=true", nil)
	require.Equal(t, http.StatusOK, code, "body: %v", body)

	var keys, svcs, projects int64
	require.NoError(t, env.db.Table("api_keys").Where("organization_id = ?", "org-force").Count(&keys).Error)
	require.NoError(t, env.db.Table("inference_services").Where("organization_id = ?", "org-force").Count(&svcs).Error)
	require.NoError(t, env.db.Table("projects").Where("organization_id = ?", "org-force").Count(&projects).Error)
	assert.Zero(t, keys)
	assert.Zero(t, svcs)
	assert.Zero(t, projects)
}
