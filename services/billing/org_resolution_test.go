package billing

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/metadata"
)

type billingNoSession struct{}

func (billingNoSession) SessionUserID(context.Context) (string, error) {
	return "", assert.AnError
}

// TestResolveOrgSessionWithoutOrganizationIgnoresHeader covers the
// isolation rule on the billing surface.
func TestResolveOrgSessionWithoutOrganizationIgnoresHeader(t *testing.T) {
	svc := NewForFVT(nil, nil)
	svc.SetSessionOrgResolver(fakeSessionOrgResolver{org: ""})
	svc.SetSessionUserResolver(fakeSessionUserResolver{user: "u1"})

	ctx := metadata.NewIncomingContext(context.Background(), metadata.Pairs(
		"x-organization-id", "org-other"))
	org, err := svc.resolveOrg(ctx)
	require.NoError(t, err)
	assert.Empty(t, org)
}

// TestResolveOrgTransitionalFallsBackToHeader covers the transitional path.
func TestResolveOrgTransitionalFallsBackToHeader(t *testing.T) {
	svc := NewForFVT(nil, nil)
	svc.SetSessionOrgResolver(fakeSessionOrgResolver{org: ""})
	svc.SetSessionUserResolver(billingNoSession{})

	ctx := metadata.NewIncomingContext(context.Background(), metadata.Pairs(
		"x-organization-id", "org-a"))
	org, err := svc.resolveOrg(ctx)
	require.NoError(t, err)
	assert.Equal(t, "org-a", org)
}
