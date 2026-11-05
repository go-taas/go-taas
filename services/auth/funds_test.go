package auth

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	authv1 "github.com/go-taas/go-taas/proto/taas/auth/v1"

	apierrors "github.com/go-taas/go-taas/pkg/errors"
	"github.com/go-taas/go-taas/pkg/server"
	"github.com/go-taas/go-taas/services/tenancy"
)

// fakeFundsGate records calls and answers with a scripted decision.
type fakeFundsGate struct {
	allowed bool
	reason  string
	err     error
	calls   int
	lastOrg string
	lastOwn string
	lastPrj string
	// The gateway-side identity the reservation is keyed by.
	lastRequestID string
	lastModel     string
	lastMaxTokens int64
	// reserving mirrors the gate's ReservationActive answer.
	reserving bool
}

// ReservationActive reports whether this fake is acting as a reserving gate.
func (f *fakeFundsGate) ReservationActive() bool { return f.reserving }

func (f *fakeFundsGate) CheckInferenceAllowed(_ context.Context, orgID, ownerID, projectID, requestID, model string, maxOutputTokens int64) (bool, string, error) {
	f.calls++
	f.lastOrg, f.lastOwn, f.lastPrj = orgID, ownerID, projectID
	f.lastRequestID, f.lastModel, f.lastMaxTokens = requestID, model, maxOutputTokens
	return f.allowed, f.reason, f.err
}

// A drained prepaid balance denies the call: the whole point of the gate.
func TestVerifyGatewayKeyDeniesADrainedBalance(t *testing.T) {
	env := newTestService(t)
	created, err := env.svc.CreateAPIKey(orgCtx("org-a"), &authv1.CreateAPIKeyRequest{Name: "gw"})
	require.NoError(t, err)
	gate := &fakeFundsGate{allowed: false, reason: "insufficient_funds"}
	env.svc.SetFundsGate(gate)

	resp, err := env.svc.VerifyGatewayKey(context.Background(), &server.GatewayVerifyRequest{
		KeyDigest: KeyDigest(created.GetApiKey()),
	})
	require.NoError(t, err, "a denial is a verdict, not a platform error")
	assert.False(t, resp.Allowed)
	assert.Equal(t, int(apierrors.CodeInsufficientFunds), resp.Code,
		"10502 is what the plugin maps to HTTP 402")
	assert.Equal(t, 1, gate.calls)
	assert.Equal(t, "org-a", gate.lastOrg)
}

// The gate is asked with the key's attribution so the project and personal
// caps apply to the right person.
func TestFundsGateReceivesAttribution(t *testing.T) {
	env := newTestService(t)
	created, err := env.svc.CreateAPIKey(orgCtx("org-a"), &authv1.CreateAPIKeyRequest{Name: "gw"})
	require.NoError(t, err)
	gate := &fakeFundsGate{allowed: true}
	env.svc.SetFundsGate(gate)

	resp, err := env.svc.VerifyGatewayKey(context.Background(), &server.GatewayVerifyRequest{
		KeyDigest: KeyDigest(created.GetApiKey()),
	})
	require.NoError(t, err)
	assert.True(t, resp.Allowed)
	require.Equal(t, 1, gate.calls)
	assert.Equal(t, "org-a", gate.lastOrg)
}

// Without a wired gate verification behaves exactly as before: the
// transitional deployment must not start rejecting traffic.
func TestVerifyGatewayKeyWithoutFundsGate(t *testing.T) {
	env := newTestService(t)
	created, err := env.svc.CreateAPIKey(orgCtx("org-a"), &authv1.CreateAPIKeyRequest{Name: "gw"})
	require.NoError(t, err)

	resp, err := env.svc.VerifyGatewayKey(context.Background(), &server.GatewayVerifyRequest{
		KeyDigest: KeyDigest(created.GetApiKey()),
	})
	require.NoError(t, err)
	assert.True(t, resp.Allowed)
}

// A billing failure never blocks inference; it is logged and allowed.
func TestFundsGateFailsOpen(t *testing.T) {
	env := newTestService(t)
	created, err := env.svc.CreateAPIKey(orgCtx("org-a"), &authv1.CreateAPIKeyRequest{Name: "gw"})
	require.NoError(t, err)
	env.svc.SetFundsGate(&fakeFundsGate{err: errors.New("billing unreachable")})

	resp, err := env.svc.VerifyGatewayKey(context.Background(), &server.GatewayVerifyRequest{
		KeyDigest: KeyDigest(created.GetApiKey()),
	})
	require.NoError(t, err)
	assert.True(t, resp.Allowed, "a billing outage must not take inference down")
}

// The decision is cached for a few seconds, so a burst costs one read.
func TestFundsGateCachesTheDecision(t *testing.T) {
	env := newTestService(t)
	created, err := env.svc.CreateAPIKey(orgCtx("org-a"), &authv1.CreateAPIKeyRequest{Name: "gw"})
	require.NoError(t, err)
	gate := &fakeFundsGate{allowed: true}
	env.svc.SetFundsGate(gate)
	digest := KeyDigest(created.GetApiKey())

	for i := 0; i < 5; i++ {
		resp, err := env.svc.VerifyGatewayKey(context.Background(),
			&server.GatewayVerifyRequest{KeyDigest: digest})
		require.NoError(t, err)
		require.True(t, resp.Allowed)
	}
	assert.Equal(t, 1, gate.calls, "five verifications in the TTL share one account read")
}

