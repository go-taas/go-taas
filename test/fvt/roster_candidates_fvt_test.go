package fvt

import (
	"context"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/go-taas/go-taas/services/tenancy"
)

// fakeCandidateDirectory stands in for the auth module's account directory.
type fakeCandidateDirectory struct {
	users []tenancy.CandidateUser
}

func (f fakeCandidateDirectory) ListActiveUsers(context.Context) ([]tenancy.CandidateUser, error) {
	return f.users, nil
}

// TestFVTRosterMemberCandidates verifies the admin roster pick-lists over the
// gateway: an organization offers the accounts that are not members of it,
// and a project offers its organization's members that are not on it. A
// person may belong to several organizations and projects.
func TestFVTRosterMemberCandidates(t *testing.T) {
	env := newRBACEnv(t)
	env.seedRBACMember(t, "org-a", "u-admin", tenancy.RoleOwner)
	env.seedRBACMember(t, "org-a", "u-org-free", tenancy.RoleMember)
	env.seedRBACMember(t, "org-a", "u-in-project", tenancy.RoleMember)
	env.seedRBACMember(t, "org-b", "u-outsider", tenancy.RoleMember)
	env.seedRBACProject(t, "proj-a", "org-a")
	env.seedRBACProjectMember(t, "proj-a", "u-in-project", tenancy.RoleMember)
	env.tenancySvc.SetCandidateUserDirectory(fakeCandidateDirectory{users: []tenancy.CandidateUser{
		{UserID: "u-admin", Username: "admin"},
		{UserID: "u-org-free", Username: "free", DisplayName: "Free Person", Email: "free@x.com"},
		{UserID: "u-in-project", Username: "member"},
		{UserID: "u-outsider", Username: "outsider"},
		{UserID: "u-unaffiliated", Username: "newbie", DisplayName: "New Bie"},
	}})

	// Organization roster: accounts that are not members of org-a. The
	// account that belongs to org-b is a candidate: it may join org-a too.
	code, body := env.call(t, http.MethodGet, "/api/v1/admin/tenancy/organizations/org-a/member-candidates", nil)
	require.Equal(t, http.StatusOK, code, "body: %v", body)
	candidates := body["candidates"].([]any)
	require.Len(t, candidates, 2)
	byID := map[string]map[string]any{}
	for _, raw := range candidates {
		row := raw.(map[string]any)
		byID[row["userId"].(string)] = row
	}
	require.Contains(t, byID, "u-unaffiliated")
	assert.Equal(t, "New Bie", byID["u-unaffiliated"]["displayName"])
	assert.Equal(t, "newbie", byID["u-unaffiliated"]["username"])
	require.Contains(t, byID, "u-outsider")

	// Project roster: the organization's members not on this project.
	code, body = env.call(t, http.MethodGet, "/api/v1/admin/tenancy/projects/proj-a/member-candidates", nil)
	require.Equal(t, http.StatusOK, code, "body: %v", body)
	ids := make([]string, 0)
	for _, raw := range body["candidates"].([]any) {
		ids = append(ids, raw.(map[string]any)["userId"].(string))
	}
	assert.Equal(t, []string{"u-admin", "u-org-free"}, ids)
}

// TestFVTRosterMemberCandidatesUnknownProject maps a missing project to 10006.
func TestFVTRosterMemberCandidatesUnknownProject(t *testing.T) {
	env := newRBACEnv(t)
	env.seedRBACMember(t, "org-a", "u-admin", tenancy.RoleOwner)

	code, body := env.call(t, http.MethodGet, "/api/v1/admin/tenancy/projects/absent/member-candidates", nil)
	require.Equal(t, http.StatusInternalServerError, code, "body: %v", body)
	assert.EqualValues(t, 10006, body["code"], "body: %v", body)
}
