package tenancy

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/metadata"
)

type tenancyNoSession struct{}

func (tenancyNoSession) SessionUserID(context.Context) (string, error) {
	return "", assert.AnError
}

func (tenancyNoSession) SessionUserEmail(context.Context) (string, error) {
	return "", assert.AnError
}

// TestResolveOrgSessionWithoutOrganizationIgnoresHeader covers the rule
// that a session with no organization beats a client-supplied header.
func TestResolveOrgSessionWithoutOrganizationIgnoresHeader(t *testing.T) {
	svc := NewForFVT(nil)
	svc.SetSessionOrgResolver(fakeQuotaSessionOrgResolver{org: ""})
	svc.SetSessionResolver(fakeQuotaSessionResolver{user: "u1"})

	ctx := metadata.NewIncomingContext(context.Background(), metadata.Pairs(
		"x-organization-id", "org-other"))
	org, err := svc.resolveOrg(ctx)
	require.NoError(t, err)
	assert.Empty(t, org)
}

// TestResolveOrgTransitionalFallsBackToHeader covers the transitional path.
func TestResolveOrgTransitionalFallsBackToHeader(t *testing.T) {
	svc := NewForFVT(nil)
	svc.SetSessionOrgResolver(fakeQuotaSessionOrgResolver{org: ""})
	svc.SetSessionResolver(tenancyNoSession{})

	ctx := metadata.NewIncomingContext(context.Background(), metadata.Pairs(
		"x-organization-id", "org-a"))
	org, err := svc.resolveOrg(ctx)
	require.NoError(t, err)
	assert.Equal(t, "org-a", org)
}
