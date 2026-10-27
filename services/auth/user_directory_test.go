package auth

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	authv1 "github.com/go-taas/go-taas/proto/taas/auth/v1"
	commonv1 "github.com/go-taas/go-taas/proto/taas/common/v1"

	apierrors "github.com/go-taas/go-taas/pkg/errors"
	"github.com/go-taas/go-taas/services/tenancy"
)

func TestServiceListUsers(t *testing.T) {
	svc, db := newSSOService(t)
	ctx := context.Background()

	require.NoError(t, db.Create(&User{ID: "user-1", Username: "alice", Email: "a@x.com"}).Error)
	require.NoError(t, db.Create(&User{ID: "user-2", Username: "bob", Email: "b@x.com"}).Error)
	require.NoError(t, db.Create(&User{ID: "user-3", Username: "carol", Email: "c@x.com", State: UserStateDisabled}).Error)

	// All users.
	resp, err := svc.ListUsers(ctx, &authv1.ListUsersRequest{Page: &commonv1.PageRequest{Limit: 10}})
	require.NoError(t, err)
	assert.EqualValues(t, 3, resp.PageMeta.Total)
	assert.Len(t, resp.Users, 3)
	assert.Equal(t, "user-3", resp.Users[0].UserId, "newest first")
	assert.Equal(t, UserStateDisabled, resp.Users[0].State)

	// State filter.
	resp, err = svc.ListUsers(ctx, &authv1.ListUsersRequest{State: UserStateActive, Page: &commonv1.PageRequest{Limit: 10}})
	require.NoError(t, err)
	assert.EqualValues(t, 2, resp.PageMeta.Total)
	assert.Len(t, resp.Users, 2)

	// Invalid state.
	_, err = svc.ListUsers(ctx, &authv1.ListUsersRequest{State: "bogus"})
	assert.EqualValues(t, apierrors.CodeTenancyInvalid, apierrors.CodeOf(err))
}

func TestServiceGetUser(t *testing.T) {
	svc, db := newSSOService(t)
	ctx := context.Background()

	require.NoError(t, db.Create(&User{ID: "user-1", Username: "alice", Email: "a@x.com"}).Error)

	resp, err := svc.GetUser(ctx, &authv1.GetUserRequest{UserId: "user-1"})
	require.NoError(t, err)
	assert.Equal(t, "user-1", resp.User.UserId)
	assert.Equal(t, "alice", resp.User.Username)
	assert.Equal(t, UserStateActive, resp.User.State)

	// Unknown user.
	_, err = svc.GetUser(ctx, &authv1.GetUserRequest{UserId: "user-missing"})
	assert.EqualValues(t, apierrors.CodeUserNotFound, apierrors.CodeOf(err))
}

func TestServiceDisableEnableUser(t *testing.T) {
	svc, db := newSSOService(t)
	ctx := context.Background()

	require.NoError(t, db.Create(&User{ID: "user-1", Username: "alice", Email: "a@x.com"}).Error)

	// Disable.
	dis, err := svc.DisableUser(ctx, &authv1.DisableUserRequest{UserId: "user-1"})
	require.NoError(t, err)
	assert.Equal(t, UserStateDisabled, dis.User.State)

	// Persisted.
	var row User
	require.NoError(t, db.First(&row, "id = ?", "user-1").Error)
	assert.Equal(t, UserStateDisabled, row.State)

	// Enable.
	en, err := svc.EnableUser(ctx, &authv1.EnableUserRequest{UserId: "user-1"})
	require.NoError(t, err)
	assert.Equal(t, UserStateActive, en.User.State)

	// Unknown user on disable.
	_, err = svc.DisableUser(ctx, &authv1.DisableUserRequest{UserId: "user-missing"})
	assert.EqualValues(t, apierrors.CodeUserNotFound, apierrors.CodeOf(err))
}

func TestServiceUserAffiliation(t *testing.T) {
	svc, db := newSSOService(t)
	ctx := context.Background()

	require.NoError(t, db.Create(&User{ID: "user-1", Username: "alice", Email: "a@x.com"}).Error)
	require.NoError(t, db.Create(&tenancy.Organization{ID: "org-a", DisplayName: "Acme", State: tenancy.StateActive}).Error)
	require.NoError(t, db.Create(&tenancy.Organization{ID: "org-b", DisplayName: "Beta", State: tenancy.StateActive}).Error)
	require.NoError(t, db.Create(&tenancy.Project{ID: "proj-a", OrganizationID: "org-a", DisplayName: "Alpha", State: tenancy.StateActive}).Error)

	// Without a membership resolver the affiliation stays empty; the
	// console renders that as "-".
	resp, err := svc.GetUser(ctx, &authv1.GetUserRequest{UserId: "user-1"})
	require.NoError(t, err)
	assert.Empty(t, resp.User.OrganizationName)
	assert.Empty(t, resp.User.ProjectName)

	// Wire the membership resolver: the directory shows the organization
	// and project display names, not a membership count.
	svc.SetMembershipResolver(tenancy.NewMembershipResolver(db))
	require.NoError(t, db.Create(&tenancy.OrgMember{OrganizationID: "org-a", UserID: "user-1", Role: "member"}).Error)
	require.NoError(t, db.Create(&tenancy.ProjectMember{ProjectID: "proj-a", UserID: "user-1", Role: "member"}).Error)

	resp, err = svc.GetUser(ctx, &authv1.GetUserRequest{UserId: "user-1"})
	require.NoError(t, err)
	assert.Equal(t, "org-a", resp.User.OrganizationId)
	assert.Equal(t, "Acme", resp.User.OrganizationName)
	assert.Equal(t, "proj-a", resp.User.ProjectId)
	assert.Equal(t, "Alpha", resp.User.ProjectName)
}

