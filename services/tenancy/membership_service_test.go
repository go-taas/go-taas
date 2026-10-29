package tenancy

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	tenancyv1 "github.com/go-taas/go-taas/proto/taas/tenancy/v1"

	apierrors "github.com/go-taas/go-taas/pkg/errors"
)

// fakeSessionResolver is a test SessionResolver.
type fakeSessionResolver struct {
	userID string
	email  string
}

func (f *fakeSessionResolver) SessionUserID(_ context.Context) (string, error) {
	if f.userID == "" {
		return "", apierrors.New(apierrors.CodeSessionInvalid)
	}
	return f.userID, nil
}

func (f *fakeSessionResolver) SessionUserEmail(_ context.Context) (string, error) {
	if f.email == "" {
		return "", apierrors.New(apierrors.CodeSessionInvalid)
	}
	return f.email, nil
}

// newMembershipService builds a tenancy service with the membership
// tables migrated and a wired role guard + session resolver.
func newMembershipService(t *testing.T) *Service {
	t.Helper()
	db := newTestDB(t)
	svc := NewForFVT(db)
	svc.SetRoleGuard(NewRoleGuard(db))
	svc.SetSessionResolver(&fakeSessionResolver{userID: "u-admin", email: "admin@x.com"})
	return svc
}

// seedMember adds a member directly.
func seedMember(t *testing.T, svc *Service, orgID, userID, role string) {
	t.Helper()
	repo := NewMembershipRepository(svc.repo.db.DB(context.Background()))
	require.NoError(t, repo.AddMember(context.Background(), &OrgMember{
		OrganizationID: orgID, UserID: userID, Role: role, JoinedAt: time.Now().UTC(),
	}))
}

// AC1: AddOrgMember adds a member; duplicate maps to 10029.
func TestServiceAddOrgMember(t *testing.T) {
	svc := newMembershipService(t)
	ctx := context.Background()
	seedMember(t, svc, "org-a", "u-admin", RoleOwner)

	resp, err := svc.AddOrgMember(ctx, &tenancyv1.AddOrgMemberRequest{
		OrganizationId: "org-a", UserId: "u-2", Role: RoleMember,
	})
	require.NoError(t, err)
	assert.Equal(t, RoleMember, resp.GetMember().GetRole())

	// Duplicate.
	_, err = svc.AddOrgMember(ctx, &tenancyv1.AddOrgMemberRequest{
		OrganizationId: "org-a", UserId: "u-2", Role: RoleAdmin,
	})
	ae, ok := apierrors.As(err)
	require.True(t, ok)
	assert.Equal(t, apierrors.CodeMemberExists, ae.Code)

	// Invalid role (owner not assignable).
	_, err = svc.AddOrgMember(ctx, &tenancyv1.AddOrgMemberRequest{
		OrganizationId: "org-a", UserId: "u-3", Role: RoleOwner,
	})
	ae, ok = apierrors.As(err)
	require.True(t, ok)
	assert.Equal(t, apierrors.CodeRoleInvalid, ae.Code)
}

// AC2: SetOrgMemberRole changes a role; unknown member -> 10030; owner
// protected -> 10032.
func TestServiceSetOrgMemberRole(t *testing.T) {
	svc := newMembershipService(t)
	ctx := context.Background()
	seedMember(t, svc, "org-a", "u-admin", RoleOwner)
	seedMember(t, svc, "org-a", "u-2", RoleMember)

	resp, err := svc.SetOrgMemberRole(ctx, &tenancyv1.SetOrgMemberRoleRequest{
		OrganizationId: "org-a", UserId: "u-2", Role: RoleAdmin,
	})
	require.NoError(t, err)
	assert.Equal(t, RoleAdmin, resp.GetMember().GetRole())

	// Unknown member.
	_, err = svc.SetOrgMemberRole(ctx, &tenancyv1.SetOrgMemberRoleRequest{
		OrganizationId: "org-a", UserId: "u-99", Role: RoleMember,
	})
	ae, ok := apierrors.As(err)
	require.True(t, ok)
	assert.Equal(t, apierrors.CodeMemberNotFound, ae.Code)

	// Owner protected.
	_, err = svc.SetOrgMemberRole(ctx, &tenancyv1.SetOrgMemberRoleRequest{
		OrganizationId: "org-a", UserId: "u-admin", Role: RoleMember,
	})
	ae, ok = apierrors.As(err)
	require.True(t, ok)
	assert.Equal(t, apierrors.CodeOwnerProtected, ae.Code)
}

