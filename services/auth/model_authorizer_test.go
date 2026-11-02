package auth

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	authv1 "github.com/go-taas/go-taas/proto/taas/auth/v1"

	"github.com/go-taas/go-taas/pkg/config"
	apierrors "github.com/go-taas/go-taas/pkg/errors"
)

// fakeModelAuthorizer is a scripted ModelAuthorizer that counts how often
// it is consulted, so a cache assertion can tell "answered from cache"
// from "re-checked".
type fakeModelAuthorizer struct {
	// denied maps "org|model" to a denial.
	denied map[string]bool
	calls  int
	err    error
}

// ResolveModelRef echoes the reference: these tests compare ids only.
func (f *fakeModelAuthorizer) ResolveModelRef(_ context.Context, ref string) (string, error) {
	return ref, nil
}

func (f *fakeModelAuthorizer) IsModelAuthorized(_ context.Context, modelID, organizationID string) (bool, error) {
	f.calls++
	if f.err != nil {
		return false, f.err
	}
	return !f.denied[organizationID+"|"+modelID], nil
}

// verifyModel verifies the key of env's first created key against a
// model, returning the error.
func verifyModel(t *testing.T, env *testEnv, digest, modelID string) error {
	t.Helper()
	_, err := env.svc.VerifyAPIKey(context.Background(), &authv1.VerifyAPIKeyRequest{
		KeyDigest: digest,
		Model:     modelID,
	})
	return err
}

// TestVerifyAPIKeyModelGate covers the data-plane half of feature-13:
// AC7 (a restricted model rejects a non-granted organization's key), AC8
// (a granted organization's key passes) and AC9 (zero grants stay open).
// It also pins the transitional behaviour: with no authorizer wired the
// model field stays accepted-but-ignored.
func TestVerifyAPIKeyModelGate(t *testing.T) {
	env := newTestService(t)
	ctx := orgCtx("org-a")

	created, err := env.svc.CreateAPIKey(ctx, &authv1.CreateAPIKeyRequest{Name: "k"})
	require.NoError(t, err)
	digest := KeyDigest(created.GetApiKey())

	// No authorizer: the model field is ignored (transitional mode).
	require.NoError(t, verifyModel(t, env, digest, "m-restricted"))

	// With an authorizer wired, a restricted model the key's org is not
	// granted is rejected with 10105.
	authorizer := &fakeModelAuthorizer{denied: map[string]bool{"org-a|m-restricted": true}}
	env.svc.SetModelAuthorizer(authorizer)

	err = verifyModel(t, env, digest, "m-restricted")
	require.Error(t, err, "AC7: the restricted model must be refused")
	ae, ok := apierrors.As(err)
	require.True(t, ok, "must be a business error")
	assert.Equal(t, apierrors.CodeModelUnauthorized, ae.Code, "AC7: 10105 MODEL_UNAUTHORIZED")
	assert.Equal(t, "model not authorized", ae.Message)

	// AC8: the granted organization's key passes.
	require.NoError(t, verifyModel(t, env, digest, "m-granted"), "AC8: a granted model is forwarded")

	// AC9: a model with zero grants is open to everyone.
	require.NoError(t, verifyModel(t, env, digest, "m-open"), "AC9: default-allow is preserved")

	// No model named: the gate does not run at all.
	require.NoError(t, verifyModel(t, env, digest, ""), "an empty model field skips the gate")

	// The key verdict cache must not bypass the gate: verification still
	// succeeds for the granted model on the cached path.
	require.NoError(t, verifyModel(t, env, digest, "m-granted"))
}

// TestVerifyAPIKeyModelGateAuthorizerError pins that an authorizer
// failure is surfaced, never silently treated as allowed.
func TestVerifyAPIKeyModelGateAuthorizerError(t *testing.T) {
	env := newTestService(t)
	ctx := orgCtx("org-a")

	created, err := env.svc.CreateAPIKey(ctx, &authv1.CreateAPIKeyRequest{Name: "k"})
	require.NoError(t, err)
	env.svc.SetModelAuthorizer(&fakeModelAuthorizer{err: errors.New("db down")})

	err = verifyModel(t, env, KeyDigest(created.GetApiKey()), "m-1")
	require.Error(t, err, "an authorizer failure must not be an implicit allow")
	ae, ok := apierrors.As(err)
	assert.False(t, ok, "the raw error is surfaced: %v", ae)
}

