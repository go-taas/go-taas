package auth

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/metadata"

	"gorm.io/gorm"

	authv1 "github.com/go-taas/go-taas/proto/taas/auth/v1"

	apierrors "github.com/go-taas/go-taas/pkg/errors"
)

// withSessionToken returns a context carrying the session token as the
// authorization metadata.
func withSessionToken(ctx context.Context, token string) context.Context {
	return metadata.NewIncomingContext(ctx, metadata.Pairs("authorization", "Bearer "+token))
}

func TestServiceGetUserProfile(t *testing.T) {
	svc, db := newSSOService(t)
	ctx := context.Background()

	userID := "440722d6-4847-40b6-abb6-db12fc41e1a1"
	require.NoError(t, db.Create(&User{ID: userID, Username: "alice", Email: "a@x.com", DisplayName: "Alice"}).Error)

	// No session → 10027.
	_, err := svc.GetUserProfile(ctx, &authv1.GetUserProfileRequest{})
	assert.EqualValues(t, apierrors.CodeSessionInvalid, apierrors.CodeOf(err))

	// With a session.
	require.NoError(t, svc.CreateSessionForTest(ctx, &Session{
		SessionID: "sess-1", UserID: userID, Username: "alice", Email: "a@x.com",
		ExpiresAt: 9999999999, Realm: RealmUser,
	}, "token-1"))
	ctx = withSessionToken(ctx, "sess-1")

	resp, err := svc.GetUserProfile(ctx, &authv1.GetUserProfileRequest{})
	require.NoError(t, err)
	assert.Equal(t, userID, resp.Profile.UserId)
	assert.Equal(t, "alice", resp.Profile.Username)
	assert.Equal(t, "a@x.com", resp.Profile.Email)
	assert.Equal(t, "Alice", resp.Profile.DisplayName)
}

func TestServiceUpdateUserProfile(t *testing.T) {
	svc, db := newSSOService(t)
	ctx := context.Background()

	userID := "440722d6-4847-40b6-abb6-db12fc41e1a1"
	require.NoError(t, db.Create(&User{ID: userID, Username: "alice", Email: "a@x.com"}).Error)
	require.NoError(t, svc.CreateSessionForTest(ctx, &Session{
		SessionID: "sess-1", UserID: userID, Username: "alice", Email: "a@x.com",
		ExpiresAt: 9999999999, Realm: RealmUser,
	}, "token-1"))
	ctx = withSessionToken(ctx, "sess-1")

	// Update display name and avatar.
	resp, err := svc.UpdateUserProfile(ctx, &authv1.UpdateUserProfileRequest{
		DisplayName: "Alice Updated", Avatar: "data:image/png;base64,iVBORw0KGgo=",
	})
	require.NoError(t, err)
	assert.Equal(t, "Alice Updated", resp.Profile.DisplayName)
	assert.Equal(t, "data:image/png;base64,iVBORw0KGgo=", resp.Profile.Avatar)

	// Persisted.
	var row User
	require.NoError(t, db.First(&row, "id = ?", userID).Error)
	assert.Equal(t, "Alice Updated", row.DisplayName)
	assert.Equal(t, "data:image/png;base64,iVBORw0KGgo=", row.Avatar)

	// Username and email are unchanged.
	assert.Equal(t, "alice", row.Username)
	assert.Equal(t, "a@x.com", row.Email)
}

func TestServiceUpdateUserProfileValidation(t *testing.T) {
	svc, db := newSSOService(t)
	ctx := context.Background()

	userID := "440722d6-4847-40b6-abb6-db12fc41e1a1"
	require.NoError(t, db.Create(&User{ID: userID, Username: "alice", Email: "a@x.com"}).Error)
	require.NoError(t, svc.CreateSessionForTest(ctx, &Session{
		SessionID: "sess-1", UserID: userID, Username: "alice", Email: "a@x.com",
		ExpiresAt: 9999999999, Realm: RealmUser,
	}, "token-1"))
	ctx = withSessionToken(ctx, "sess-1")

	// Oversized display name → 10019.
	_, err := svc.UpdateUserProfile(ctx, &authv1.UpdateUserProfileRequest{
		DisplayName: string(make([]byte, 200)),
	})
	assert.EqualValues(t, apierrors.CodeTenancyInvalid, apierrors.CodeOf(err))
}
// profileUser creates a user row and an authenticated context for it.
func profileUser(t *testing.T, svc *Service, db *gorm.DB, id, username, email, sessionID string) context.Context {
	t.Helper()
	ctx := context.Background()
	require.NoError(t, db.Create(&User{ID: id, Username: username, Email: email}).Error)
	require.NoError(t, svc.CreateSessionForTest(ctx, &Session{
		SessionID: sessionID, UserID: id, Username: username, Email: email,
		ExpiresAt: 9999999999, Realm: RealmUser,
	}, sessionID))
	return withSessionToken(ctx, sessionID)
}

