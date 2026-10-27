package fvt

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/go-taas/go-taas/services/auth"
	"github.com/go-taas/go-taas/services/tenancy"
)

// TestFVTUserDirectoryAffiliation verifies the user directory shows where
// an account belongs - the organization and project display names - or
// nothing when it belongs to neither.
func TestFVTUserDirectoryAffiliation(t *testing.T) {
	env := newFVTEnv(t)

	require.NoError(t, env.db.Create(&tenancy.Project{
		ID: "proj-fvt", OrganizationID: "org-fvt", DisplayName: "Platform Core", State: tenancy.StateActive,
	}).Error)
	userID := "440722d6-4847-40b6-abb6-db12fc41e1a1"
	require.NoError(t, env.db.Create(&auth.User{
		ID: userID, Username: "alice", Email: "alice@x.com", State: auth.UserStateActive,
	}).Error)

	// Nobody yet: both columns are empty, which the console renders as "-".
	code, body := env.call(t, http.MethodGet, "/api/v1/admin/auth/users/"+userID, nil, "")
	require.Equal(t, http.StatusOK, code, "body: %v", body)
	user := body["user"].(map[string]any)
	assert.Empty(t, user["organizationName"])
	assert.Empty(t, user["projectName"])

	// Wire the tenancy resolver and join the account to an organization and
	// a project.
	env.authSvc.SetMembershipResolver(tenancy.NewMembershipResolver(env.db))
	require.NoError(t, env.db.Create(&tenancy.OrgMember{
		OrganizationID: "org-fvt", UserID: userID, Role: tenancy.RoleMember,
	}).Error)
	require.NoError(t, env.db.Create(&tenancy.ProjectMember{
		ProjectID: "proj-fvt", UserID: userID, Role: tenancy.RoleMember,
	}).Error)

	code, body = env.call(t, http.MethodGet, "/api/v1/admin/auth/users/"+userID, nil, "")
	require.Equal(t, http.StatusOK, code, "body: %v", body)
	user = body["user"].(map[string]any)
	assert.Equal(t, "org-fvt", user["organizationId"])
	assert.Equal(t, "org-fvt", user["organizationName"])
	assert.Equal(t, "proj-fvt", user["projectId"])
	assert.Equal(t, "Platform Core", user["projectName"])
}

// TestFVTOrganizationSummaryCounts verifies the organization row reports
// the projects and members it carries, and carries no API-key or
// inference-service counters: those are platform-owned assets, not tenant
// metrics.
func TestFVTOrganizationSummaryCounts(t *testing.T) {
	env := newRBACEnv(t)
	env.tenancySvc.SetSessionRolesResolver(fakeRBACRoles{roles: []string{tenancy.RolePlatformAdmin}})
	env.seedRBACOrg(t, "org-count", "Counted")
	env.seedRBACProject(t, "proj-count", "org-count")
	env.seedRBACMember(t, "org-count", "u-1", tenancy.RoleMember)
	env.seedRBACMember(t, "org-count", "u-2", tenancy.RoleAdmin)

	code, body := env.call(t, http.MethodGet,
		"/api/v1/admin/tenancy/organizations/org-count", nil)
	require.Equal(t, http.StatusOK, code, "body: %v", body)
	org := body["organization"].(map[string]any)
	assert.Equal(t, "1", org["projectCount"])
	assert.Equal(t, "2", org["memberCount"])
	assert.NotContains(t, org, "apiKeyCount")
	assert.NotContains(t, org, "inferenceServiceCount")
}
