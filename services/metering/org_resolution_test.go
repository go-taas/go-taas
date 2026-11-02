package metering

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/metadata"
)

// noUserResolver reports "no session" the way the auth service does: an
// error, not an empty id.
type noUserResolver struct{}

func (noUserResolver) SessionUserID(context.Context) (string, error) {
	return "", assert.AnError
}

// TestResolveOrgSessionWithoutOrganizationIgnoresHeader covers the
// isolation rule: when a session exists but the account belongs to no
// organization, a client-supplied X-Organization-Id must not frame the
// read around another tenant.
func TestResolveOrgSessionWithoutOrganizationIgnoresHeader(t *testing.T) {
	svc := NewForFVT(nil, nil)
	svc.SetSessionOrgResolver(&fakeMeteringOrgResolver{org: ""})
	svc.SetSessionUserResolver(&fakeMeteringUserResolver{user: "u1"})

	ctx := metadata.NewIncomingContext(context.Background(), metadata.Pairs(
		"x-organization-id", "org-other"))
	org, err := svc.resolveOrg(ctx)
	require.NoError(t, err)
	assert.Empty(t, org, "a session without an organization wins over the header")
}

// TestResolveOrgTransitionalFallsBackToHeader covers the transitional
// path: no session means the header is still the caller context.
func TestResolveOrgTransitionalFallsBackToHeader(t *testing.T) {
	svc := NewForFVT(nil, nil)
	svc.SetSessionOrgResolver(&fakeMeteringOrgResolver{org: ""})
	svc.SetSessionUserResolver(noUserResolver{})

	ctx := metadata.NewIncomingContext(context.Background(), metadata.Pairs(
		"x-organization-id", "org-a"))
	org, err := svc.resolveOrg(ctx)
	require.NoError(t, err)
	assert.Equal(t, "org-a", org)
}