// TestModelAuthCacheConsultsAndPopulates covers AD6: the per-(org, model)
// verdict is cached — both the allow and the deny verdict — so the hot
// path does not re-read the database per request, and the cache key is
// scoped per organization.
func TestModelAuthCacheConsultsAndPopulates(t *testing.T) {
	env := newTestService(t)
	ctx := orgCtx("org-a")

	created, err := env.svc.CreateAPIKey(ctx, &authv1.CreateAPIKeyRequest{Name: "k"})
	require.NoError(t, err)
	digest := KeyDigest(created.GetApiKey())

	cache := newFakeModelAuthCache()
	env.svc.modelAuthCache = cache
	authorizer := &fakeModelAuthorizer{denied: map[string]bool{"org-a|m-restricted": true}}
	env.svc.SetModelAuthorizer(authorizer)

	// First call populates the cache with the denial.
	err = verifyModel(t, env, digest, "m-restricted")
	require.Error(t, err)
	assert.Equal(t, 1, authorizer.calls)
	require.NotNil(t, env.svc.modelAuthCache, "the cache is wired")

	// Second call is answered from the cache: the authorizer is not
	// consulted again, and the verdict is unchanged.
	err = verifyModel(t, env, digest, "m-restricted")
	require.Error(t, err)
	assert.Equal(t, 1, authorizer.calls, "AD6: a cached denial is served without a re-check")

	// A different organization is a different cache entry.
	otherCreated, err := env.svc.CreateAPIKey(orgCtx("org-b"), &authv1.CreateAPIKeyRequest{Name: "kb"})
	require.NoError(t, err)
	require.NoError(t, verifyModel(t, env, KeyDigest(otherCreated.GetApiKey()), "m-restricted"),
		"the cache key is scoped per organization")
	assert.Equal(t, 2, authorizer.calls, "AD6: another organization re-checks")

	// An expired entry falls back to the authorizer.
	expired := newFakeModelAuthCache()
	expired.entries[modelAuthCacheKey("org-a", "m-restricted")] = fakeModelAuthEntry{
		allowed: false, deadline: time.Now().Add(-time.Second),
	}
	env.svc.modelAuthCache = expired
	err = verifyModel(t, env, digest, "m-restricted")
	require.Error(t, err)
	assert.Equal(t, 3, authorizer.calls, "an expired entry is re-checked")
}

// TestModelAuthCacheKey pins the cache key shape: namespaced and scoped
// by both the organization and the model.
func TestModelAuthCacheKey(t *testing.T) {
	assert.Equal(t, "taas:auth:modelauth:org-a:m-1", modelAuthCacheKey("org-a", "m-1"))
	assert.NotEqual(t, modelAuthCacheKey("org-a", "m-1"), modelAuthCacheKey("org-b", "m-1"))
	assert.NotEqual(t, modelAuthCacheKey("org-a", "m-1"), modelAuthCacheKey("org-a", "m-2"))
	assert.NotEqual(t, cacheKeyPrefix+"x", modelAuthCacheKey("", "x"), "namespaces must not collide")
}

// TestModelAuthCacheTTL pins the configured TTL and its default (AD6).
func TestModelAuthCacheTTL(t *testing.T) {
	env := newTestService(t)
	assert.Equal(t, 5*time.Second, env.svc.modelAuthCacheTTL(), "default cache TTL")

	config.SetConfigForTest(&config.Configuration{
		Model: config.ModelConfig{Auth: config.ModelAuthConfig{CacheTTL: 2 * time.Second}},
	})
	t.Cleanup(func() { config.SetConfigForTest(nil) })
	assert.Equal(t, 2*time.Second, env.svc.modelAuthCacheTTL(), "model.auth.cacheTTL is applied")
}

// TestRedisModelAuthCacheRoundTrip covers the Redis-backed cache against
// the package's RESP test server (the API-key cache pattern).
func TestRedisModelAuthCacheRoundTrip(t *testing.T) {
	srv := newFakeRedisServer(t)
	client := redis.NewClient(&redis.Options{Addr: srv.addr})
	t.Cleanup(func() { _ = client.Close() })
	cache := newRedisModelAuthCache(client)
	ctx := context.Background()
	key := modelAuthCacheKey("org-a", "m-1")

	// Miss.
	verdict, err := cache.Get(ctx, key)
	require.NoError(t, err)
	assert.Nil(t, verdict)

	// Denial round-trip.
	require.NoError(t, cache.Set(ctx, key, false, time.Minute))
	verdict, err = cache.Get(ctx, key)
	require.NoError(t, err)
	require.NotNil(t, verdict)
	assert.False(t, *verdict)

	// Allow round-trip overwrites.
	require.NoError(t, cache.Set(ctx, key, true, time.Minute))
	verdict, err = cache.Get(ctx, key)
	require.NoError(t, err)
	require.NotNil(t, verdict)
	assert.True(t, *verdict)

	// A corrupt entry is an error, not a silent verdict.
	srv.mu.Lock()
	srv.entries[key] = "{not json"
	srv.mu.Unlock()
	_, err = cache.Get(ctx, key)
	require.Error(t, err)
}