// AC3: RemoveOrgMember removes a member; owner protected -> 10032.
func TestServiceRemoveOrgMember(t *testing.T) {
	svc := newMembershipService(t)
	ctx := context.Background()
	seedMember(t, svc, "org-a", "u-admin", RoleOwner)
	seedMember(t, svc, "org-a", "u-2", RoleMember)

	_, err := svc.RemoveOrgMember(ctx, &tenancyv1.RemoveOrgMemberRequest{
		OrganizationId: "org-a", UserId: "u-2",
	})
	require.NoError(t, err)

	// Owner protected.
	_, err = svc.RemoveOrgMember(ctx, &tenancyv1.RemoveOrgMemberRequest{
		OrganizationId: "org-a", UserId: "u-admin",
	})
	ae, ok := apierrors.As(err)
	require.True(t, ok)
	assert.Equal(t, apierrors.CodeOwnerProtected, ae.Code)
}

// AC8: RoleGuard enforces the admin/owner gate — a member cannot manage
// the roster.
func TestServiceRoleGuardForbidden(t *testing.T) {
	svc := newMembershipService(t)
	ctx := context.Background()
	seedMember(t, svc, "org-a", "u-member", RoleMember)

	_, err := svc.ListOrgMembers(ctx, &tenancyv1.ListOrgMembersRequest{OrganizationId: "org-a"})
	ae, ok := apierrors.As(err)
	require.True(t, ok)
	assert.Equal(t, apierrors.CodeForbidden, ae.Code)
}

// AC4: CreateInvitation stores a pending invite and returns the token
// once; duplicate pending -> 10035.
func TestServiceCreateInvitation(t *testing.T) {
	svc := newMembershipService(t)
	ctx := context.Background()
	seedMember(t, svc, "org-a", "u-admin", RoleOwner)

	resp, err := svc.CreateInvitation(ctx, &tenancyv1.CreateInvitationRequest{
		OrganizationId: "org-a", Email: "invitee@x.com", Role: RoleMember,
	})
	require.NoError(t, err)
	assert.NotEmpty(t, resp.GetToken())
	assert.Equal(t, InvitationPending, resp.GetInvitation().GetStatus())

	// Duplicate pending.
	_, err = svc.CreateInvitation(ctx, &tenancyv1.CreateInvitationRequest{
		OrganizationId: "org-a", Email: "invitee@x.com", Role: RoleAdmin,
	})
	ae, ok := apierrors.As(err)
	require.True(t, ok)
	assert.Equal(t, apierrors.CodeInvitationExists, ae.Code)
}

// AC5: AcceptInvitation creates a membership and marks the invite
// accepted; a used token -> 10033.
func TestServiceAcceptInvitation(t *testing.T) {
	svc := newMembershipService(t)
	ctx := context.Background()
	seedMember(t, svc, "org-a", "u-admin", RoleOwner)

	// Create an invite for the session user's email.
	createResp, err := svc.CreateInvitation(ctx, &tenancyv1.CreateInvitationRequest{
		OrganizationId: "org-a", Email: "admin@x.com", Role: RoleMember,
	})
	require.NoError(t, err)
	token := createResp.GetToken()

	// Accept.
	acceptResp, err := svc.AcceptInvitation(ctx, &tenancyv1.AcceptInvitationRequest{Token: token})
	require.NoError(t, err)
	assert.Equal(t, "org-a", acceptResp.GetOrganizationId())
	assert.Equal(t, RoleMember, acceptResp.GetRole())

	// The invite is consumed; reuse -> 10033.
	_, err = svc.AcceptInvitation(ctx, &tenancyv1.AcceptInvitationRequest{Token: token})
	ae, ok := apierrors.As(err)
	require.True(t, ok)
	assert.Equal(t, apierrors.CodeInvitationNotFound, ae.Code)
}

