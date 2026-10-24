package fvt

import (
	"context"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/go-taas/go-taas/services/tenancy"
)

// seedJoinRequest inserts a pending request directly.
func (e *rbacEnv) seedJoinRequest(t *testing.T, id, userID, targetType, targetID string) {
	t.Helper()
	repo := tenancy.NewMembershipRepository(e.db)
	require.NoError(t, repo.CreateJoinRequest(context.Background(), &tenancy.JoinRequest{
		ID: id, UserID: userID, TargetType: targetType, TargetID: targetID,
		Status: tenancy.JoinStatusPending,
	}))
}

func TestFVTJoinRequestApproveAndReject(t *testing.T) {
	env := newRBACEnv(t)
	env.seedRBACMember(t, "org-a", "u-admin", tenancy.RoleOwner)
	env.seedJoinRequest(t, "jr-1", "u-new", tenancy.JoinTargetOrganization, "org-a")
	env.seedJoinRequest(t, "jr-2", "u-other", tenancy.JoinTargetOrganization, "org-a")

	// List pending requests for the org.
	code, body := env.call(t, http.MethodGet,
		"/api/v1/admin/tenancy/join-requests?targetType=organization&targetId=org-a&status=pending", nil)
	require.Equal(t, http.StatusOK, code, "body: %v", body)
	assert.Len(t, body["requests"].([]any), 2)

	// Approve one: the applicant becomes a member.
	code, body = env.call(t, http.MethodPost,
		"/api/v1/admin/tenancy/join-requests/jr-1:approve", nil)
	require.Equal(t, http.StatusOK, code, "body: %v", body)
	assert.Equal(t, "approved", body["request"].(map[string]any)["status"])

	code, body = env.call(t, http.MethodGet, "/api/v1/admin/tenancy/organizations/org-a/members", nil)
	require.Equal(t, http.StatusOK, code)
	assert.Len(t, body["members"].([]any), 2)

	// Reject the other.
	code, body = env.call(t, http.MethodPost,
		"/api/v1/admin/tenancy/join-requests/jr-2:reject", nil)
	require.Equal(t, http.StatusOK, code, "body: %v", body)
	assert.Equal(t, "rejected", body["request"].(map[string]any)["status"])
}

func TestFVTProjectQuota(t *testing.T) {
	env := newRBACEnv(t)
	env.seedRBACMember(t, "org-a", "u-admin", tenancy.RoleOwner)
	env.seedRBACProject(t, "proj-a", "org-a")

	code, body := env.call(t, http.MethodPatch,
		"/api/v1/admin/tenancy/projects/proj-a/quota", map[string]any{"maxConcurrentRequests": "25"})
	require.Equal(t, http.StatusOK, code, "body: %v", body)
	assert.Equal(t, "25", body["quota"].(map[string]any)["maxConcurrentRequests"])

	code, body = env.call(t, http.MethodGet, "/api/v1/admin/tenancy/projects/proj-a/quota", nil)
	require.Equal(t, http.StatusOK, code, "body: %v", body)
	assert.Equal(t, "25", body["quota"].(map[string]any)["maxConcurrentRequests"])
}
