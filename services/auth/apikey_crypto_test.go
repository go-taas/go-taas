package auth

import (
	"math"
	"regexp"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/go-taas/go-taas/pkg/config"
)

func TestGenerateAPIKeyFormat(t *testing.T) {
	// AC1: ^sk-[A-Za-z0-9]{43}$ and uniqueness over draws.
	re := regexp.MustCompile(`^sk-[A-Za-z0-9]{43}$`)
	seen := make(map[string]struct{}, 256)
	for i := 0; i < 256; i++ {
		key, err := GenerateAPIKey()
		require.NoError(t, err)
		require.Regexp(t, re, key)
		_, dup := seen[key]
		assert.False(t, dup, "duplicate key generated")
		seen[key] = struct{}{}
	}
}

func TestKeyDigest(t *testing.T) {
	// The contract shared with the Wasm plugin: lowercase hex SHA-256.
	d1 := KeyDigest("sk-abc")
	d2 := KeyDigest("sk-abd")
	assert.Len(t, d1, 64)
	assert.Regexp(t, `^[0-9a-f]{64}$`, d1)
	assert.NotEqual(t, d1, d2)
	// Deterministic.
	assert.Equal(t, d1, KeyDigest("sk-abc"))
}

func TestNewSalt(t *testing.T) {
	s1, err := NewSalt()
	require.NoError(t, err)
	s2, err := NewSalt()
	require.NoError(t, err)
	assert.NotEqual(t, s1, s2, "salts must be fresh per call")
	assert.NotEmpty(t, s1)
}

func TestHashAndVerifyKey(t *testing.T) {
	params := config.Argon2Params{Algorithm: "argon2id", Time: 1, MemoryMiB: 16, Parallelism: 1}
	digest := KeyDigest("sk-test-key")
	salt, err := NewSalt()
	require.NoError(t, err)

	hashed := HashKey(digest, salt, params)
	assert.NotEqual(t, digest, hashed)
	assert.NotEmpty(t, hashed)

	// Correct digest verifies.
	assert.True(t, VerifyKeyHash(digest, salt, hashed, params))
	// Wrong digest fails.
	assert.False(t, VerifyKeyHash(KeyDigest("sk-other"), salt, hashed, params))
	// Wrong salt fails.
	assert.False(t, VerifyKeyHash(digest, "other-salt", hashed, params))
	// Different params produce a different hash.
	other := config.Argon2Params{Algorithm: "argon2id", Time: 2, MemoryMiB: 16, Parallelism: 1}
	assert.NotEqual(t, hashed, HashKey(digest, salt, other))
}

func TestArgon2MemoryKiB(t *testing.T) {
	assert.Equal(t, uint32(16*1024), argon2MemoryKiB(16))
	assert.Equal(t, uint32(64*1024), argon2MemoryKiB(64))
	// Out-of-range values clamp instead of overflowing.
	assert.Equal(t, uint32(math.MaxUint32), argon2MemoryKiB(1<<22))
	assert.Equal(t, uint32(math.MaxUint32), argon2MemoryKiB(-1))
}

func TestIsValidDigest(t *testing.T) {
	assert.True(t, IsValidDigest(KeyDigest("sk-x")))
	assert.False(t, IsValidDigest(""))
	assert.False(t, IsValidDigest("ABC"))                                                            // too short
	assert.False(t, IsValidDigest("ZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZ")) // not hex
}
