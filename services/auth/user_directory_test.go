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

func TestServiceUserOrganizationCount(t *testing.T) {
	svc, db := newSSOService(t)
	ctx := context.Background()

	require.NoError(t, db.Create(&User{ID: "user-1", Username: "alice", Email: "a@x.com"}).Error)
	require.NoError(t, db.Create(&tenancy.Organization{ID: "org-a", DisplayName: "A", State: tenancy.StateActive}).Error)
	require.NoError(t, db.Create(&tenancy.Organization{ID: "org-b", DisplayName: "B", State: tenancy.StateActive}).Error)

	// Without a membership resolver the count reads 0.
	resp, err := svc.GetUser(ctx, &authv1.GetUserRequest{UserId: "user-1"})
	require.NoError(t, err)
	assert.EqualValues(t, 0, resp.User.OrganizationCount)

	// Wire the membership resolver and add memberships.
	svc.SetMembershipResolver(tenancy.NewMembershipResolver(db))
	require.NoError(t, db.Create(&tenancy.OrgMember{OrganizationID: "org-a", UserID: "user-1", Role: "member"}).Error)
	require.NoError(t, db.Create(&tenancy.OrgMember{OrganizationID: "org-b", UserID: "user-1", Role: "viewer"}).Error)

	resp, err = svc.GetUser(ctx, &authv1.GetUserRequest{UserId: "user-1"})
	require.NoError(t, err)
	assert.EqualValues(t, 2, resp.User.OrganizationCount)
}