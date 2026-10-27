package auth

import (
	"context"

	"google.golang.org/grpc/metadata"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	authv1 "github.com/go-taas/go-taas/proto/taas/auth/v1"

	apierrors "github.com/go-taas/go-taas/pkg/errors"
	"github.com/go-taas/go-taas/services/tenancy"
)

func TestServiceCreateUser(t *testing.T) {
	svc, db := newSSOService(t)
	ctx := context.Background()

	resp, err := svc.CreateUser(ctx, &authv1.CreateUserRequest{
		Username: "alice", Password: "Password123", Email: "alice@x.com",
	})
	require.NoError(t, err)
	assert.NotEmpty(t, resp.UserId)

	// The password is stored hashed, never plaintext.
	var row User
	require.NoError(t, db.First(&row, "id = ?", resp.UserId).Error)
	assert.Equal(t, "alice", row.Username)
	assert.NotEqual(t, "Password123", row.PasswordHash)
	assert.Contains(t, row.PasswordHash, "argon2id$")
	assert.Equal(t, UserStateActive, row.State)

	// Duplicate username → 10004.
	_, err = svc.CreateUser(ctx, &authv1.CreateUserRequest{
		Username: "alice", Password: "Password123",
	})
	assert.EqualValues(t, apierrors.CodeUserExists, apierrors.CodeOf(err))
}

func TestServiceCreateUserValidation(t *testing.T) {
	svc, _ := newSSOService(t)
	ctx := context.Background()

	// Empty username → 10003.
	_, err := svc.CreateUser(ctx, &authv1.CreateUserRequest{Username: "", Password: "Password123"})
	assert.EqualValues(t, apierrors.CodeUserNotFound, apierrors.CodeOf(err))

	// Short password → 10014.
	_, err = svc.CreateUser(ctx, &authv1.CreateUserRequest{Username: "bob", Password: "short"})
	assert.EqualValues(t, apierrors.CodePasswordPolicy, apierrors.CodeOf(err))
}

func TestServiceLogin(t *testing.T) {
	svc, db := newSSOService(t)
	ctx := context.Background()

	// Register a user.
	_, err := svc.CreateUser(ctx, &authv1.CreateUserRequest{
		Username: "alice", Password: "Password123", Email: "alice@x.com",
	})
	require.NoError(t, err)

	// Correct credentials → session.
	resp, err := svc.Login(ctx, &authv1.LoginRequest{Username: "alice", Password: "Password123"})
	require.NoError(t, err)
	assert.NotEmpty(t, resp.SessionToken)
	assert.Greater(t, resp.ExpiresAt, int64(0))

	// Wrong password → 10002.
	_, err = svc.Login(ctx, &authv1.LoginRequest{Username: "alice", Password: "wrong"})
	assert.EqualValues(t, apierrors.CodeInvalidCredentials, apierrors.CodeOf(err))

	// Unknown user → 10002.
	_, err = svc.Login(ctx, &authv1.LoginRequest{Username: "nobody", Password: "Password123"})
	assert.EqualValues(t, apierrors.CodeInvalidCredentials, apierrors.CodeOf(err))

	// Empty credentials → 10002.
	_, err = svc.Login(ctx, &authv1.LoginRequest{Username: "", Password: ""})
	assert.EqualValues(t, apierrors.CodeInvalidCredentials, apierrors.CodeOf(err))

	// Disabled user → 10040.
	require.NoError(t, db.Model(&User{}).Where("username = ?", "alice").Update("state", UserStateDisabled).Error)
	_, err = svc.Login(ctx, &authv1.LoginRequest{Username: "alice", Password: "Password123"})
	assert.EqualValues(t, apierrors.CodeUserDisabled, apierrors.CodeOf(err))
}

func TestServiceLoginDerivesAdminRealm(t *testing.T) {
	svc, db := newSSOService(t)
	ctx := context.Background()

	// Register a user and make them an org admin.
	created, err := svc.CreateUser(ctx, &authv1.CreateUserRequest{
		Username: "admin2", Password: "Password123",
	})
	require.NoError(t, err)
	svc.SetMembershipResolver(tenancy.NewMembershipResolver(db))
	require.NoError(t, db.Create(&tenancy.Organization{ID: "org-a", DisplayName: "A", State: tenancy.StateActive}).Error)
	require.NoError(t, db.Create(&tenancy.OrgMember{OrganizationID: "org-a", UserID: created.UserId, Role: "admin"}).Error)

	resp, err := svc.Login(ctx, &authv1.LoginRequest{Username: "admin2", Password: "Password123"})
	require.NoError(t, err)
	assert.NotEmpty(t, resp.SessionToken)

	// The session realm derives as admin from the org role.
	sess, err := svc.sessionStore.Get(ctx, resp.SessionToken)
	require.NoError(t, err)
	assert.Equal(t, RealmAdmin, sess.Realm)
	assert.Equal(t, "org-a", sess.ActiveOrg)
}
// A platform admin is exempt from login email verification so the
// bootstrap admin can sign in with its initial password.
func TestServiceLoginPlatformAdminSkipsEmailCode(t *testing.T) {
	svc, db := newSSOService(t)
	ctx := context.Background()

	_, err := svc.CreateUser(ctx, &authv1.CreateUserRequest{
		Username: "rootadmin", Password: "Pass4admin",
	})
	require.NoError(t, err)
	svc.SetMembershipResolver(tenancy.NewMembershipResolver(db))
	require.NoError(t, db.Create(&tenancy.Organization{ID: "platform", DisplayName: "P", State: tenancy.StateActive}).Error)
	var u User
	require.NoError(t, db.First(&u, "username = ?", "rootadmin").Error)
	require.NoError(t, db.Create(&tenancy.OrgMember{
		OrganizationID: "platform", UserID: u.ID, Role: tenancy.RolePlatformAdmin,
	}).Error)

	enableEmailVerification(t)
	resp, err := svc.Login(ctx, &authv1.LoginRequest{Username: "rootadmin", Password: "Pass4admin"})
	require.NoError(t, err)
	assert.NotEmpty(t, resp.SessionToken)
}