// The synthetic load-test credential is exempt: it generates traffic, it
// does not spend a tenant's balance.
func TestFundsGateSkipsTheSystemCredential(t *testing.T) {
	env := newTestService(t)
	created, err := env.svc.CreateAPIKey(orgCtx("org-a"), &authv1.CreateAPIKeyRequest{Name: "system"})
	require.NoError(t, err)
	require.NoError(t, env.db.Model(&APIKey{}).
		Where("id = ?", created.GetKeyId()).Update("is_system", true).Error)
	gate := &fakeFundsGate{allowed: false, reason: "insufficient_funds"}
	env.svc.SetFundsGate(gate)

	resp, err := env.svc.VerifyGatewayKey(context.Background(), &server.GatewayVerifyRequest{
		KeyDigest: KeyDigest(created.GetApiKey()),
	})
	require.NoError(t, err)
	assert.True(t, resp.Allowed, "the system credential bypasses the funds gate")
	assert.Zero(t, gate.calls)
}

// A denial carries a message an operator can act on.
func TestInsufficientFundsError(t *testing.T) {
	err := insufficientFundsError("insufficient_funds")
	assert.EqualValues(t, apierrors.CodeInsufficientFunds, apierrors.CodeOf(err))
	assert.Contains(t, err.Error(), "insufficient_funds")

	bare := insufficientFundsError("")
	assert.EqualValues(t, apierrors.CodeInsufficientFunds, apierrors.CodeOf(bare))
}

// A request that names itself is never answered from the decision cache: a
// cached "yes" would skip the reservation that makes the admission atomic.
func TestFundsGateBypassesTheCacheForReservedRequests(t *testing.T) {
	env := newTestService(t)
	created, err := env.svc.CreateAPIKey(orgCtx("org-a"), &authv1.CreateAPIKeyRequest{Name: "gw"})
	require.NoError(t, err)
	gate := &fakeFundsGate{allowed: true, reserving: true}
	env.svc.SetFundsGate(gate)
	digest := KeyDigest(created.GetApiKey())

	for i := 0; i < 3; i++ {
		resp, err := env.svc.VerifyGatewayKey(context.Background(), &server.GatewayVerifyRequest{
			KeyDigest: digest,
			Model:     "model-a",
			RequestID: "req-" + string(rune('a'+i)),
			MaxTokens: 512,
		})
		require.NoError(t, err)
		require.True(t, resp.Allowed)
		assert.True(t, resp.Reserve, "the plugin must not reuse a reserved verdict")
	}
	assert.Equal(t, 3, gate.calls, "each admitted request takes its own hold")
	assert.Equal(t, "model-a", gate.lastModel)
	assert.Equal(t, int64(512), gate.lastMaxTokens)
	assert.NotEmpty(t, gate.lastRequestID)
}

// Without reservation the cached verdict stays reusable, so a burst still
// costs one control-plane read.
func TestFundsGateKeepsTheCacheWithoutReservation(t *testing.T) {
	env := newTestService(t)
	created, err := env.svc.CreateAPIKey(orgCtx("org-a"), &authv1.CreateAPIKeyRequest{Name: "gw"})
	require.NoError(t, err)
	gate := &fakeFundsGate{allowed: true}
	env.svc.SetFundsGate(gate)
	digest := KeyDigest(created.GetApiKey())

	for i := 0; i < 3; i++ {
		resp, err := env.svc.VerifyGatewayKey(context.Background(), &server.GatewayVerifyRequest{
			KeyDigest: digest,
			Model:     "model-a",
			RequestID: "req-" + string(rune('a'+i)),
		})
		require.NoError(t, err)
		require.True(t, resp.Allowed)
		assert.False(t, resp.Reserve)
	}
	assert.Equal(t, 1, gate.calls)
}

// The reservation is sized by what the request may actually produce: the
// gateway clamps max_tokens to the organization's cap, so the hold must use
// the clamped figure or it would refuse callers who can afford the call.
func TestFundsGateReservesTheClampedOutputSize(t *testing.T) {
	env := newTestService(t)
	created, err := env.svc.CreateAPIKey(orgCtx("org-a"), &authv1.CreateAPIKeyRequest{Name: "gw"})
	require.NoError(t, err)
	env.svc.SetOrgQuotaReader(fakeOrgQuotaReader{quota: tenancy.OrgQuota{
		MaxOutputTokensPerRequest: 1024,
	}})
	gate := &fakeFundsGate{allowed: true, reserving: true}
	env.svc.SetFundsGate(gate)

	resp, err := env.svc.VerifyGatewayKey(context.Background(), &server.GatewayVerifyRequest{
		KeyDigest: KeyDigest(created.GetApiKey()),
		Model:     "model-a",
		RequestID: "req-big",
		MaxTokens: 100000,
	})
	require.NoError(t, err)
	require.True(t, resp.Allowed)
	assert.Equal(t, int64(1024), gate.lastMaxTokens,
		"the hold covers the clamped output, not what the caller asked for")
}
