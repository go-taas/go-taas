package fvt

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	apierrors "github.com/go-taas/go-taas/pkg/errors"
	"github.com/go-taas/go-taas/services/tenancy"
)

// seedRBACProject adds a project directly.
func (e *rbacEnv) seedRBACProject(t *testing.T, projectID, orgID string) {
	t.Helper()
	repo := tenancy.NewRepository(e.db)
	require.NoError(t, repo.CreateProject(context.Background(), &tenancy.Project{
		ID: projectID, OrganizationID: orgID, DisplayName: projectID, State: tenancy.StateActive,
	}))
}

// seedRBACProjectMember adds a project member directly.
func (e *rbacEnv) seedRBACProjectMember(t *testing.T, projectID, userID, role string) {
	t.Helper()
	repo := tenancy.NewMembershipRepository(e.db)
	require.NoError(t, repo.AddProjectMember(context.Background(), &tenancy.ProjectMember{
		ProjectID: projectID, UserID: userID, Role: role, JoinedAt: time.Now().UTC(),
	}))
}

// Project member CRUD round-trips through the gateway.
func TestFVTProjectMemberCRUD(t *testing.T) {
	env := newRBACEnv(t)
	env.seedRBACMember(t, "org-a", "u-admin", tenancy.RoleOwner)
	env.seedRBACProject(t, "proj-a", "org-a")

	// Add.
	code, body := env.call(t, http.MethodPost, "/api/v1/admin/tenancy/projects/proj-a/members",
		map[string]any{"userId": "u-2", "role": "member"})
	require.Equal(t, http.StatusOK, code, "body: %v", body)
	assert.Equal(t, float64(0), body["response"].(map[string]any)["code"])

	// List.
	code, body = env.call(t, http.MethodGet, "/api/v1/admin/tenancy/projects/proj-a/members", nil)
	require.Equal(t, http.StatusOK, code, "body: %v", body)
	members := body["members"].([]any)
	assert.Len(t, members, 1)

	// Duplicate → 10029.
	_, body = env.call(t, http.MethodPost, "/api/v1/admin/tenancy/projects/proj-a/members",
		map[string]any{"userId": "u-2", "role": "admin"})
	assert.Equal(t, float64(10029), body["code"])

	// Role change.
	code, body = env.call(t, http.MethodPatch, "/api/v1/admin/tenancy/projects/proj-a/members/u-2",
		map[string]any{"role": "admin"})
	require.Equal(t, http.StatusOK, code, "body: %v", body)
	assert.Equal(t, "admin", body["member"].(map[string]any)["role"])

	// Remove.
	code, body = env.call(t, http.MethodDelete, "/api/v1/admin/tenancy/projects/proj-a/members/u-2", nil)
	require.Equal(t, http.StatusOK, code, "body: %v", body)

	// Unknown project → 10006.
	_, body = env.call(t, http.MethodGet, "/api/v1/admin/tenancy/projects/proj-missing/members", nil)
	assert.Equal(t, float64(10006), body["code"])
}

// A non-admin caller is forbidden from managing project members.
func TestFVTProjectMemberForbidden(t *testing.T) {
	env := newRBACEnv(t)
	env.seedRBACMember(t, "org-a", "u-admin", tenancy.RoleMember)
	env.seedRBACProject(t, "proj-a", "org-a")

	_, body := env.call(t, http.MethodPost, "/api/v1/admin/tenancy/projects/proj-a/members",
		map[string]any{"userId": "u-2", "role": "member"})
	assert.Equal(t, float64(10036), body["code"])
}

// fvtUserResolver resolves a username to a canonical id.
type fvtUserResolver struct{}

func (fvtUserResolver) ResolveUserID(_ context.Context, idOrUsername string) (string, error) {
	if idOrUsername == "alice" {
		return "u-alice", nil
	}
	return "", apierrors.New(apierrors.CodeUserNotFound)
}

// A username is resolved to the canonical user id before insert.
func TestFVTProjectMemberResolvesUsername(t *testing.T) {
	env := newRBACEnv(t)
	env.tenancySvc.SetUserResolver(fvtUserResolver{})
	env.seedRBACMember(t, "org-a", "u-admin", tenancy.RoleOwner)
	env.seedRBACProject(t, "proj-a", "org-a")

	// Add by username.
	code, body := env.call(t, http.MethodPost, "/api/v1/admin/tenancy/projects/proj-a/members",
		map[string]any{"userId": "alice", "role": "member"})
	require.Equal(t, http.StatusOK, code, "body: %v", body)
	assert.Equal(t, "u-alice", body["member"].(map[string]any)["userId"])

	// Unknown username → 10003.
	_, body = env.call(t, http.MethodPost, "/api/v1/admin/tenancy/projects/proj-a/members",
		map[string]any{"userId": "nobody", "role": "member"})
	assert.Equal(t, float64(10003), body["code"])
}