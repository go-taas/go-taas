package fvt

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/go-taas/go-taas/services/tenancy"
)

// The user-surface my-organizations/my-projects endpoints return the
// caller's own memberships.
func TestFVTMyOrganizationsAndProjects(t *testing.T) {
	env := newRBACEnv(t)

	// The fake session resolver is u-admin. Seed its memberships.
	env.seedRBACMember(t, "org-a", "u-admin", tenancy.RoleAdmin)
	env.seedRBACMember(t, "org-b", "u-admin", tenancy.RoleMember)
	env.seedRBACProject(t, "proj-a", "org-a")
	env.seedRBACProjectMember(t, "proj-a", "u-admin", tenancy.RoleAdmin)

	// My organizations.
	code, body := env.call(t, http.MethodGet, "/api/v1/tenancy/my-organizations", nil)
	require.Equal(t, http.StatusOK, code, "body: %v", body)
	orgs := body["organizations"].([]any)
	assert.Len(t, orgs, 2)

	// My projects.
	code, body = env.call(t, http.MethodGet, "/api/v1/tenancy/my-projects", nil)
	require.Equal(t, http.StatusOK, code, "body: %v", body)
	projects := body["projects"].([]any)
	assert.Len(t, projects, 1)
	assert.Equal(t, "proj-a", projects[0].(map[string]any)["projectId"])
	assert.Equal(t, "admin", projects[0].(map[string]any)["role"])
}

// A caller with no memberships gets an empty list.
func TestFVTMyTenancyNoMemberships(t *testing.T) {
	env := newRBACEnv(t)
	// The fake session resolver is u-admin, which has no memberships.

	code, body := env.call(t, http.MethodGet, "/api/v1/tenancy/my-organizations", nil)
	require.Equal(t, http.StatusOK, code, "body: %v", body)
	assert.Len(t, body["organizations"].([]any), 0)

	code, body = env.call(t, http.MethodGet, "/api/v1/tenancy/my-projects", nil)
	require.Equal(t, http.StatusOK, code, "body: %v", body)
	assert.Len(t, body["projects"].([]any), 0)
}