package tenancy

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	apierrors "github.com/go-taas/go-taas/pkg/errors"
)

// AC1: AddMember stores (org, user, role); a duplicate maps to 10029.
func TestMembershipAddDuplicate(t *testing.T) {
	db := newTestDB(t)
	repo := NewMembershipRepository(db)
	ctx := context.Background()

	m := &OrgMember{OrganizationID: "org-a", UserID: "u-1", Role: RoleMember, JoinedAt: time.Now().UTC()}
	require.NoError(t, repo.AddMember(ctx, m))

	dup := &OrgMember{OrganizationID: "org-a", UserID: "u-1", Role: RoleAdmin, JoinedAt: time.Now().UTC()}
	err := repo.AddMember(ctx, dup)
	assert.EqualValues(t, apierrors.CodeMemberExists, apierrors.CodeOf(err))
}

// AC1: FindMember returns the row; nil when absent.
func TestMembershipFindMember(t *testing.T) {
	db := newTestDB(t)
	repo := NewMembershipRepository(db)
	ctx := context.Background()

	require.NoError(t, repo.AddMember(ctx, &OrgMember{
		OrganizationID: "org-a", UserID: "u-1", Role: RoleMember, JoinedAt: time.Now().UTC(),
	}))
	m, err := repo.FindMember(ctx, "org-a", "u-1")
	require.NoError(t, err)
	require.NotNil(t, m)
	assert.Equal(t, RoleMember, m.Role)

	none, err := repo.FindMember(ctx, "org-a", "u-2")
	require.NoError(t, err)
	assert.Nil(t, none)
}

// AC1: ListMembers returns the roster.
func TestMembershipListMembers(t *testing.T) {
	db := newTestDB(t)
	repo := NewMembershipRepository(db)
	ctx := context.Background()

	require.NoError(t, repo.AddMember(ctx, &OrgMember{OrganizationID: "org-a", UserID: "u-1", Role: RoleMember, JoinedAt: time.Now().UTC()}))
	require.NoError(t, repo.AddMember(ctx, &OrgMember{OrganizationID: "org-a", UserID: "u-2", Role: RoleAdmin, JoinedAt: time.Now().UTC()}))
	require.NoError(t, repo.AddMember(ctx, &OrgMember{OrganizationID: "org-b", UserID: "u-1", Role: RoleViewer, JoinedAt: time.Now().UTC()}))

	rows, total, err := repo.ListMembers(ctx, "org-a", 0, 20)
	require.NoError(t, err)
	assert.Equal(t, int64(2), total)
	assert.Len(t, rows, 2)
}

// AC3: CountOwners counts owner rows.
func TestMembershipCountOwners(t *testing.T) {
	db := newTestDB(t)
	repo := NewMembershipRepository(db)
	ctx := context.Background()

	require.NoError(t, repo.AddMember(ctx, &OrgMember{OrganizationID: "org-a", UserID: "u-1", Role: RoleOwner, JoinedAt: time.Now().UTC()}))
	require.NoError(t, repo.AddMember(ctx, &OrgMember{OrganizationID: "org-a", UserID: "u-2", Role: RoleMember, JoinedAt: time.Now().UTC()}))

	count, err := repo.CountOwners(ctx, "org-a")
	require.NoError(t, err)
	assert.Equal(t, int64(1), count)
}

// AC2: ListMemberOrgs returns the user's (org, role) memberships.
func TestMembershipListMemberOrgs(t *testing.T) {
	db := newTestDB(t)
	repo := NewMembershipRepository(db)
	ctx := context.Background()

	require.NoError(t, repo.AddMember(ctx, &OrgMember{OrganizationID: "org-a", UserID: "u-1", Role: RoleMember, JoinedAt: time.Now().UTC()}))
	require.NoError(t, repo.AddMember(ctx, &OrgMember{OrganizationID: "org-b", UserID: "u-1", Role: RoleAdmin, JoinedAt: time.Now().UTC()}))

	rows, err := repo.ListMemberOrgs(ctx, "u-1")
	require.NoError(t, err)
	assert.Len(t, rows, 2)
}

// Project member repository: add, find, list, role change, remove.
func TestProjectMembershipCRUD(t *testing.T) {
	db := newTestDB(t)
	repo := NewMembershipRepository(db)
	ctx := context.Background()

	// Add.
	m := &ProjectMember{ProjectID: "proj-a", UserID: "u-1", Role: RoleMember, JoinedAt: time.Now().UTC()}
	require.NoError(t, repo.AddProjectMember(ctx, m))

	// Duplicate (project, user) maps to 10029.
	dup := &ProjectMember{ProjectID: "proj-a", UserID: "u-1", Role: RoleAdmin, JoinedAt: time.Now().UTC()}
	err := repo.AddProjectMember(ctx, dup)
	assert.EqualValues(t, apierrors.CodeMemberExists, apierrors.CodeOf(err))

	// Find.
	found, err := repo.FindProjectMember(ctx, "proj-a", "u-1")
	require.NoError(t, err)
	require.NotNil(t, found)
	assert.Equal(t, RoleMember, found.Role)

	// Absent → nil.
	absent, err := repo.FindProjectMember(ctx, "proj-a", "u-missing")
	require.NoError(t, err)
	assert.Nil(t, absent)

	// List.
	require.NoError(t, repo.AddProjectMember(ctx, &ProjectMember{ProjectID: "proj-a", UserID: "u-2", Role: RoleViewer, JoinedAt: time.Now().UTC()}))
	rows, total, err := repo.ListProjectMembers(ctx, "proj-a", 0, 10)
	require.NoError(t, err)
	assert.EqualValues(t, 2, total)
	assert.Len(t, rows, 2)

	// Role change.
	require.NoError(t, repo.SetProjectMemberRole(ctx, "proj-a", "u-1", RoleAdmin))
	found, err = repo.FindProjectMember(ctx, "proj-a", "u-1")
	require.NoError(t, err)
	assert.Equal(t, RoleAdmin, found.Role)

	// Remove.
	require.NoError(t, repo.RemoveProjectMember(ctx, "proj-a", "u-1"))
	absent, err = repo.FindProjectMember(ctx, "proj-a", "u-1")
	require.NoError(t, err)
	assert.Nil(t, absent)
}