// AC6: RejectInvitation marks the invite rejected.
func TestServiceRejectInvitation(t *testing.T) {
	svc := newMembershipService(t)
	ctx := context.Background()
	seedMember(t, svc, "org-a", "u-admin", RoleOwner)

	createResp, err := svc.CreateInvitation(ctx, &tenancyv1.CreateInvitationRequest{
		OrganizationId: "org-a", Email: "admin@x.com", Role: RoleMember,
	})
	require.NoError(t, err)

	_, err = svc.RejectInvitation(ctx, &tenancyv1.RejectInvitationRequest{Token: createResp.GetToken()})
	require.NoError(t, err)

	// Reuse -> 10033.
	_, err = svc.RejectInvitation(ctx, &tenancyv1.RejectInvitationRequest{Token: createResp.GetToken()})
	ae, ok := apierrors.As(err)
	require.True(t, ok)
	assert.Equal(t, apierrors.CodeInvitationNotFound, ae.Code)
}

// AC5: AcceptInvitation with an expired invite -> 10034.
func TestServiceAcceptExpiredInvitation(t *testing.T) {
	svc := newMembershipService(t)
	ctx := context.Background()
	seedMember(t, svc, "org-a", "u-admin", RoleOwner)

	// Insert an expired invite directly.
	repo := NewMembershipRepository(svc.repo.db.DB(ctx))
	raw := "expired-token"
	hash, err := hashInvitationToken(raw)
	require.NoError(t, err)
	require.NoError(t, repo.CreateInvitation(ctx, &Invitation{
		ID: "inv-exp", OrganizationID: "org-a", Email: "admin@x.com", Role: RoleMember,
		TokenHash: hash, Status: InvitationPending, ExpiresAt: time.Now().Add(-time.Hour).Unix(),
		CreatedBy: "u-admin", CreatedAt: time.Now().UTC(),
	}))

	_, err = svc.AcceptInvitation(ctx, &tenancyv1.AcceptInvitationRequest{Token: raw})
	ae, ok := apierrors.As(err)
	require.True(t, ok)
	assert.Equal(t, apierrors.CodeInvitationExpired, ae.Code)
}

// AC1: ListOrgMembers returns the roster.
func TestServiceListOrgMembers(t *testing.T) {
	svc := newMembershipService(t)
	ctx := context.Background()
	seedMember(t, svc, "org-a", "u-admin", RoleOwner)
	seedMember(t, svc, "org-a", "u-2", RoleMember)

	resp, err := svc.ListOrgMembers(ctx, &tenancyv1.ListOrgMembersRequest{OrganizationId: "org-a"})
	require.NoError(t, err)
	assert.Len(t, resp.GetMembers(), 2)
}

// AC4: ListInvitations returns the pipeline.
func TestServiceListInvitations(t *testing.T) {
	svc := newMembershipService(t)
	ctx := context.Background()
	seedMember(t, svc, "org-a", "u-admin", RoleOwner)

	_, err := svc.CreateInvitation(ctx, &tenancyv1.CreateInvitationRequest{
		OrganizationId: "org-a", Email: "a@x.com", Role: RoleMember,
	})
	require.NoError(t, err)

	resp, err := svc.ListInvitations(ctx, &tenancyv1.ListInvitationsRequest{OrganizationId: "org-a"})
	require.NoError(t, err)
	assert.Len(t, resp.GetInvitations(), 1)
}