// The dedicated platform organization is the tenancy anchor of the
// platform itself, not a tenant a person joins, so it is never reported
// as a user's affiliation.
func TestServiceUserAffiliationSkipsPlatformOrg(t *testing.T) {
	svc, db := newSSOService(t)
	ctx := context.Background()

	require.NoError(t, db.Create(&User{ID: "user-2", Username: "bob", Email: "b@x.com"}).Error)
	require.NoError(t, db.Create(&tenancy.Organization{ID: "platform", DisplayName: "Platform", State: tenancy.StateActive}).Error)
	require.NoError(t, db.Create(&tenancy.Organization{ID: "org-a", DisplayName: "Acme", State: tenancy.StateActive}).Error)
	require.NoError(t, db.Create(&tenancy.OrgMember{OrganizationID: "platform", UserID: "user-2", Role: "platform-admin"}).Error)

	svc.SetMembershipResolver(tenancy.NewMembershipResolver(db))
	resp, err := svc.GetUser(ctx, &authv1.GetUserRequest{UserId: "user-2"})
	require.NoError(t, err)
	assert.Empty(t, resp.User.OrganizationName)
	assert.Empty(t, resp.User.OrganizationId)
}

func TestServiceResolveUserID(t *testing.T) {
	svc, db := newSSOService(t)
	ctx := context.Background()

	userID := "440722d6-4847-40b6-abb6-db12fc41e1a1"
	require.NoError(t, db.Create(&User{ID: userID, Username: "alice", Email: "a@x.com"}).Error)

	// Resolve by username.
	id, err := svc.ResolveUserID(ctx, "alice")
	require.NoError(t, err)
	assert.Equal(t, userID, id)

	// Resolve by id (a valid UUID).
	id, err = svc.ResolveUserID(ctx, userID)
	require.NoError(t, err)
	assert.Equal(t, userID, id)

	// Unknown username → 10003.
	_, err = svc.ResolveUserID(ctx, "nobody")
	assert.EqualValues(t, apierrors.CodeUserNotFound, apierrors.CodeOf(err))

	// Unknown id → 10003.
	_, err = svc.ResolveUserID(ctx, "00000000-0000-0000-0000-000000000000")
	assert.EqualValues(t, apierrors.CodeUserNotFound, apierrors.CodeOf(err))
}
func TestServiceDeleteUser(t *testing.T) {
	svc, db := newSSOService(t)
	ctx := context.Background()

	require.NoError(t, db.Create(&User{ID: "user-1", Username: "alice", Email: "a@x.com"}).Error)
	require.NoError(t, db.Create(&IdentityBinding{ID: "b-1", ProviderID: "p", ExternalSubject: "s", UserID: "user-1"}).Error)

	resp, err := svc.DeleteUser(ctx, &authv1.DeleteUserRequest{UserId: "user-1"})
	require.NoError(t, err)
	assert.NotNil(t, resp.Response)

	// The user and its bindings are gone.
	var count int64
	require.NoError(t, db.Model(&User{}).Where("id = ?", "user-1").Count(&count).Error)
	assert.EqualValues(t, 0, count)
	require.NoError(t, db.Model(&IdentityBinding{}).Where("user_id = ?", "user-1").Count(&count).Error)
	assert.EqualValues(t, 0, count)

	// Unknown user → 10003.
	_, err = svc.DeleteUser(ctx, &authv1.DeleteUserRequest{UserId: "missing"})
	assert.EqualValues(t, apierrors.CodeUserNotFound, apierrors.CodeOf(err))
}

// TestResolveUserLabels verifies the roster label resolver: display name
// when set, username as the fallback, and unknown ids omitted.
func TestResolveUserLabels(t *testing.T) {
	svc, db := newSSOService(t)
	ctx := context.Background()
	require.NoError(t, db.Create(&User{
		ID: "u-with-name", Username: "alice", Email: "alice@example.com",
		DisplayName: "Alice Zhang", State: UserStateActive,
	}).Error)
	require.NoError(t, db.Create(&User{
		ID: "u-without-name", Username: "bob", Email: "bob@example.com",
		State: UserStateActive,
	}).Error)

	labels, err := svc.ResolveUserLabels(ctx, []string{"u-with-name", "u-without-name", "u-unknown"})
	require.NoError(t, err)
	assert.Equal(t, "Alice Zhang", labels["u-with-name"])
	assert.Equal(t, "bob", labels["u-without-name"], "falls back to the username")
	_, ok := labels["u-unknown"]
	assert.False(t, ok, "unknown ids are omitted")

	// An empty batch short-circuits.
	empty, err := svc.ResolveUserLabels(ctx, nil)
	require.NoError(t, err)
	assert.Empty(t, empty)
}