// AC4: CreateInvitation stores a pending invite; a duplicate pending
// (org, email) maps to 10035.
func TestInvitationCreateDuplicate(t *testing.T) {
	db := newTestDB(t)
	repo := NewMembershipRepository(db)
	ctx := context.Background()

	inv := &Invitation{
		ID: "inv-1", OrganizationID: "org-a", Email: "a@x.com", Role: RoleMember,
		TokenHash: "h1", Status: InvitationPending, ExpiresAt: time.Now().Add(time.Hour).Unix(),
		CreatedBy: "u-1", CreatedAt: time.Now().UTC(),
	}
	require.NoError(t, repo.CreateInvitation(ctx, inv))

	dup := &Invitation{
		ID: "inv-2", OrganizationID: "org-a", Email: "a@x.com", Role: RoleAdmin,
		TokenHash: "h2", Status: InvitationPending, ExpiresAt: time.Now().Add(time.Hour).Unix(),
		CreatedBy: "u-1", CreatedAt: time.Now().UTC(),
	}
	err := repo.CreateInvitation(ctx, dup)
	assert.EqualValues(t, apierrors.CodeInvitationExists, apierrors.CodeOf(err))
}

// AC4: FindInvitationByTokenHash returns the invite; nil when absent.
func TestInvitationFindByTokenHash(t *testing.T) {
	db := newTestDB(t)
	repo := NewMembershipRepository(db)
	ctx := context.Background()

	inv := &Invitation{
		ID: "inv-1", OrganizationID: "org-a", Email: "a@x.com", Role: RoleMember,
		TokenHash: "h1", Status: InvitationPending, ExpiresAt: time.Now().Add(time.Hour).Unix(),
		CreatedBy: "u-1", CreatedAt: time.Now().UTC(),
	}
	require.NoError(t, repo.CreateInvitation(ctx, inv))

	got, err := repo.FindInvitationByTokenHash(ctx, "h1")
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, "a@x.com", got.Email)

	none, err := repo.FindInvitationByTokenHash(ctx, "nope")
	require.NoError(t, err)
	assert.Nil(t, none)
}

// AC5: SetInvitationStatus transitions the status.
func TestInvitationSetStatus(t *testing.T) {
	db := newTestDB(t)
	repo := NewMembershipRepository(db)
	ctx := context.Background()

	inv := &Invitation{
		ID: "inv-1", OrganizationID: "org-a", Email: "a@x.com", Role: RoleMember,
		TokenHash: "h1", Status: InvitationPending, ExpiresAt: time.Now().Add(time.Hour).Unix(),
		CreatedBy: "u-1", CreatedAt: time.Now().UTC(),
	}
	require.NoError(t, repo.CreateInvitation(ctx, inv))
	require.NoError(t, repo.SetInvitationStatus(ctx, "inv-1", InvitationAccepted))

	got, err := repo.FindInvitationByID(ctx, "inv-1")
	require.NoError(t, err)
	assert.Equal(t, InvitationAccepted, got.Status)
}

// AC6: PendingInvitationExists reports a pending invite for (org, email).
func TestInvitationPendingExists(t *testing.T) {
	db := newTestDB(t)
	repo := NewMembershipRepository(db)
	ctx := context.Background()

	inv := &Invitation{
		ID: "inv-1", OrganizationID: "org-a", Email: "a@x.com", Role: RoleMember,
		TokenHash: "h1", Status: InvitationPending, ExpiresAt: time.Now().Add(time.Hour).Unix(),
		CreatedBy: "u-1", CreatedAt: time.Now().UTC(),
	}
	require.NoError(t, repo.CreateInvitation(ctx, inv))

	exists, err := repo.PendingInvitationExists(ctx, "org-a", "a@x.com")
	require.NoError(t, err)
	assert.True(t, exists)

	// A consumed invite no longer counts as pending.
	require.NoError(t, repo.SetInvitationStatus(ctx, "inv-1", InvitationAccepted))
	exists, err = repo.PendingInvitationExists(ctx, "org-a", "a@x.com")
	require.NoError(t, err)
	assert.False(t, exists)
}