// TestServiceUpdateUserProfileEmailChange verifies the email is replaced
// once the new address proves ownership with a verification code.
func TestServiceUpdateUserProfileEmailChange(t *testing.T) {
	svc, db := newSSOService(t)
	enableEmailVerification(t)
	const userID = "440722d6-4847-40b6-abb6-db12fc41e1a1"
	ctx := profileUser(t, svc, db, userID, "alice", "alice@x.com", "sess-e1")

	_, err := svc.SendEmailCode(ctx, &authv1.SendEmailCodeRequest{
		Email: "new@x.com", Purpose: emailCodePurposeChangeEmail,
	})
	require.NoError(t, err)
	client, err := svc.redisClientFor()
	require.NoError(t, err)
	code, err := client.Get(ctx, emailCodeKeyPrefix+"new@x.com:"+emailCodePurposeChangeEmail).Result()
	require.NoError(t, err)

	resp, err := svc.UpdateUserProfile(ctx, &authv1.UpdateUserProfileRequest{
		DisplayName: "Alice", Email: "new@x.com", EmailCode: code,
	})
	require.NoError(t, err)
	assert.Equal(t, "new@x.com", resp.Profile.Email)

	var row User
	require.NoError(t, db.First(&row, "id = ?", userID).Error)
	assert.Equal(t, "new@x.com", row.Email)
	assert.Equal(t, "alice", row.Username, "username stays immutable")
}

// TestServiceUpdateUserProfileEmailNeedsCode verifies a change without a
// valid code is rejected and the stored email is unchanged.
func TestServiceUpdateUserProfileEmailNeedsCode(t *testing.T) {
	svc, db := newSSOService(t)
	enableEmailVerification(t)
	const userID = "440722d6-4847-40b6-abb6-db12fc41e1a1"
	ctx := profileUser(t, svc, db, userID, "alice", "alice@x.com", "sess-e2")

	_, err := svc.UpdateUserProfile(ctx, &authv1.UpdateUserProfileRequest{Email: "new@x.com"})
	require.Error(t, err)
	assert.EqualValues(t, apierrors.CodeInvalidCredentials, apierrors.CodeOf(err))

	var row User
	require.NoError(t, db.First(&row, "id = ?", userID).Error)
	assert.Equal(t, "alice@x.com", row.Email)
}

// TestServiceUpdateUserProfileEmailTaken verifies an address owned by
// another account is refused.
func TestServiceUpdateUserProfileEmailTaken(t *testing.T) {
	svc, db := newSSOService(t)
	const userID = "440722d6-4847-40b6-abb6-db12fc41e1a1"
	ctx := profileUser(t, svc, db, userID, "alice", "alice@x.com", "sess-e3")
	require.NoError(t, db.Create(&User{
		ID: "b811aed1-cb28-402e-a03c-c01924da0aac", Username: "bob", Email: "bob@x.com",
	}).Error)

	_, err := svc.UpdateUserProfile(ctx, &authv1.UpdateUserProfileRequest{Email: "bob@x.com"})
	require.Error(t, err)
	assert.EqualValues(t, apierrors.CodeUserExists, apierrors.CodeOf(err))
	// The message names the real cause - the address, not the account -
	// so the console does not leave the user guessing which one clashed.
	assert.Contains(t, err.Error(), "bob@x.com")
	assert.Contains(t, err.Error(), "already in use")
}

// TestServiceUpdateUserProfileSameEmail verifies resubmitting the current
// address needs no code.
func TestServiceUpdateUserProfileSameEmail(t *testing.T) {
	svc, db := newSSOService(t)
	enableEmailVerification(t)
	const userID = "440722d6-4847-40b6-abb6-db12fc41e1a1"
	ctx := profileUser(t, svc, db, userID, "alice", "alice@x.com", "sess-e4")

	resp, err := svc.UpdateUserProfile(ctx, &authv1.UpdateUserProfileRequest{
		DisplayName: "Alice A", Email: "alice@x.com",
	})
	require.NoError(t, err)
	assert.Equal(t, "alice@x.com", resp.Profile.Email)
	assert.Equal(t, "Alice A", resp.Profile.DisplayName)
}

