package auth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	goredis "github.com/redis/go-redis/v9"

	"github.com/go-taas/go-taas/pkg/config"
	apierrors "github.com/go-taas/go-taas/pkg/errors"
	"github.com/go-taas/go-taas/pkg/logger"
)

// ModelAuthorizer resolves whether an organization may use a model
// (feature-13, AD5). It is implemented by the model module's repository
// and injected at wiring time (the SetOrgGuard pattern), so the data
// plane and the control plane share one implementation of the
// default-allow rule. Nil until wired: the model field of VerifyAPIKey
// stays accepted-but-ignored, the transitional behaviour.
type ModelAuthorizer interface {
	// IsModelAuthorized returns true when the organization may use the
	// model: always for a model with zero grants, otherwise only for the
	// organizations in its grant list.
	IsModelAuthorized(ctx context.Context, modelID, organizationID string) (bool, error)
}

// defaultModelAuthCacheTTL bounds the revoke-propagation window of the
// data-plane authorization cache when model.auth.cacheTTL is unset (AD6).
const defaultModelAuthCacheTTL = 5 * time.Second

// modelAuthCacheKeyPrefix namespaces the per-(org, model) verdicts in
// Redis.
const modelAuthCacheKeyPrefix = "taas:auth:modelauth:"

// modelAuthCache caches the per-(org, model) authorization verdict. Both
// verdicts are cached — unlike the API-key verdict cache, a denial is
// worth caching because a non-granted organization can hammer a model it
// may not use.
type modelAuthCache interface {
	// Get returns the cached verdict, or nil on miss or expiry.
	Get(ctx context.Context, key string) (*bool, error)
	// Set caches the verdict with the given TTL.
	Set(ctx context.Context, key string, allowed bool, ttl time.Duration) error
}

// modelAuthCacheKey identifies one (organization, model) verdict.
func modelAuthCacheKey(organizationID, modelID string) string {
	return modelAuthCacheKeyPrefix + organizationID + ":" + modelID
}

// redisModelAuthCache implements modelAuthCache on Redis. A nil client
// fails closed at the operation level: the caller logs and falls back to
// the uncached check, which is the source of truth.
type redisModelAuthCache struct {
	client *goredis.Client
}

// newRedisModelAuthCache wraps the shared Redis client.
func newRedisModelAuthCache(client *goredis.Client) *redisModelAuthCache {
	return &redisModelAuthCache{client: client}
}

// Get returns the cached verdict, or nil on cache miss.
func (c *redisModelAuthCache) Get(ctx context.Context, key string) (*bool, error) {
	if c.client == nil {
		return nil, fmt.Errorf("auth: redis client unavailable")
	}
	raw, err := c.client.Get(ctx, key).Result()
	if err != nil {
		if errors.Is(err, goredis.Nil) {
			return nil, nil // cache miss
		}
		return nil, err
	}
	var allowed bool
	if err := json.Unmarshal([]byte(raw), &allowed); err != nil {
		return nil, err
	}
	return &allowed, nil
}

// Set caches a verdict with the configured TTL.
func (c *redisModelAuthCache) Set(ctx context.Context, key string, allowed bool, ttl time.Duration) error {
	if c.client == nil {
		return fmt.Errorf("auth: redis client unavailable")
	}
	raw, err := json.Marshal(allowed)
	if err != nil {
		return err
	}
	return c.client.Set(ctx, key, raw, ttl).Err()
}

// fakeModelAuthCache is the in-memory model-authorization cache used by
// unit tests.
type fakeModelAuthCache struct {
	mu      sync.Mutex
	entries map[string]fakeModelAuthEntry
}

type fakeModelAuthEntry struct {
	allowed  bool
	deadline time.Time
}

func newFakeModelAuthCache() *fakeModelAuthCache {
	return &fakeModelAuthCache{entries: map[string]fakeModelAuthEntry{}}
}

// Get returns the cached verdict, or nil on miss or expiry.
func (c *fakeModelAuthCache) Get(_ context.Context, key string) (*bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries[key]
	if !ok || time.Now().After(e.deadline) {
		return nil, nil
	}
	allowed := e.allowed
	return &allowed, nil
}

// Set caches a verdict with the given TTL.
func (c *fakeModelAuthCache) Set(_ context.Context, key string, allowed bool, ttl time.Duration) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries[key] = fakeModelAuthEntry{allowed: allowed, deadline: time.Now().Add(ttl)}
	return nil
}

