package auth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	goredis "github.com/redis/go-redis/v9"
)

// keyVerdict is the positive verification result cached in Redis, keyed
// by the key digest. It never contains the plaintext key.
type keyVerdict struct {
	OrganizationID string `json:"organization_id"`
	KeyID          string `json:"key_id"`
	Role           string `json:"role"`
}

// cacheKeyPrefix namespaces the API-key verdict entries in Redis.
const cacheKeyPrefix = "taas:auth:apikey:"

// cacheKey returns the Redis key of a digest's verdict entry.
func cacheKey(digest string) string { return cacheKeyPrefix + digest }

// verdictCache is the positive cache consulted by VerifyAPIKey. Only
// positive verdicts are cached in v1; negative caching of unknown keys
// is deferred (see the architecture doc, Section 12).
type verdictCache interface {
	Get(ctx context.Context, digest string) (*keyVerdict, error)
	Set(ctx context.Context, digest string, v *keyVerdict, ttl time.Duration) error
	Delete(ctx context.Context, digest string) error
}

// redisVerdictCache implements verdictCache on Redis. A nil client fails
// closed: every operation errors so verification never silently
// succeeds.
type redisVerdictCache struct {
	client *goredis.Client
	ttl    time.Duration
}

// newRedisVerdictCache wraps the shared Redis client.
func newRedisVerdictCache(client *goredis.Client, ttl time.Duration) *redisVerdictCache {
	return &redisVerdictCache{client: client, ttl: ttl}
}

// Get returns the cached verdict, or nil on cache miss.
func (c *redisVerdictCache) Get(ctx context.Context, digest string) (*keyVerdict, error) {
	if c.client == nil {
		return nil, fmt.Errorf("auth: redis client unavailable")
	}
	raw, err := c.client.Get(ctx, cacheKey(digest)).Result()
	if err != nil {
		if errors.Is(err, goredis.Nil) {
			return nil, nil // cache miss
		}
		return nil, err
	}
	var v keyVerdict
	if err := json.Unmarshal([]byte(raw), &v); err != nil {
		return nil, err
	}
	return &v, nil
}

// Set caches a verdict with the configured TTL.
func (c *redisVerdictCache) Set(ctx context.Context, digest string, v *keyVerdict, _ time.Duration) error {
	if c.client == nil {
		return fmt.Errorf("auth: redis client unavailable")
	}
	raw, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return c.client.Set(ctx, cacheKey(digest), raw, c.ttl).Err()
}

// Delete removes the verdict entry (revoke-time invalidation).
func (c *redisVerdictCache) Delete(ctx context.Context, digest string) error {
	if c.client == nil {
		return fmt.Errorf("auth: redis client unavailable")
	}
	return c.client.Del(ctx, cacheKey(digest)).Err()
}

// fakeVerdictCache is the in-memory verdict cache used by unit tests.
type fakeVerdictCache struct {
	mu      sync.Mutex
	entries map[string]fakeEntry
}

type fakeEntry struct {
	verdict  *keyVerdict
	deadline time.Time
}

func newFakeVerdictCache() *fakeVerdictCache {
	return &fakeVerdictCache{entries: map[string]fakeEntry{}}
}

// Get returns the cached verdict, or nil on miss or expiry.
func (c *fakeVerdictCache) Get(_ context.Context, digest string) (*keyVerdict, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries[digest]
	if !ok || time.Now().After(e.deadline) {
		return nil, nil
	}
	return e.verdict, nil
}

// Set caches a verdict with the given TTL.
func (c *fakeVerdictCache) Set(_ context.Context, digest string, v *keyVerdict, ttl time.Duration) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries[digest] = fakeEntry{verdict: v, deadline: time.Now().Add(ttl)}
	return nil
}

// Delete removes the verdict entry.
func (c *fakeVerdictCache) Delete(_ context.Context, digest string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.entries, digest)
	return nil
}