// The login response carries the role-derived realm so the console
// stores the token under the right key.
func TestServiceLoginReturnsRealm(t *testing.T) {
	svc, db := newSSOService(t)
	ctx := context.Background()

	_, err := svc.CreateUser(ctx, &authv1.CreateUserRequest{Username: "plain", Password: "Pass4admin"})
	require.NoError(t, err)

	resp, err := svc.Login(ctx, &authv1.LoginRequest{Username: "plain", Password: "Pass4admin"})
	require.NoError(t, err)
	assert.Equal(t, RealmUser, resp.Realm, "a user without an org role signs in to the user realm")

	// Give the account a platform-admin role and sign in again.
	svc.SetMembershipResolver(tenancy.NewMembershipResolver(db))
	require.NoError(t, db.Create(&tenancy.Organization{ID: "platform", DisplayName: "P", State: tenancy.StateActive}).Error)
	var u User
	require.NoError(t, db.First(&u, "username = ?", "plain").Error)
	require.NoError(t, db.Create(&tenancy.OrgMember{
		OrganizationID: "platform", UserID: u.ID, Role: tenancy.RolePlatformAdmin,
	}).Error)

	resp, err = svc.Login(ctx, &authv1.LoginRequest{Username: "plain", Password: "Pass4admin"})
	require.NoError(t, err)
	assert.Equal(t, RealmAdmin, resp.Realm, "a platform admin signs in to the admin realm")
}

// TestServiceLoginPlatformAdminWithoutOrganization verifies the platform
// administrator identity is a property of the account: it derives the
// admin realm and the platform role with no organization and no
// membership row, so the platform needs no default organization.
func TestServiceLoginPlatformAdminWithoutOrganization(t *testing.T) {
	svc, db := newSSOService(t)
	ctx := context.Background()

	created, err := svc.CreateUser(ctx, &authv1.CreateUserRequest{
		Username: "rootadmin", Password: "Pass4admin",
	})
	require.NoError(t, err)
	require.NoError(t, db.Model(&User{}).Where("id = ?", created.UserId).
		Update("platform_admin", true).Error)
	svc.SetMembershipResolver(tenancy.NewMembershipResolver(db))

	resp, err := svc.Login(ctx, &authv1.LoginRequest{Username: "rootadmin", Password: "Pass4admin"})
	require.NoError(t, err)
	assert.Equal(t, RealmAdmin, resp.GetRealm())

	sess, err := svc.sessionStore.Get(ctx, resp.SessionToken)
	require.NoError(t, err)
	assert.Contains(t, sess.Roles, tenancy.RolePlatformAdmin)
	assert.Empty(t, sess.AccessibleOrgs)
	assert.Empty(t, sess.ActiveOrg)

	// The session roles feed the platform gate with no organization.
	roles, err := svc.SessionRoles(metadata.NewIncomingContext(context.Background(),
		metadata.Pairs("authorization", "Bearer "+resp.SessionToken)))
	require.NoError(t, err)
	assert.Contains(t, roles, tenancy.RolePlatformAdmin)
}

// A platform administrator is also exempt from login email verification by
// account flag, without any organization.
func TestServiceLoginPlatformAdminFlagSkipsEmailCode(t *testing.T) {
	svc, db := newSSOService(t)
	ctx := context.Background()

	created, err := svc.CreateUser(ctx, &authv1.CreateUserRequest{
		Username: "root2", Password: "Pass4admin", Email: "root2@x.com",
	})
	require.NoError(t, err)
	require.NoError(t, db.Model(&User{}).Where("id = ?", created.UserId).
		Update("platform_admin", true).Error)

	enableEmailVerification(t)
	_, err = svc.Login(ctx, &authv1.LoginRequest{Username: "root2", Password: "Pass4admin"})
	require.NoError(t, err)
}