// TestServiceUpdateUserProfileEmailValidation verifies an oversized email
// is rejected.
func TestServiceUpdateUserProfileEmailValidation(t *testing.T) {
	svc, db := newSSOService(t)
	const userID = "440722d6-4847-40b6-abb6-db12fc41e1a1"
	ctx := profileUser(t, svc, db, userID, "alice", "alice@x.com", "sess-e5")

	_, err := svc.UpdateUserProfile(ctx, &authv1.UpdateUserProfileRequest{
		Email: strings.Repeat("a", maxEmailLen+1) + "@x.com",
	})
	require.Error(t, err)
	assert.EqualValues(t, apierrors.CodeTenancyInvalid, apierrors.CodeOf(err))
}

// TestServiceUpdateUserProfileEmailWithoutVerification verifies a free
// (verification-disabled) install can still change the address.
func TestServiceUpdateUserProfileEmailWithoutVerification(t *testing.T) {
	svc, db := newSSOService(t)
	const userID = "440722d6-4847-40b6-abb6-db12fc41e1a1"
	ctx := profileUser(t, svc, db, userID, "alice", "alice@x.com", "sess-e6")

	resp, err := svc.UpdateUserProfile(ctx, &authv1.UpdateUserProfileRequest{
		DisplayName: "Alice", Email: "free@x.com",
	})
	require.NoError(t, err)
	assert.Equal(t, "free@x.com", resp.Profile.Email)
}

// TestApplyEmailChangeUnknownUser verifies an unknown caller is reported
// as not found.
func TestApplyEmailChangeUnknownUser(t *testing.T) {
	svc, _ := newSSOService(t)
	repo, err := svc.ssoRepository()
	require.NoError(t, err)

	err = svc.applyEmailChange(context.Background(), repo,
		"00000000-0000-0000-0000-000000000000", "x@x.com", "")
	require.Error(t, err)
	assert.EqualValues(t, apierrors.CodeUserNotFound, apierrors.CodeOf(err))
}

// TestServiceUpdateUserProfileAvatarValidation verifies the avatar must
// be an inline image data URL within the size bound.
func TestServiceUpdateUserProfileAvatarValidation(t *testing.T) {
	svc, db := newSSOService(t)
	const userID = "440722d6-4847-40b6-abb6-db12fc41e1a1"
	ctx := profileUser(t, svc, db, userID, "alice", "alice@x.com", "sess-e7")

	// Remote URLs are refused (the avatar lives in the database).
	_, err := svc.UpdateUserProfile(ctx, &authv1.UpdateUserProfileRequest{
		DisplayName: "Alice", Avatar: "https://example.com/a.png",
	})
	require.Error(t, err)
	assert.EqualValues(t, apierrors.CodeTenancyInvalid, apierrors.CodeOf(err))

	// Oversized payloads are refused.
	_, err = svc.UpdateUserProfile(ctx, &authv1.UpdateUserProfileRequest{
		DisplayName: "Alice", Avatar: "data:image/png;base64," + strings.Repeat("A", maxAvatarLen),
	})
	require.Error(t, err)
	assert.EqualValues(t, apierrors.CodeTenancyInvalid, apierrors.CodeOf(err))
}

// TestServiceSendEmailCodeChangeEmailTaken verifies a change-email code is
// not even sent to an address another account already owns: the caller
// learns immediately instead of spending a code and the resend cooldown.
func TestServiceSendEmailCodeChangeEmailTaken(t *testing.T) {
	svc, db := newSSOService(t)
	enableEmailVerification(t)
	ctx := profileUser(t, svc, db, "440722d6-4847-40b6-abb6-db12fc41e1a1", "alice", "alice@x.com", "sess-taken")
	require.NoError(t, db.Create(&User{
		ID: "b811aed1-cb28-402e-a03c-c01924da0aac", Username: "bob", Email: "bob@x.com",
	}).Error)

	_, err := svc.SendEmailCode(ctx, &authv1.SendEmailCodeRequest{
		Email: "bob@x.com", Purpose: emailCodePurposeChangeEmail,
	})
	require.Error(t, err)
	assert.EqualValues(t, apierrors.CodeUserExists, apierrors.CodeOf(err))
	assert.Contains(t, err.Error(), "already in use")

	// The caller's own address is not a conflict.
	_, err = svc.SendEmailCode(ctx, &authv1.SendEmailCodeRequest{
		Email: "alice@x.com", Purpose: emailCodePurposeChangeEmail,
	})
	require.NoError(t, err)
}
