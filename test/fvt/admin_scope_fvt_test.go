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
