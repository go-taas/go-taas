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

// Session is the server-side session context carried in Redis.
type Session struct {
	SessionID      string   `json:"session_id"`
	UserID         string   `json:"user_id"`
	Username       string   `json:"username"`
	Roles          []string `json:"roles"`
	AccessibleOrgs []string `json:"accessible_orgs"`
	ActiveOrg      string   `json:"active_org"`
	ExpiresAt      int64    `json:"expires_at"` // unix seconds
	CreatedAt      int64    `json:"created_at"`
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
	if err := s.client.HSet(ctx, key, map[string]any{
		"user_id":         sess.UserID,
		"username":        sess.Username,
		"roles":           string(roles),
		"accessible_orgs": string(orgs),
		"active_org":      sess.ActiveOrg,
		"expires_at":      sess.ExpiresAt,
		"created_at":      sess.CreatedAt,
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
		Roles:          roles,
		AccessibleOrgs: orgs,
		ActiveOrg:      vals["active_org"],
		ExpiresAt:      expiresAt,
		CreatedAt:      parseInt64(vals["created_at"]),
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
	return s.client.Del(ctx, key, key+":token").Err()
}

// parseInt64 parses a string to int64, returning 0 on failure.
func parseInt64(s string) int64 {
	var n int64
	_, _ = fmt.Sscanf(s, "%d", &n)
	return n
}
