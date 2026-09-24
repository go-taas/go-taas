package auth

import (
	"context"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	apierrors "github.com/go-taas/go-taas/pkg/errors"
)

// newTestSessionStore wires a SessionStore against the fake Redis server.
func newTestSessionStore(t *testing.T) (*SessionStore, *fakeRedisServer) {
	t.Helper()
	srv := newFakeRedisServer(t)
	client := redis.NewClient(&redis.Options{Addr: srv.addr})
	t.Cleanup(func() { _ = client.Close() })
	return NewSessionStore(client, time.Hour), srv
}

func TestSessionStoreCreateGet(t *testing.T) {
	store, _ := newTestSessionStore(t)
	ctx := context.Background()

	sess := &Session{
		SessionID:      "sess-1",
		UserID:         "user-1",
		Username:       "alice",
		Roles:          []string{"admin"},
		AccessibleOrgs: []string{"org-a", "org-b"},
		ActiveOrg:      "org-a",
		ExpiresAt:      time.Now().Add(time.Hour).Unix(),
		CreatedAt:      time.Now().Unix(),
	}
	require.NoError(t, store.Create(ctx, sess, "access-token"))

	got, err := store.Get(ctx, "sess-1")
	require.NoError(t, err)
	assert.Equal(t, "user-1", got.UserID)
	assert.Equal(t, "alice", got.Username)
	assert.Equal(t, []string{"admin"}, got.Roles)
	assert.Equal(t, []string{"org-a", "org-b"}, got.AccessibleOrgs)
	assert.Equal(t, "org-a", got.ActiveOrg)

	// Missing session → 10027.
	_, err = store.Get(ctx, "sess-missing")
	assert.EqualValues(t, apierrors.CodeSessionInvalid, apierrors.CodeOf(err))
}

func TestSessionStoreUpdateOrg(t *testing.T) {
	store, _ := newTestSessionStore(t)
	ctx := context.Background()

	sess := &Session{
		SessionID:      "sess-1",
		UserID:         "user-1",
		Username:       "alice",
		AccessibleOrgs: []string{"org-a", "org-b"},
		ActiveOrg:      "org-a",
		ExpiresAt:      time.Now().Add(time.Hour).Unix(),
		CreatedAt:      time.Now().Unix(),
	}
	require.NoError(t, store.Create(ctx, sess, "token"))

	require.NoError(t, store.UpdateOrg(ctx, "sess-1", "org-b"))
	got, err := store.Get(ctx, "sess-1")
	require.NoError(t, err)
	assert.Equal(t, "org-b", got.ActiveOrg)

	// Missing session → 10027.
	err = store.UpdateOrg(ctx, "sess-missing", "org-a")
	assert.EqualValues(t, apierrors.CodeSessionInvalid, apierrors.CodeOf(err))
}

func TestSessionStoreRevoke(t *testing.T) {
	store, _ := newTestSessionStore(t)
	ctx := context.Background()

	sess := &Session{
		SessionID:      "sess-1",
		UserID:         "user-1",
		Username:       "alice",
		AccessibleOrgs: []string{"org-a"},
		ActiveOrg:      "org-a",
		ExpiresAt:      time.Now().Add(time.Hour).Unix(),
		CreatedAt:      time.Now().Unix(),
	}
	require.NoError(t, store.Create(ctx, sess, "token"))

	// Revoke (idempotent).
	require.NoError(t, store.Revoke(ctx, "sess-1"))
	require.NoError(t, store.Revoke(ctx, "sess-1"))

	// Subsequent Get → 10027.
	_, err := store.Get(ctx, "sess-1")
	assert.EqualValues(t, apierrors.CodeSessionInvalid, apierrors.CodeOf(err))
}

func TestSessionStoreNilClient(t *testing.T) {
	store := NewSessionStore(nil, time.Hour)
	ctx := context.Background()

	_, err := store.Get(ctx, "sess-1")
	assert.EqualValues(t, apierrors.CodeSessionInvalid, apierrors.CodeOf(err))
	err = store.Create(ctx, &Session{SessionID: "s"}, "t")
	assert.EqualValues(t, apierrors.CodeInternal, apierrors.CodeOf(err))
}