// Data-plane funds gate full-verification suite.
//
// The gap this covers: metering settles each *closed* hour, so the money a
// caller spends in the current hour is not deducted yet. Without a gate on
// the verification path a tenant whose prepaid balance is already zero keeps
// calling inference until the next settlement pass — and then overdraws.
//
// The test wires the real auth and billing services over one database and
// asks the gateway verdict, which is exactly what the plugin calls.

package fvt

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/metadata"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	"github.com/go-taas/go-taas/pkg/config"
	apierrors "github.com/go-taas/go-taas/pkg/errors"
	"github.com/go-taas/go-taas/pkg/server"
	authv1 "github.com/go-taas/go-taas/proto/taas/auth/v1"
	billingv1 "github.com/go-taas/go-taas/proto/taas/billing/v1"
	"github.com/go-taas/go-taas/services/auth"
	"github.com/go-taas/go-taas/services/billing"
	"github.com/go-taas/go-taas/services/tenancy"
)

// metadataWithOrg builds a caller context carrying the transitional
// organization header, which is how the services resolve the tenant for
// in-process calls.
func metadataWithOrg(orgID string) context.Context {
	return metadata.NewIncomingContext(context.Background(),
		metadata.Pairs("x-organization-id", orgID))
}

// fundsGateEnv is the auth + billing stack the gate spans.
type fundsGateEnv struct {
	auth    *auth.Service
	billing *billing.Service
	db      *gorm.DB
}

func newFundsGateEnv(t *testing.T) *fundsGateEnv {
	t.Helper()

	// CreateAccount reads the billing currency from the config.
	cfg := &config.Configuration{}
	cfg.Billing.Currency = "USD"
	config.SetConfigForTest(cfg)
	t.Cleanup(func() { config.SetConfigForTest(&config.Configuration{}) })

	db, err := gorm.Open(sqlite.Open(fmt.Sprintf("file:%s?mode=memory&cache=shared", t.Name())), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, auth.MigrateSchemaForFVT(db))
	require.NoError(t, billing.MigrateSchemaForFVT(db))
	require.NoError(t, tenancy.MigrateSchemaForFVT(db))
	for _, orgID := range []string{"org-funded", "org-drained", "org-none"} {
		require.NoError(t, db.Create(&tenancy.Organization{
			ID: orgID, DisplayName: orgID, State: tenancy.StateActive,
		}).Error)
	}
	t.Cleanup(func() {
		sqlDB, _ := db.DB()
		_ = sqlDB.Close()
	})

	authSvc := auth.NewForFVT(db)
	authSvc.SetOrgGuard(tenancy.NewOrgGuard(db))
	billingSvc := billing.NewForFVT(db, nil)
	billingSvc.SetOrgGuard(tenancy.NewOrgGuard(db))
	// The wiring main.go performs.
	authSvc.SetFundsGate(billingSvc)
	return &fundsGateEnv{auth: authSvc, billing: billingSvc, db: db}
}

// createKeyFor mints an API key in an organization and returns its digest.
func (e *fundsGateEnv) createKeyFor(t *testing.T, orgID string) string {
	t.Helper()
	created, err := e.auth.CreateAPIKey(
		metadataWithOrg(orgID), &authv1.CreateAPIKeyRequest{Name: "gate-" + orgID})
	require.NoError(t, err)
	return auth.KeyDigest(created.GetApiKey())
}

// seedPrepaid creates a prepaid account and optionally drains it.
func (e *fundsGateEnv) seedPrepaid(t *testing.T, orgID string, cents int64, drain bool) {
	t.Helper()
	ctx := metadataWithOrg(orgID)
	created, err := e.billing.CreateAccount(ctx, &billingv1.CreateAccountRequest{
		Mode: "prepaid", OverdrawPolicy: "block", InitialBalanceCents: cents,
	})
	require.NoError(t, err)
	if !drain {
		return
	}
	_, err = e.billing.Refund(ctx, &billingv1.RefundRequest{
		AccountId:      created.GetAccount().GetAccountId(),
		AmountCents:    cents,
		IdempotencyKey: "drain-" + orgID,
	})
	require.NoError(t, err)
}

// A drained prepaid balance denies inference at the gateway.
func TestFVTFundsGateBlocksADrainedBalance(t *testing.T) {
	env := newFundsGateEnv(t)
	env.seedPrepaid(t, "org-drained", 100, true)

	resp, err := env.auth.VerifyGatewayKey(context.Background(), &server.GatewayVerifyRequest{
		KeyDigest: env.createKeyFor(t, "org-drained"),
	})
	require.NoError(t, err, "a denial is a verdict, not a platform fault")
	assert.False(t, resp.Allowed)
	assert.Equal(t, int(apierrors.CodeInsufficientFunds), resp.Code,
		"10502 is what the plugin renders as HTTP 402")
}

