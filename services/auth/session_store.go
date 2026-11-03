// Package auth implements the authentication and authorization service:
// local accounts, API keys, SSO federation, and the gateway-facing key
// verification used to authenticate inference traffic.
//
// Session store (feature #7): Redis-backed server-side sessions and
// access tokens. Sessions are revocable and carry the full context
// (user, roles, accessible orgs, active org) so org-scoped services
// resolve the org context without a DB join.
package auth

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	goredis "github.com/redis/go-redis/v9"

	apierrors "github.com/go-taas/go-taas/pkg/errors"
)

// sessionKeyPrefix namespaces the session entries in Redis.
const sessionKeyPrefix = "taas:auth:session:"

// sessionKey returns the Redis key of a session's hash.
func sessionKey(sessionID string) string { return sessionKeyPrefix + sessionID }

// userSessionIndexPrefix namespaces the per-user session index.
const userSessionIndexPrefix = "taas:auth:user-sessions:"

// userSessionsKey returns the Redis key of an account's session index.
func userSessionsKey(userID string) string { return userSessionIndexPrefix + userID }

// Session is the server-side session context carried in Redis.
type Session struct {
	SessionID      string   `json:"session_id"`
	UserID         string   `json:"user_id"`
	Username       string   `json:"username"`
	Email          string   `json:"email"`
	Roles          []string `json:"roles"`
	AccessibleOrgs []string `json:"accessible_orgs"`
	ActiveOrg      string   `json:"active_org"`
	ExpiresAt      int64    `json:"expires_at"` // unix seconds
	CreatedAt      int64    `json:"created_at"`
	// Realm is the session's audience marker (user or admin), minted
	// from the login binding (feature-17 AD2). Empty for a session
	// minted before the split, which the realm guard answers 10027.
	Realm string `json:"realm"`
}

// SessionStore is the Redis-backed session store.
type SessionStore struct {
	client *goredis.Client
	ttl    time.Duration
}

// NewSessionStore constructs a SessionStore bound to a Redis client.
func NewSessionStore(client *goredis.Client, ttl time.Duration) *SessionStore {
	return &SessionStore{client: client, ttl: ttl}
}

// Create stores a session and its access token with the configured TTL.
func (s *SessionStore) Create(ctx context.Context, sess *Session, accessToken string) error {
	if s.client == nil {
		return apierrors.Newf(apierrors.CodeInternal, "auth: session store unavailable")
	}
	roles, err := json.Marshal(sess.Roles)
	if err != nil {
		return err
	}
	orgs, err := json.Marshal(sess.AccessibleOrgs)
	if err != nil {
		return err
	}
	key := sessionKey(sess.SessionID)
	// Index the session under its user so a role change (or a disable) can
	// revoke every session that account holds. The index expires on its own,
	// and a stale member is harmless: revoking a gone session is a no-op.
	if sess.UserID != "" {
		indexKey := userSessionsKey(sess.UserID)
		if err := s.client.SAdd(ctx, indexKey, sess.SessionID).Err(); err != nil {
			return err
		}
		if err := s.client.Expire(ctx, indexKey, s.ttl).Err(); err != nil {
			return err
		}
	}
	if err := s.client.HSet(ctx, key, map[string]any{
		"user_id":         sess.UserID,
		"username":        sess.Username,
		"email":           sess.Email,
		"roles":           string(roles),
		"accessible_orgs": string(orgs),
		"active_org":      sess.ActiveOrg,
		"expires_at":      sess.ExpiresAt,
		"created_at":      sess.CreatedAt,
		"realm":           sess.Realm,
	}).Err(); err != nil {
		return err
	}
	if err := s.client.Expire(ctx, key, s.ttl).Err(); err != nil {
		return err
	}
	return s.client.Set(ctx, key+":token", accessToken, s.ttl).Err()
}

// Get loads a session by id; a missing/expired key returns
// CodeSessionInvalid.
func (s *SessionStore) Get(ctx context.Context, sessionID string) (*Session, error) {
	if s.client == nil {
		return nil, apierrors.New(apierrors.CodeSessionInvalid)
	}
	key := sessionKey(sessionID)
	vals, err := s.client.HGetAll(ctx, key).Result()
	if err != nil {
		return nil, err
	}
	if len(vals) == 0 {
		return nil, apierrors.New(apierrors.CodeSessionInvalid)
	}
	var roles, orgs []string
	_ = json.Unmarshal([]byte(vals["roles"]), &roles)
	_ = json.Unmarshal([]byte(vals["accessible_orgs"]), &orgs)
	expiresAt := parseInt64(vals["expires_at"])
	if expiresAt > 0 && expiresAt < time.Now().Unix() {
		// Expired: clean up and report invalid.
		_ = s.client.Del(ctx, key, key+":token").Err()
		return nil, apierrors.New(apierrors.CodeSessionInvalid)
	}
	return &Session{
		SessionID:      sessionID,
		UserID:         vals["user_id"],
		Username:       vals["username"],
		Email:          vals["email"],
		Roles:          roles,
		AccessibleOrgs: orgs,
		ActiveOrg:      vals["active_org"],
		ExpiresAt:      expiresAt,
		CreatedAt:      parseInt64(vals["created_at"]),
		Realm:          vals["realm"],
	}, nil
}

// UpdateOrg sets the session's active org (validated by the caller).
func (s *SessionStore) UpdateOrg(ctx context.Context, sessionID, orgID string) error {
	if s.client == nil {
		return apierrors.New(apierrors.CodeSessionInvalid)
	}
	key := sessionKey(sessionID)
	exists, err := s.client.Exists(ctx, key).Result()
	if err != nil {
		return err
	}
	if exists == 0 {
		return apierrors.New(apierrors.CodeSessionInvalid)
	}
	return s.client.HSet(ctx, key, "active_org", orgID).Err()
}

// Revoke deletes the session and its token (idempotent).
func (s *SessionStore) Revoke(ctx context.Context, sessionID string) error {
	if s.client == nil {
		return apierrors.New(apierrors.CodeSessionInvalid)
	}
	key := sessionKey(sessionID)
	// Drop the user index entry too, so a revoked session is not retried on
	// every later revoke-all for that account.
	if userID, err := s.client.HGet(ctx, key, "user_id").Result(); err == nil && userID != "" {
		_ = s.client.SRem(ctx, userSessionsKey(userID), sessionID).Err()
	}
	return s.client.Del(ctx, key, key+":token").Err()
}

// RevokeUserSessions deletes every session the account holds and reports how
// many were removed. It exists because a session caches the roles it was
// minted with: without this, a demoted administrator keeps working until the
// token expires, and a newly promoted one has to log in twice to get an
// admin-realm session.
func (s *SessionStore) RevokeUserSessions(ctx context.Context, userID string) (int, error) {
	if s.client == nil || userID == "" {
		return 0, nil
	}
	indexKey := userSessionsKey(userID)
	ids, err := s.client.SMembers(ctx, indexKey).Result()
	if err != nil {
		return 0, err
	}
	revoked := 0
	for _, id := range ids {
		if err := s.Revoke(ctx, id); err != nil {
			return revoked, err
		}
		revoked++
	}
	if err := s.client.Del(ctx, indexKey).Err(); err != nil {
		return revoked, err
	}
	return revoked, nil
}

// parseInt64 parses a string to int64, returning 0 on failure.
func parseInt64(s string) int64 {
	var n int64
	_, _ = fmt.Sscanf(s, "%d", &n)
	return n
}
