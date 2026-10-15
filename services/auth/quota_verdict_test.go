package auth

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	authv1 "github.com/go-taas/go-taas/proto/taas/auth/v1"

	"github.com/go-taas/go-taas/services/tenancy"
)

// fakeOrgQuotaReader returns a fixed quota for the org.
type fakeOrgQuotaReader struct {
	quota tenancy.OrgQuota
	err   error
}

func (f fakeOrgQuotaReader) OrgQuota(context.Context, string) (tenancy.OrgQuota, error) {
	return f.quota, f.err
}

// TestVerifyAPIKeyQuotaFields covers the verdict extension (feature #51,
// AD12): the org's hot-path caps ride the VerifyAPIKey response on the
// cache-miss path.
func TestVerifyAPIKeyQuotaFields(t *testing.T) {
	env := newTestService(t)
	ctx := orgCtx("org-a")

	created, err := env.svc.CreateAPIKey(ctx, &authv1.CreateAPIKeyRequest{Name: "quota-key"})
	require.NoError(t, err)
	digest := KeyDigest(created.GetApiKey())

	// Wire a quota reader returning caps for org-a.
	env.svc.SetOrgQuotaReader(fakeOrgQuotaReader{quota: tenancy.OrgQuota{
		MaxConcurrentRequests:     50,
		MaxOutputTokensPerRequest: 8192,
	}})

	// Cache miss path: the verdict carries the caps.
	resp, err := env.svc.VerifyAPIKey(context.Background(), &authv1.VerifyAPIKeyRequest{KeyDigest: digest})
	require.NoError(t, err)
	assert.Equal(t, int64(50), resp.GetMaxConcurrentRequests())
	assert.Equal(t, int64(8192), resp.GetMaxOutputTokensPerRequest())

	// Cache hit path: the caps deserialize from the cached verdict.
	resp, err = env.svc.VerifyAPIKey(context.Background(), &authv1.VerifyAPIKeyRequest{KeyDigest: digest})
	require.NoError(t, err)
	assert.Equal(t, int64(50), resp.GetMaxConcurrentRequests())
	assert.Equal(t, int64(8192), resp.GetMaxOutputTokensPerRequest())
}

// TestVerifyAPIKeyQuotaReaderNilAndError covers the fail-open rule
// (AD12): a nil reader or a read error degrades to unlimited (0) and
// never fails VerifyAPIKey.
func TestVerifyAPIKeyQuotaReaderNilAndError(t *testing.T) {
	env := newTestService(t)
	ctx := orgCtx("org-a")

	created, err := env.svc.CreateAPIKey(ctx, &authv1.CreateAPIKeyRequest{Name: "quota-key"})
	require.NoError(t, err)
	digest := KeyDigest(created.GetApiKey())

	// Nil reader -> unlimited.
	resp, err := env.svc.VerifyAPIKey(context.Background(), &authv1.VerifyAPIKeyRequest{KeyDigest: digest})
	require.NoError(t, err)
	assert.Equal(t, int64(0), resp.GetMaxConcurrentRequests())
	assert.Equal(t, int64(0), resp.GetMaxOutputTokensPerRequest())

	// Read error -> unlimited, still succeeds.
	env.svc.SetOrgQuotaReader(fakeOrgQuotaReader{err: assert.AnError})
	resp, err = env.svc.VerifyAPIKey(context.Background(), &authv1.VerifyAPIKeyRequest{KeyDigest: digest})
	require.NoError(t, err)
	assert.Equal(t, int64(0), resp.GetMaxConcurrentRequests())
	assert.Equal(t, int64(0), resp.GetMaxOutputTokensPerRequest())
}