// AC6: RevokeInvitation cancels a pending invite.
func TestServiceRevokeInvitation(t *testing.T) {
	svc := newMembershipService(t)
	ctx := context.Background()
	seedMember(t, svc, "org-a", "u-admin", RoleOwner)

	createResp, err := svc.CreateInvitation(ctx, &tenancyv1.CreateInvitationRequest{
		OrganizationId: "org-a", Email: "a@x.com", Role: RoleMember,
	})
	require.NoError(t, err)

	resp, err := svc.RevokeInvitation(ctx, &tenancyv1.RevokeInvitationRequest{
		InvitationId: createResp.GetInvitation().GetInvitationId(),
	})
	require.NoError(t, err)
	assert.Equal(t, InvitationRevoked, resp.GetInvitation().GetStatus())

	// Revoking a consumed invite -> 10033.
	_, err = svc.RevokeInvitation(ctx, &tenancyv1.RevokeInvitationRequest{
		InvitationId: createResp.GetInvitation().GetInvitationId(),
	})
	ae, ok := apierrors.As(err)
	require.True(t, ok)
	assert.Equal(t, apierrors.CodeInvitationNotFound, ae.Code)
}

// AC6: ResendInvitation issues a new token.
func TestServiceResendInvitation(t *testing.T) {
	svc := newMembershipService(t)
	ctx := context.Background()
	seedMember(t, svc, "org-a", "u-admin", RoleOwner)

	createResp, err := svc.CreateInvitation(ctx, &tenancyv1.CreateInvitationRequest{
		OrganizationId: "org-a", Email: "a@x.com", Role: RoleMember,
	})
	require.NoError(t, err)

	resp, err := svc.ResendInvitation(ctx, &tenancyv1.ResendInvitationRequest{
		InvitationId: createResp.GetInvitation().GetInvitationId(),
	})
	require.NoError(t, err)
	assert.NotEmpty(t, resp.GetToken())
	assert.NotEqual(t, createResp.GetToken(), resp.GetToken())
}

// AC2: MembershipResolver returns the user's memberships.
func TestMembershipResolver(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	repo := NewMembershipRepository(db)
	require.NoError(t, repo.AddMember(ctx, &OrgMember{
		OrganizationID: "org-a", UserID: "u-1", Role: RoleMember, JoinedAt: time.Now().UTC(),
	}))
	require.NoError(t, repo.AddMember(ctx, &OrgMember{
		OrganizationID: "org-b", UserID: "u-1", Role: RoleAdmin, JoinedAt: time.Now().UTC(),
	}))

	resolver := NewMembershipResolver(db)
	orgs, err := resolver.AccessibleOrgsAndRoles(ctx, "u-1")
	require.NoError(t, err)
	assert.Len(t, orgs, 2)
}

// AC8: RoleGuard truth table — viewer/member below admin -> forbidden.
func TestRoleGuardTruthTable(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	repo := NewMembershipRepository(db)
	for _, role := range []string{RoleViewer, RoleMember, RoleAdmin, RoleOwner} {
		require.NoError(t, repo.AddMember(ctx, &OrgMember{
			OrganizationID: "org-a", UserID: "u-" + role, Role: role, JoinedAt: time.Now().UTC(),
		}))
	}
	guard := NewRoleGuard(db)

	// viewer and member are below admin.
	err := guard.RequireAdminOrOwner(ctx, "org-a", "u-viewer")
	assert.EqualValues(t, apierrors.CodeForbidden, apierrors.CodeOf(err))
	err = guard.RequireAdminOrOwner(ctx, "org-a", "u-member")
	assert.EqualValues(t, apierrors.CodeForbidden, apierrors.CodeOf(err))

	// admin and owner pass.
	require.NoError(t, guard.RequireAdminOrOwner(ctx, "org-a", "u-admin"))
	require.NoError(t, guard.RequireAdminOrOwner(ctx, "org-a", "u-owner"))

	// Non-member -> forbidden.
	err = guard.RequireAdminOrOwner(ctx, "org-a", "u-nobody")
	assert.EqualValues(t, apierrors.CodeForbidden, apierrors.CodeOf(err))
}

