package infer

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/go-taas/go-taas/services/tenancy"
)

// fakeQuotaResolver is the three-level quota seam.
type fakeQuotaResolver struct {
	quota tenancy.EffectiveQuota
	err   error
	calls int
}

func (f *fakeQuotaResolver) EffectiveQuota(_ context.Context, _, _, _ string) (tenancy.EffectiveQuota, error) {
	f.calls++
	return f.quota, f.err
}

// TestServiceQuotaLimitUsesEffectiveValue covers the personal tier: the
// service-count check reads the combined limit, not the organization row.
func TestServiceQuotaLimitUsesEffectiveValue(t *testing.T) {
	svc := NewForFVT(nil, nil)
	resolver := &fakeQuotaResolver{quota: tenancy.EffectiveQuota{MaxInferenceServices: 3}}
	svc.SetQuotaResolver(resolver)

	limit, err := svc.serviceQuotaLimit(context.Background(), "org-a")
	require.NoError(t, err)
	assert.EqualValues(t, 3, limit)
	assert.Equal(t, 1, resolver.calls)
}

// TestServiceQuotaLimitFallsBackToOrganization covers the degradation
// rule: a resolver failure keeps the organization-only rule.
func TestServiceQuotaLimitFallsBackToOrganization(t *testing.T) {
	svc := NewForFVT(nil, nil)
	svc.SetQuotaResolver(&fakeQuotaResolver{err: assert.AnError})
	svc.SetOrgQuotaReader(fakeInferOrgQuotaReader{quota: tenancy.OrgQuota{MaxInferenceServices: 9}})

	limit, err := svc.serviceQuotaLimit(context.Background(), "org-a")
	require.NoError(t, err)
	assert.EqualValues(t, 9, limit)
}
