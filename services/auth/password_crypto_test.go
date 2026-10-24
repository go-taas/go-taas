package auth

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/go-taas/go-taas/pkg/config"
)

func TestHashAndVerifyPassword(t *testing.T) {
	params := config.Argon2Params{Algorithm: "argon2id", Time: 1, MemoryMiB: 16, Parallelism: 1}

	hash, err := HashPassword("correct horse battery staple", params)
	require.NoError(t, err)
	assert.Contains(t, hash, "argon2id$")

	// Correct password verifies.
	assert.True(t, VerifyPassword("correct horse battery staple", hash))

	// Wrong password fails.
	assert.False(t, VerifyPassword("wrong password", hash))

	// Empty password fails.
	assert.False(t, VerifyPassword("", hash))
}

func TestHashPasswordUniqueSalt(t *testing.T) {
	params := config.Argon2Params{Algorithm: "argon2id", Time: 1, MemoryMiB: 16, Parallelism: 1}

	h1, err := HashPassword("same-password", params)
	require.NoError(t, err)
	h2, err := HashPassword("same-password", params)
	require.NoError(t, err)

	// Different salts produce different hashes for the same password.
	assert.NotEqual(t, h1, h2)
	// Both verify.
	assert.True(t, VerifyPassword("same-password", h1))
	assert.True(t, VerifyPassword("same-password", h2))
}

func TestVerifyPasswordMalformed(t *testing.T) {
	// Malformed stored hashes never verify.
	assert.False(t, VerifyPassword("pw", ""))
	assert.False(t, VerifyPassword("pw", "not-a-hash"))
	assert.False(t, VerifyPassword("pw", "argon2id$v=19$m=16,t=1,p=1$badsalt$badhash"))
	assert.False(t, VerifyPassword("pw", "bcrypt$v=19$m=16,t=1,p=1$c2FsdA==$aGFzaA=="))
}

func TestHashPasswordDefaults(t *testing.T) {
	// Zero-value params fall back to the shipped defaults.
	hash, err := HashPassword("password", config.Argon2Params{})
	require.NoError(t, err)
	assert.True(t, VerifyPassword("password", hash))
}