// seedOrganization adds an organization row directly, so the directory can
// resolve a membership's display name.
func seedOrganization(t *testing.T, svc *Service, orgID, displayName string) {
	t.Helper()
	repo := NewRepository(svc.repo.db.DB(context.Background()))
	require.NoError(t, repo.CreateOrganization(context.Background(), &Organization{
		ID: orgID, DisplayName: displayName, State: StateActive,
	}))
}

// TestAddOrgMemberMovesTheUserAcrossOrganizations covers the single-organization
// rule at write time: joining a second organization removes the first, and with
// it any project membership the user held inside the organization they left.
func TestAddOrgMemberMovesTheUserAcrossOrganizations(t *testing.T) {
	svc := newMembershipService(t)
	ctx := context.Background()

	seedOrganization(t, svc, "org-a", "Org A")
	seedOrganization(t, svc, "org-b", "Org B")
	// The caller administers both organizations.
	seedMember(t, svc, "org-a", "u-admin", RoleOwner)
	seedMember(t, svc, "org-b", "u-admin", RoleOwner)
	// The user starts out in org-a, on a project of org-a.
	seedMember(t, svc, "org-a", "u-2", RoleMember)
	seedProject(t, svc, "proj-a", "org-a")
	seedProjectMember(t, svc, "proj-a", "u-2", RoleMember)

	_, err := svc.AddOrgMember(ctx, &tenancyv1.AddOrgMemberRequest{
		OrganizationId: "org-b", UserId: "u-2", Role: RoleMember,
	})
	require.NoError(t, err)

	repo := NewMembershipRepository(svc.repo.db.DB(ctx))
	// The old organization membership is gone.
	old, err := repo.FindMember(ctx, "org-a", "u-2")
	require.NoError(t, err)
	assert.Nil(t, old, "joining org-b must remove the org-a membership")

	// The new one exists.
	moved, err := repo.FindMember(ctx, "org-b", "u-2")
	require.NoError(t, err)
	require.NotNil(t, moved)
	assert.Equal(t, RoleMember, moved.Role)

	// The project of the organization they left is no longer theirs.
	pm, err := repo.FindProjectMember(ctx, "proj-a", "u-2")
	require.NoError(t, err)
	assert.Nil(t, pm, "a project of the previous organization must not survive the move")

	// The directory resolves the new organization.
	resolver := NewMembershipResolver(svc.repo.db.DB(ctx))
	affiliation, err := resolver.UserAffiliation(ctx, "u-2")
	require.NoError(t, err)
	assert.Equal(t, "org-b", affiliation.OrganizationID)
	assert.Equal(t, "Org B", affiliation.OrganizationName)
	assert.Empty(t, affiliation.ProjectID)
}

// TestAddOrgMemberKeepsProjectsOfTheTargetOrganization proves the move does not
// take away a project the user already holds inside the organization they are
// joining.
func TestAddOrgMemberKeepsProjectsOfTheTargetOrganization(t *testing.T) {
	svc := newMembershipService(t)
	ctx := context.Background()

	seedOrganization(t, svc, "org-a", "Org A")
	seedOrganization(t, svc, "org-b", "Org B")
	seedMember(t, svc, "org-a", "u-admin", RoleOwner)
	seedMember(t, svc, "org-b", "u-admin", RoleOwner)
	seedProject(t, svc, "proj-b", "org-b")
	seedProjectMember(t, svc, "proj-b", "u-2", RoleMember)

	_, err := svc.AddOrgMember(ctx, &tenancyv1.AddOrgMemberRequest{
		OrganizationId: "org-b", UserId: "u-2", Role: RoleMember,
	})
	require.NoError(t, err)

	repo := NewMembershipRepository(svc.repo.db.DB(ctx))
	pm, err := repo.FindProjectMember(ctx, "proj-b", "u-2")
	require.NoError(t, err)
	require.NotNil(t, pm, "a project of the target organization must be kept")
}

