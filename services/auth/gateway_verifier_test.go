// Copyright 2025 The go-taas Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package auth

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	authv1 "github.com/go-taas/go-taas/proto/taas/auth/v1"

	apierrors "github.com/go-taas/go-taas/pkg/errors"
	"github.com/go-taas/go-taas/pkg/server"
)

// TestVerifyGatewayKeyAllowsAValidKey covers the happy path and that the
// limits the plugin enforces locally come back on the verdict.
func TestVerifyGatewayKeyAllowsAValidKey(t *testing.T) {
	env := newTestService(t)
	created, err := env.svc.CreateAPIKey(orgCtx("org-a"), &authv1.CreateAPIKeyRequest{
		Name: "gateway", RateLimitRpm: 60,
	})
	require.NoError(t, err)

	resp, err := env.svc.VerifyGatewayKey(context.Background(), &server.GatewayVerifyRequest{
		KeyDigest: KeyDigest(created.GetApiKey()),
		Model:     "qwen-2.5-0.5b",
		SourceIP:  "10.0.0.9",
	})
	require.NoError(t, err)
	assert.True(t, resp.Allowed)
	assert.Equal(t, "org-a", resp.OrganizationID)
	assert.Equal(t, created.GetKeyId(), resp.KeyID)
	assert.EqualValues(t, 60, resp.RateLimitRPM)
	assert.NotEmpty(t, resp.Role)
}

// TestVerifyGatewayKeyRejectsAnUnknownDigest proves a bad key becomes a
// verdict, not an error: the plugin must answer 401, not fail closed with
// 503 (which would tell the caller the platform is broken).
func TestVerifyGatewayKeyRejectsAnUnknownDigest(t *testing.T) {
	env := newTestService(t)

	resp, err := env.svc.VerifyGatewayKey(context.Background(), &server.GatewayVerifyRequest{
		KeyDigest: KeyDigest("sk-not-a-real-key"),
	})
	require.NoError(t, err)
	assert.False(t, resp.Allowed)
	assert.Equal(t, int(apierrors.CodeAPIKeyNotFound), resp.Code,
		"an unknown digest is a missing key, not a platform fault")
	assert.NotEmpty(t, resp.Message)
}

// TestVerifyGatewayKeyRejectsAMalformedDigest covers the 64-hex rule: a
// truncated or non-hex digest is a rejection, never a lookup.
func TestVerifyGatewayKeyRejectsAMalformedDigest(t *testing.T) {
	env := newTestService(t)

	for _, digest := range []string{"", "deadbeef", "SK-" + repeat("a", 40), "Z" + repeat("a", 63)} {
		resp, err := env.svc.VerifyGatewayKey(context.Background(),
			&server.GatewayVerifyRequest{KeyDigest: digest})
		require.NoError(t, err, "digest %q", digest)
		assert.False(t, resp.Allowed, "digest %q must be rejected", digest)
		assert.Equal(t, int(apierrors.CodeAPIKeyInvalid), resp.Code)
	}
}

// TestVerifyGatewayKeyRejectsARevokedKey covers revocation reaching the
// gateway: this is the whole point of putting verification there.
func TestVerifyGatewayKeyRejectsARevokedKey(t *testing.T) {
	env := newTestService(t)
	created, err := env.svc.CreateAPIKey(orgCtx("org-a"), &authv1.CreateAPIKeyRequest{Name: "gateway"})
	require.NoError(t, err)
	digest := KeyDigest(created.GetApiKey())

	// Populate the positive cache, then revoke: the verdict must flip once
	// the cache entry is gone (the plugin's own cache adds its TTL on top).
	ok, err := env.svc.VerifyGatewayKey(context.Background(), &server.GatewayVerifyRequest{KeyDigest: digest})
	require.NoError(t, err)
	require.True(t, ok.Allowed)

	_, err = env.svc.RevokeAPIKey(orgCtx("org-a"), &authv1.RevokeAPIKeyRequest{KeyId: created.GetKeyId()})
	require.NoError(t, err)
	// Simulate the plugin's own 30s verdict cache expiring: the revocation
	// must be effective on the next call that reaches the server.
	require.NoError(t, env.cache.Delete(context.Background(), digest))

	resp, err := env.svc.VerifyGatewayKey(context.Background(), &server.GatewayVerifyRequest{KeyDigest: digest})
	require.NoError(t, err)
	assert.False(t, resp.Allowed)
	assert.Equal(t, int(apierrors.CodeAPIKeyRevoked), resp.Code)
}

// TestVerifyGatewayKeyRejectsAnOutOfScopeModel covers the per-key model
// allow-list: the gateway is where it must be enforced, and the plugin only
// learns about it because it sends the model name.
func TestVerifyGatewayKeyRejectsAnOutOfScopeModel(t *testing.T) {
	env := newTestService(t)
	created, err := env.svc.CreateAPIKey(orgCtx("org-a"), &authv1.CreateAPIKeyRequest{
		Name: "gateway", Models: []string{"m-allowed"},
	})
	require.NoError(t, err)
	digest := KeyDigest(created.GetApiKey())

	resp, err := env.svc.VerifyGatewayKey(context.Background(),
		&server.GatewayVerifyRequest{KeyDigest: digest, Model: "m-other"})
	require.NoError(t, err)
	assert.False(t, resp.Allowed)
	assert.Equal(t, int(apierrors.CodeAPIKeyModelNotAllowed), resp.Code)

	resp, err = env.svc.VerifyGatewayKey(context.Background(),
		&server.GatewayVerifyRequest{KeyDigest: digest, Model: "m-allowed"})
	require.NoError(t, err)
	assert.True(t, resp.Allowed)
}

// TestIsVerdictCode pins the boundary that decides between "reject" and
// "fail closed". A platform-side code in this list would tell callers their
// key is bad when the platform simply could not check it.
func TestIsVerdictCode(t *testing.T) {
	for _, code := range []apierrors.Code{
		apierrors.CodeAPIKeyNotFound,
		apierrors.CodeAPIKeyInvalid,
		apierrors.CodeAPIKeyRevoked,
		apierrors.CodeAPIKeyExpired,
		apierrors.CodeAPIKeyModelNotAllowed,
		apierrors.CodeModelNotFound,
		apierrors.CodeModelUnauthorized,
	} {
		assert.True(t, isVerdictCode(code), "%v must be a verdict", code)
	}
	for _, code := range []apierrors.Code{
		apierrors.CodeInternal,
		apierrors.CodeOK,
		apierrors.CodeRateLimitExceeded,
		apierrors.CodeForbidden,
		apierrors.CodeUnauthorized,
	} {
		assert.False(t, isVerdictCode(code), "%v must not be a verdict", code)
	}
}

// repeat is a local helper so the test does not import strings for one use.
func repeat(s string, n int) string {
	out := make([]byte, 0, len(s)*n)
	for i := 0; i < n; i++ {
		out = append(out, s...)
	}
	return string(out)
}