// A funded prepaid balance keeps working.
func TestFVTFundsGateAllowsAFundedBalance(t *testing.T) {
	env := newFundsGateEnv(t)
	env.seedPrepaid(t, "org-funded", 10000, false)

	resp, err := env.auth.VerifyGatewayKey(context.Background(), &server.GatewayVerifyRequest{
		KeyDigest: env.createKeyFor(t, "org-funded"),
	})
	require.NoError(t, err)
	assert.True(t, resp.Allowed)
}

// An organization with no billing account is ungated: a fresh tenant can
// call inference before anyone opens an account (transitional open mode).
func TestFVTFundsGateLeavesAnUnbilledOrgOpen(t *testing.T) {
	env := newFundsGateEnv(t)

	resp, err := env.auth.VerifyGatewayKey(context.Background(), &server.GatewayVerifyRequest{
		KeyDigest: env.createKeyFor(t, "org-none"),
	})
	require.NoError(t, err)
	assert.True(t, resp.Allowed)
}

// fakeReserver models the Redis reservation in memory, so the FVT can drive
// the admission path without a Redis server. The atomicity it stands in for is
// what services/billing/funds_holds_test.go proves against a real interpreter.
type fakeReserver struct {
	holds map[string]int64
}

// holdSeparator mirrors billing's field separator: scope\x01requestID.
const holdSeparator = "\x01"

func (f *fakeReserver) Reserve(_ context.Context, _, requestID string, need int64, bounds []billing.FundsBound) (bool, error) {
	if f.holds == nil {
		f.holds = map[string]int64{}
	}
	for _, b := range bounds {
		used := int64(0)
		for field, v := range f.holds {
			scope, rid, _ := strings.Cut(field, holdSeparator)
			if scope == b.Scope && rid != requestID {
				used += v
			}
		}
		if used+need > b.AvailableMicros {
			return false, nil
		}
	}
	for _, b := range bounds {
		f.holds[b.Scope+holdSeparator+requestID] = need
	}
	return true, nil
}

func (f *fakeReserver) Release(_ context.Context, _, requestID string) (int64, error) {
	freed := int64(0)
	for field, v := range f.holds {
		if _, rid, _ := strings.Cut(field, holdSeparator); rid == requestID {
			freed += v
			delete(f.holds, field)
		}
	}
	return freed, nil
}

func (f *fakeReserver) Outstanding(_ context.Context, _, scope string) (int64, error) {
	sum := int64(0)
	for field, v := range f.holds {
		if s, _, _ := strings.Cut(field, holdSeparator); s == scope {
			sum += v
		}
	}
	return sum, nil
}

// withoutReservation rebuilds the process config with the reservation switch
// on or off; the FVT shares one process-wide config.
func (e *fundsGateEnv) setReservation(t *testing.T, ttl time.Duration) {
	t.Helper()
	cfg := &config.Configuration{}
	cfg.Billing.Currency = "USD"
	cfg.Billing.Funds.Reservation = true
	cfg.Billing.Funds.HoldTTL = ttl
	cfg.Billing.Funds.DefaultOutputTokens = 1024
	config.SetConfigForTest(cfg)
}

// Reservation closes the burst race: one balance, two concurrent admissions,
// one of them served. Both would pass a plain read.
func TestFVTFundsGateReservesAcrossConcurrentAdmissions(t *testing.T) {
	env := newFundsGateEnv(t)
	env.setReservation(t, 10*time.Minute)
	reserver := &fakeReserver{}
	env.billing.SetFundsReserver(reserver)

	// One cent of balance, and a model priced so 1024 output tokens cost
	// exactly that.
	env.seedPrepaid(t, "org-funded", 1, false)
	require.NoError(t, env.db.Create(&billing.PriceEntry{
		ModelID: "model-a", AcceleratorType: "default",
		OutputPricePerMillion: 9.765625,
		Currency:              "USD",
		EffectiveFrom:         time.Now().Add(-time.Hour).Unix(),
		TiersJSON:             []byte("[]"),
	}).Error)

	digest := env.createKeyFor(t, "org-funded")
	first, err := env.auth.VerifyGatewayKey(context.Background(), &server.GatewayVerifyRequest{
		KeyDigest: digest, Model: "model-a", RequestID: "req-a", MaxTokens: 1024,
	})
	require.NoError(t, err)
	assert.True(t, first.Allowed)
	assert.True(t, first.Reserve, "a held admission must not be reused")

	second, err := env.auth.VerifyGatewayKey(context.Background(), &server.GatewayVerifyRequest{
		KeyDigest: digest, Model: "model-a", RequestID: "req-b", MaxTokens: 1024,
	})
	require.NoError(t, err)
	assert.False(t, second.Allowed, "the first hold already spent the balance")
	assert.Equal(t, int(apierrors.CodeInsufficientFunds), second.Code)

	// Recording the first request's usage retires its hold; the money is
	// spendable again for the second.
	_, err = reserver.Release(context.Background(), "org-funded", "req-a")
	require.NoError(t, err)
	third, err := env.auth.VerifyGatewayKey(context.Background(), &server.GatewayVerifyRequest{
		KeyDigest: digest, Model: "model-a", RequestID: "req-b", MaxTokens: 1024,
	})
	require.NoError(t, err)
	assert.True(t, third.Allowed)
}
