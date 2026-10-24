package auth

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/metadata"

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
		DisplayName: "Alice Updated", Avatar: "https://example.com/a.png",
	})
	require.NoError(t, err)
	assert.Equal(t, "Alice Updated", resp.Profile.DisplayName)
	assert.Equal(t, "https://example.com/a.png", resp.Profile.Avatar)

	// Persisted.
	var row User
	require.NoError(t, db.First(&row, "id = ?", userID).Error)
	assert.Equal(t, "Alice Updated", row.DisplayName)
	assert.Equal(t, "https://example.com/a.png", row.Avatar)

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