// TestRedisModelAuthCacheNilClientFailClosed pins that a cache without a
// client errors instead of reporting a verdict; the caller then falls
// back to the authorizer.
func TestRedisModelAuthCacheNilClientFailClosed(t *testing.T) {
	cache := newRedisModelAuthCache(nil)
	_, err := cache.Get(context.Background(), "k")
	require.Error(t, err)
	require.Error(t, cache.Set(context.Background(), "k", true, time.Second))
}

// TestModelAuthCacheForWithoutComponents pins that the cache is optional:
// with no injected cache and no Redis component the gate still runs, just
// uncached.
func TestModelAuthCacheForWithoutComponents(t *testing.T) {
	env := newTestService(t)
	assert.Nil(t, env.svc.modelAuthCacheFor(), "no cache available")
	env.svc.modelAuthCache = newFakeModelAuthCache()
	assert.NotNil(t, env.svc.modelAuthCacheFor(), "an injected cache is reused")
}

// fakeRedisComponent adapts a client to server.RedisComponent.
type fakeRedisComponent struct{ client any }

func (c fakeRedisComponent) Client() any { return c.client }

// TestModelAuthCacheForWiresRedis pins the production wiring: the verdict
// cache is built lazily from the shared Redis component, and a component
// that exposes the wrong client type degrades to "no cache" instead of
// panicking.
func TestModelAuthCacheForWiresRedis(t *testing.T) {
	srv := newFakeRedisServer(t)
	client := redis.NewClient(&redis.Options{Addr: srv.addr})
	t.Cleanup(func() { _ = client.Close() })

	svc := &Service{components: &fakeComponents{redis: fakeRedisComponent{client: client}}}
	cache := svc.modelAuthCacheFor()
	require.NotNil(t, cache, "the Redis component must yield a cache")
	ctx := context.Background()
	require.NoError(t, cache.Set(ctx, modelAuthCacheKey("org-a", "m-1"), true, time.Minute))
	verdict, err := cache.Get(ctx, modelAuthCacheKey("org-a", "m-1"))
	require.NoError(t, err)
	require.NotNil(t, verdict)
	assert.True(t, *verdict)
	assert.NotNil(t, svc.modelAuthCacheFor(), "the wired cache is memoized")

	// An unexpected client type is not a cache.
	wrongType := &Service{components: &fakeComponents{redis: fakeRedisComponent{client: "not-a-client"}}}
	assert.Nil(t, wrongType.modelAuthCacheFor())
	assert.Nil(t, (&Service{components: &fakeComponents{}}).modelAuthCacheFor())
}

// failingModelAuthCache fails every operation, exercising the degraded
// cache path.
type failingModelAuthCache struct{}

func (failingModelAuthCache) Get(context.Context, string) (*bool, error) {
	return nil, errors.New("redis down")
}

func (failingModelAuthCache) Set(context.Context, string, bool, time.Duration) error {
	return errors.New("redis down")
}

// TestModelAuthCacheFailureIsNotAnAllow pins that a broken cache degrades
// to the uncached check in both directions: it never turns a denial into
// an allow, and it never blocks a granted organization.
func TestModelAuthCacheFailureIsNotAnAllow(t *testing.T) {
	env := newTestService(t)
	ctx := orgCtx("org-a")
	created, err := env.svc.CreateAPIKey(ctx, &authv1.CreateAPIKeyRequest{Name: "k"})
	require.NoError(t, err)
	digest := KeyDigest(created.GetApiKey())

	env.svc.modelAuthCache = failingModelAuthCache{}
	env.svc.SetModelAuthorizer(&fakeModelAuthorizer{denied: map[string]bool{"org-a|m-1": true}})

	err = verifyModel(t, env, digest, "m-1")
	require.Error(t, err, "a cache failure must not swallow the denial")
	ae, ok := apierrors.As(err)
	require.True(t, ok)
	assert.Equal(t, apierrors.CodeModelUnauthorized, ae.Code)

	require.NoError(t, verifyModel(t, env, digest, "m-2"),
		"a cache failure must not block a granted organization")
}
