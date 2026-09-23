package auth

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFakeVerdictCache(t *testing.T) {
	c := newFakeVerdictCache()
	ctx := context.Background()

	// Miss on empty.
	v, err := c.Get(ctx, "d")
	require.NoError(t, err)
	assert.Nil(t, v)

	// Set then hit.
	verdict := &keyVerdict{OrganizationID: "org", KeyID: "k", Role: "agent"}
	require.NoError(t, c.Set(ctx, "d", verdict, time.Minute))
	got, err := c.Get(ctx, "d")
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, verdict, got)

	// Delete then miss.
	require.NoError(t, c.Delete(ctx, "d"))
	v, err = c.Get(ctx, "d")
	require.NoError(t, err)
	assert.Nil(t, v)
}

func TestCacheKeyFormat(t *testing.T) {
	assert.Equal(t, "taas:auth:apikey:"+KeyDigest("sk-x"), cacheKey(KeyDigest("sk-x")))
}

func TestRedisVerdictCacheNilClient(t *testing.T) {
	// A nil client must fail closed (error), never succeed silently.
	c := &redisVerdictCache{client: nil, ttl: time.Second}
	err := c.Set(context.Background(), "d", &keyVerdict{}, time.Second)
	require.Error(t, err)
}
