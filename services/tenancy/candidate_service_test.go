package tenancy

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	apierrors "github.com/go-taas/go-taas/pkg/errors"
	tenancyv1 "github.com/go-taas/go-taas/proto/taas/tenancy/v1"
)

// fakeCandidateDirectory is the auth-module seam: a fixed account list.
type fakeCandidateDirectory struct {
	users []CandidateUser
	err   error
}

func (f fakeCandidateDirectory) ListActiveUsers(context.Context) ([]CandidateUser, error) {
	return f.users, f.err
}

// newCandidateService wires a membership service with a fixed directory.
func newCandidateService(t *testing.T, users ...CandidateUser) *Service {
	t.Helper()
	svc := newMembershipService(t)
	svc.SetCandidateUserDirectory(fakeCandidateDirectory{users: users})
	return svc
}

func candidateIDs(resp []*tenancyv1.MemberCandidate) []string {
	out := make([]string, 0, len(resp))
	for _, c := range resp {
		out = append(out, c.GetUserId())
	}
	return out
}

// TestListOrgMemberCandidatesExcludesMembers verifies the org pick-list is the
// accounts that have not joined any organization yet: a person belongs to at
// most one organization.
func TestListOrgMemberCandidatesExcludesMembers(t *testing.T) {
	svc := newCandidateService(t,
		CandidateUser{UserID: "u-admin", Username: "admin"},
		CandidateUser{UserID: "u-free", Username: "free", DisplayName: "Free Person", Email: "free@x.com"},
		CandidateUser{UserID: "u-other-org"},
	)
	ctx := context.Background()
	seedMember(t, svc, "org-a", "u-admin", RoleOwner)
	seedMember(t, svc, "org-b", "u-other-org", RoleMember)

	resp, err := svc.ListOrgMemberCandidates(ctx, &tenancyv1.ListOrgMemberCandidatesRequest{
		OrganizationId: "org-a",
	})
	require.NoError(t, err)
	assert.Equal(t, []string{"u-free"}, candidateIDs(resp.GetCandidates()))
	assert.Equal(t, "Free Person", resp.GetCandidates()[0].GetDisplayName())
	assert.Equal(t, "free@x.com", resp.GetCandidates()[0].GetEmail())
}

// TestListOrgMemberCandidatesRequiresAdmin verifies a non-member cannot read
// the organization's pick-list.
func TestListOrgMemberCandidatesRequiresAdmin(t *testing.T) {
	svc := newCandidateService(t, CandidateUser{UserID: "u-1"})
	ctx := context.Background()
	seedMember(t, svc, "org-a", "u-admin", RoleOwner)
	seedMember(t, svc, "org-a", "u-viewer", RoleViewer)
	// The fake session user is the owner by default; switch to a viewer.
	svc.SetSessionResolver(&fakeSessionResolver{userID: "u-viewer", email: "v@x.com"})

	_, err := svc.ListOrgMemberCandidates(ctx, &tenancyv1.ListOrgMemberCandidatesRequest{
		OrganizationId: "org-a",
	})
	require.Error(t, err)
	assert.Equal(t, apierrors.CodeForbidden, apierrors.CodeOf(err))
}

// TestListProjectMemberCandidates verifies the project pick-list is limited to
// the owning organization's members that belong to no project.
func TestListProjectMemberCandidates(t *testing.T) {
	svc := newCandidateService(t,
		CandidateUser{UserID: "u-admin"},
		CandidateUser{UserID: "u-org-free"},
		CandidateUser{UserID: "u-in-project"},
		CandidateUser{UserID: "u-outsider"},
	)
	ctx := context.Background()
	seedMember(t, svc, "org-a", "u-admin", RoleOwner)
	seedMember(t, svc, "org-a", "u-org-free", RoleMember)
	seedMember(t, svc, "org-a", "u-in-project", RoleMember)
	seedMember(t, svc, "org-b", "u-outsider", RoleMember)
	seedProject(t, svc, "proj-a", "org-a")
	seedProjectMember(t, svc, "proj-a", "u-in-project", RoleMember)

	resp, err := svc.ListProjectMemberCandidates(ctx, &tenancyv1.ListProjectMemberCandidatesRequest{
		ProjectId: "proj-a",
	})
	require.NoError(t, err)
	// The owner is already an org member without a project, so they remain a
	// candidate; the outsider is not in the organization, and the existing
	// project member is already in a project.
	assert.Equal(t, []string{"u-admin", "u-org-free"}, candidateIDs(resp.GetCandidates()))
}

// TestListProjectMemberCandidatesUnknownProject maps a missing project to
// 10006.
func TestListProjectMemberCandidatesUnknownProject(t *testing.T) {
	svc := newCandidateService(t, CandidateUser{UserID: "u-1"})
	ctx := context.Background()
	seedMember(t, svc, "org-a", "u-admin", RoleOwner)

	_, err := svc.ListProjectMemberCandidates(ctx, &tenancyv1.ListProjectMemberCandidatesRequest{
		ProjectId: "missing",
	})
	require.Error(t, err)
	assert.Equal(t, apierrors.CodeProjectNotFound, apierrors.CodeOf(err))

	_, err = svc.ListProjectMemberCandidates(ctx, &tenancyv1.ListProjectMemberCandidatesRequest{})
	require.Error(t, err)
	assert.Equal(t, apierrors.CodeProjectNotFound, apierrors.CodeOf(err))
}

// TestListCandidatesWithoutDirectoryIsEmpty verifies the seam is safe when it
// is not wired: the pick-list is empty, never wrong.
func TestListCandidatesWithoutDirectoryIsEmpty(t *testing.T) {
	svc := newMembershipService(t)
	ctx := context.Background()
	seedMember(t, svc, "org-a", "u-admin", RoleOwner)

	resp, err := svc.ListOrgMemberCandidates(ctx, &tenancyv1.ListOrgMemberCandidatesRequest{
		OrganizationId: "org-a",
	})
	require.NoError(t, err)
	assert.Empty(t, resp.GetCandidates())
}