// TestAddOrgMemberKeepsThePlatformMembership proves the platform anchor is not
// treated as an affiliation the move clears.
func TestAddOrgMemberKeepsThePlatformMembership(t *testing.T) {
	svc := newMembershipService(t)
	ctx := context.Background()

	seedOrganization(t, svc, "org-a", "Org A")
	seedOrganization(t, svc, "org-b", "Org B")
	seedMember(t, svc, platformOrgID, "u-admin", RoleOwner)
	seedMember(t, svc, "org-a", "u-admin", RoleOwner)
	seedMember(t, svc, "org-b", "u-admin", RoleOwner)
	seedMember(t, svc, "org-a", "u-2", RoleMember)

	_, err := svc.AddOrgMember(ctx, &tenancyv1.AddOrgMemberRequest{
		OrganizationId: "org-b", UserId: "u-2", Role: RoleMember,
	})
	require.NoError(t, err)

	repo := NewMembershipRepository(svc.repo.db.DB(ctx))
	platform, err := repo.FindMember(ctx, platformOrgID, "u-2")
	require.NoError(t, err)
	assert.Nil(t, platform, "the user was never in the platform organization")

	anchor, err := repo.FindMember(ctx, platformOrgID, "u-admin")
	require.NoError(t, err)
	assert.NotNil(t, anchor, "the platform anchor membership is never cleared by a move")
}

// TestAddProjectMemberMovesTheUserAcrossProjects covers the same rule at the
// project level: an account belongs to at most one project.
func TestAddProjectMemberMovesTheUserAcrossProjects(t *testing.T) {
	svc := newMembershipService(t)
	ctx := context.Background()

	seedOrganization(t, svc, "org-a", "Org A")
	seedMember(t, svc, "org-a", "u-admin", RoleOwner)
	seedProject(t, svc, "proj-1", "org-a")
	seedProject(t, svc, "proj-2", "org-a")
	seedProjectMember(t, svc, "proj-1", "u-2", RoleMember)

	_, err := svc.AddProjectMember(ctx, &tenancyv1.AddProjectMemberRequest{
		ProjectId: "proj-2", UserId: "u-2", Role: RoleMember,
	})
	require.NoError(t, err)

	repo := NewMembershipRepository(svc.repo.db.DB(ctx))
	first, err := repo.FindProjectMember(ctx, "proj-1", "u-2")
	require.NoError(t, err)
	assert.Nil(t, first, "joining a second project must remove the first membership")

	second, err := repo.FindProjectMember(ctx, "proj-2", "u-2")
	require.NoError(t, err)
	require.NotNil(t, second)
}

// TestAddOrgMemberRefusesToMoveAnOwner keeps an organization from losing its
// last owner through the platform directory: the move is refused with 10032,
// the same code the roster returns when removing an owner.
func TestAddOrgMemberRefusesToMoveAnOwner(t *testing.T) {
	svc := newMembershipService(t)
	ctx := context.Background()

	seedOrganization(t, svc, "org-a", "Org A")
	seedOrganization(t, svc, "org-b", "Org B")
	seedMember(t, svc, "org-a", "u-owner", RoleOwner)
	seedMember(t, svc, "org-a", "u-admin", RoleOwner)
	seedMember(t, svc, "org-b", "u-admin", RoleOwner)

	_, err := svc.AddOrgMember(ctx, &tenancyv1.AddOrgMemberRequest{
		OrganizationId: "org-b", UserId: "u-owner", Role: RoleMember,
	})
	ae, ok := apierrors.As(err)
	require.True(t, ok)
	assert.Equal(t, apierrors.CodeOwnerProtected, ae.Code)

	// Nothing moved.
	repo := NewMembershipRepository(svc.repo.db.DB(ctx))
	still, err := repo.FindMember(ctx, "org-a", "u-owner")
	require.NoError(t, err)
	require.NotNil(t, still, "a refused move must leave the owner in place")
	assert.Equal(t, RoleOwner, still.Role)
}