// SetModelAuthorizer injects the model authorization check (feature-13,
// AD5). Production and FVT wire the model repository; unit tests leave it
// nil so the model field is ignored.
func (s *Service) SetModelAuthorizer(a ModelAuthorizer) { s.modelAuthorizer = a }

// ModelExistenceChecker resolves whether a model ID exists in the
// catalog (feature #46, AD6). It is implemented by the model module's
// repository and injected at wiring time next to the authorizer, so
// scope writes are validated against the same catalog the data plane
// authorizes against. Nil until wired: scope writes containing unknown
// model IDs are accepted (the transitional fail-open shape, matching
// the authorizer seam).
type ModelExistenceChecker interface {
	// ModelExists returns true when the model ID is present in the
	// catalog.
	ModelExists(ctx context.Context, modelID string) (bool, error)
}

// SetModelExistenceChecker injects the catalog existence check (feature
// #46, AD6). Production and FVT wire the model repository; unit tests
// leave it nil so unknown IDs stay accepted.
func (s *Service) SetModelExistenceChecker(c ModelExistenceChecker) { s.modelExistenceChecker = c }

// checkModelsExist validates that every model ID of a scope list exists
// in the catalog (feature #46, AD6). A nil checker accepts the list
// (transitional fail-open); a checker error fails the write.
func (s *Service) checkModelsExist(ctx context.Context, modelIDs []string) error {
	if s.modelExistenceChecker == nil || len(modelIDs) == 0 {
		return nil
	}
	for _, id := range modelIDs {
		exists, err := s.modelExistenceChecker.ModelExists(ctx, id)
		if err != nil {
			return err
		}
		if !exists {
			return apierrors.Newf(apierrors.CodeModelNotFound, "auth: model %q not found", id)
		}
	}
	return nil
}

// modelAuthCacheFor resolves the per-(org, model) verdict cache, lazily
// wiring the Redis-backed one from the shared components. A nil return
// means no cache is available: the check then runs uncached, because the
// cache is an optimization and the authorizer is the source of truth.
func (s *Service) modelAuthCacheFor() modelAuthCache {
	if s.modelAuthCache != nil {
		return s.modelAuthCache
	}
	if s.components == nil || s.components.Redis() == nil {
		return nil
	}
	client, ok := s.components.Redis().Client().(*goredis.Client)
	if !ok {
		return nil
	}
	s.modelAuthCache = newRedisModelAuthCache(client)
	return s.modelAuthCache
}

// modelAuthCacheTTL returns the configured data-plane authorization cache
// TTL, defaulting to 5s (AD6).
func (s *Service) modelAuthCacheTTL() time.Duration {
	if cfg := config.GetConfig(); cfg != nil && cfg.Model.Auth.CacheTTL > 0 {
		return cfg.Model.Auth.CacheTTL
	}
	return defaultModelAuthCacheTTL
}

// checkModelAuthorization enforces the data-plane gate (AC7): when the
// inference request names a model, a restricted model the key's
// organization is not granted returns 10105 MODEL_UNAUTHORIZED, while a
// model with zero grants stays open to every organization (AC9). The
// verdict is cached per (org, model) for modelAuthCacheTTL (AD6), so a
// revoked organization may keep calling for up to the cache window.
func (s *Service) checkModelAuthorization(ctx context.Context, modelID, organizationID string) error {
	if modelID == "" || s.modelAuthorizer == nil {
		// No model named, or no authorizer wired: nothing to enforce.
		return nil
	}
	key := modelAuthCacheKey(organizationID, modelID)
	cache := s.modelAuthCacheFor()
	if cache != nil {
		cached, err := cache.Get(ctx, key)
		switch {
		case err != nil:
			// A cache read failure only costs a database read.
			logger.S().Warnw("auth: model authorization cache read failed",
				"org_id", organizationID, "model_id", modelID, "err", err)
		case cached != nil:
			return modelAuthVerdict(*cached)
		}
	}

	allowed, err := s.modelAuthorizer.IsModelAuthorized(ctx, modelID, organizationID)
	if err != nil {
		return err
	}
	if cache != nil {
		if setErr := cache.Set(ctx, key, allowed, s.modelAuthCacheTTL()); setErr != nil {
			logger.S().Warnw("auth: model authorization cache write failed",
				"org_id", organizationID, "model_id", modelID, "err", setErr)
		}
	}
	return modelAuthVerdict(allowed)
}

// modelAuthVerdict maps an authorization verdict to its error.
func modelAuthVerdict(allowed bool) error {
	if allowed {
		return nil
	}
	return apierrors.New(apierrors.CodeModelUnauthorized)
}
