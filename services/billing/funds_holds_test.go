package billing

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	goredis "github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	billingv1 "github.com/go-taas/go-taas/proto/taas/billing/v1"

	"github.com/go-taas/go-taas/pkg/config"
)

// newTestReserver starts an in-memory Redis and returns a reserver bound to
// it. The Lua scripts run on the real interpreter, so the atomicity the
// reservation depends on is what gets exercised.
func newTestReserver(t *testing.T, ttl time.Duration) (*redisFundsReserver, *miniredis.Miniredis) {
	t.Helper()
	server := miniredis.RunT(t)
	client := goredis.NewClient(&goredis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	return newRedisFundsReserver(client, ttl), server
}

// orgBound is the single organization-level ceiling these script tests use.
func orgBound(available int64) []FundsBound {
	return []FundsBound{{Scope: scopeOrganization, AvailableMicros: available}}
}

// enableReservation installs a config with the reservation switch on.
func enableReservation(t *testing.T, ttl time.Duration) {
	t.Helper()
	cfg := &config.Configuration{}
	cfg.Billing.Currency = "USD"
	cfg.Billing.SupportedCurrencies = []string{"USD", "CNY"}
	cfg.Billing.Funds.Reservation = true
	cfg.Billing.Funds.HoldTTL = ttl
	cfg.Billing.Funds.DefaultOutputTokens = 1024
	config.SetConfigForTest(cfg)
	t.Cleanup(func() { config.SetConfigForTest(&config.Configuration{}) })
}

// The reserve script is the atomic decision: the second admission is refused
// because the first one's hold already consumed the balance. Without it both
// would read the same available amount and both would be admitted.
func TestReserveIsAtomic(t *testing.T) {
	reserver, _ := newTestReserver(t, time.Minute)
	ctx := context.Background()

	available := CentsToMicros(10) // ten cents
	ok, err := reserver.Reserve(ctx, "org-a", "req-1", CentsToMicros(6), orgBound(available))
	require.NoError(t, err)
	assert.True(t, ok)

	// The second request wants six more cents; only four are left.
	ok, err = reserver.Reserve(ctx, "org-a", "req-2", CentsToMicros(6), orgBound(available))
	require.NoError(t, err)
	assert.False(t, ok, "the first hold already spent that money")

	outstanding, err := reserver.Outstanding(ctx, "org-a", scopeOrganization)
	require.NoError(t, err)
	assert.Equal(t, CentsToMicros(6), outstanding)

	// Releasing the first frees exactly what it held.
	freed, err := reserver.Release(ctx, "org-a", "req-1")
	require.NoError(t, err)
	assert.Equal(t, CentsToMicros(6), freed)

	ok, err = reserver.Reserve(ctx, "org-a", "req-2", CentsToMicros(6), orgBound(available))
	require.NoError(t, err)
	assert.True(t, ok, "the released money is spendable again")
}

// A retried verification for the same request replaces its hold instead of
// adding a second one: a gateway retry must not reserve twice.
func TestReserveIsIdempotentPerRequest(t *testing.T) {
	reserver, _ := newTestReserver(t, time.Minute)
	ctx := context.Background()

	ok, err := reserver.Reserve(ctx, "org-a", "req-1", CentsToMicros(6), orgBound(CentsToMicros(10)))
	require.NoError(t, err)
	require.True(t, ok)

	ok, err = reserver.Reserve(ctx, "org-a", "req-1", CentsToMicros(6), orgBound(CentsToMicros(10)))
	require.NoError(t, err)
	assert.True(t, ok, "the same request re-reserves its own money")

	outstanding, err := reserver.Outstanding(ctx, "org-a", scopeOrganization)
	require.NoError(t, err)
	assert.Equal(t, CentsToMicros(6), outstanding, "one hold, not two")
}

// A hold whose request never reported usage expires: a crashed engine costs a
// bounded delay, not locked funds.
func TestReserveExpires(t *testing.T) {
	reserver, _ := newTestReserver(t, 50*time.Millisecond)
	ctx := context.Background()

	ok, err := reserver.Reserve(ctx, "org-a", "req-lost", CentsToMicros(5), orgBound(CentsToMicros(10)))
	require.NoError(t, err)
	require.True(t, ok)

	// The clock is Redis's own (miniredis only advances TTLs, not TIME), so
	// the hold is given just enough real time to die.
	time.Sleep(120 * time.Millisecond)

	outstanding, err := reserver.Outstanding(ctx, "org-a", scopeOrganization)
	require.NoError(t, err)
	assert.Zero(t, outstanding, "an unreported request stops holding money")

	// The money is spendable again.
	ok, err = reserver.Reserve(ctx, "org-a", "req-next", CentsToMicros(10), orgBound(CentsToMicros(10)))
	require.NoError(t, err)
	assert.True(t, ok)
}

// Releasing an unknown request (a redelivered usage event, or a request
// admitted before reservations existed) is a no-op.
func TestReleaseUnknownRequestIsNoOp(t *testing.T) {
	reserver, _ := newTestReserver(t, time.Minute)
	freed, err := reserver.Release(context.Background(), "org-a", "req-never-held")
	require.NoError(t, err)
	assert.Zero(t, freed)
}

// A reserved admission is per-request: the auth seam is told so, and the
// gateway stops reusing the verdict.
func TestReservationActiveFollowsConfig(t *testing.T) {
	svc := newBillingTestService(t)
	svc.SetFundsReserver(&fakeFundsReserver{})
	assert.False(t, svc.ReservationActive(), "switched off by default")

	enableReservation(t, time.Minute)
	assert.True(t, svc.ReservationActive())
}

// End to end through the admission seam: a burst that fits once is admitted
// once.
func TestCheckInferenceAllowedReservesTheBalance(t *testing.T) {
	svc := newBillingTestService(t)
	enableReservation(t, time.Minute)
	reserver := &fakeFundsReserver{}
	svc.SetFundsReserver(reserver)
	ctx := context.Background()

	// One cent of balance, a model priced so that a 1024-token output costs
	// one cent: the worst case the hold assumes.
	createTestAccount(t, svc, "org-pre", AccountModePrepaid, 1, 0)
	require.NoError(t, svc.repo.DB(ctx).Create(&PriceEntry{
		ModelID: "model-a", AcceleratorType: defaultCardType,
		InputPricePerMillion: 0, OutputPricePerMillion: 9.765625,
		Currency: "USD", EffectiveFrom: time.Now().Unix() - 60, TiersJSON: []byte("[]"),
	}).Error)

	query := FundsQuery{
		OrganizationID:  "org-pre",
		RequestID:       "req-1",
		Model:           "model-a",
		MaxOutputTokens: 1024,
	}
	allowed, _, err := svc.evaluateAdmission(ctx, query)
	require.NoError(t, err)
	assert.True(t, allowed)
	assert.Equal(t, int64(CentsToMicros(1)), reserver.holds[scopeOrganization+holdFieldSeparator+"req-1"], "1024 tokens at that rate is one cent")

	// The same balance cannot cover a second concurrent request.
	query.RequestID = "req-2"
	allowed, reason, err := svc.evaluateAdmission(ctx, query)
	require.NoError(t, err)
	assert.False(t, allowed)
	assert.Equal(t, "insufficient_funds", reason)
}

// Recording the usage retires the hold: the line's own accrual takes over, so
// the money is counted once.
func TestIngestReleasesTheHold(t *testing.T) {
	svc := newBillingTestService(t)
	enableReservation(t, time.Minute)
	reserver := &fakeFundsReserver{}
	svc.SetFundsReserver(reserver)
	ctx := context.Background()

	_, err := svc.handleMeteringEvent(ctx, &meteringEvent{
		RequestID:       "req-usage",
		OrganizationID:  "org-1",
		APIKeyID:        "key-1",
		ModelID:         "model-a",
		AcceleratorType: defaultCardType,
		Usage:           tokenUsage{PromptTokens: 10, CompletionTokens: 10},
		CompletedAt:     time.Now().Unix(),
	})
	require.NoError(t, err)
	assert.Contains(t, reserver.released, "req-usage")
}

// CheckFunds is a read: it reports the position without reserving anything,
// so the console cannot accidentally hold a tenant's money.
func TestCheckFundsDoesNotReserve(t *testing.T) {
	svc := newBillingTestService(t)
	enableReservation(t, time.Minute)
	reserver := &fakeFundsReserver{}
	svc.SetFundsReserver(reserver)
	createTestAccount(t, svc, "org-pre", AccountModePrepaid, 100, 0)

	resp, err := svc.CheckFunds(context.Background(), &billingv1.CheckFundsRequest{OrganizationId: "org-pre"})
	require.NoError(t, err)
	assert.True(t, resp.GetAllowed())
	assert.Empty(t, reserver.holds)
}

// fakeFundsReserver models the Redis scripts in memory: holds per
// scope<sep>requestID, all-or-nothing per admission.
type fakeFundsReserver struct {
	holds    map[string]int64
	released []string
	err      error
}

func (f *fakeFundsReserver) Reserve(_ context.Context, _, requestID string, need int64, bounds []FundsBound) (bool, error) {
	if f.err != nil {
		return false, f.err
	}
	if f.holds == nil {
		f.holds = map[string]int64{}
	}
	for _, b := range bounds {
		used := int64(0)
		for field, v := range f.holds {
			scope, rid, _ := strings.Cut(field, holdFieldSeparator)
			if scope == b.Scope && rid != requestID {
				used += v
			}
		}
		if used+need > b.AvailableMicros {
			return false, nil
		}
	}
	for _, b := range bounds {
		f.holds[b.Scope+holdFieldSeparator+requestID] = need
	}
	return true, nil
}

func (f *fakeFundsReserver) Release(_ context.Context, _, requestID string) (int64, error) {
	if f.err != nil {
		return 0, f.err
	}
	f.released = append(f.released, requestID)
	freed := int64(0)
	for field, v := range f.holds {
		if _, rid, _ := strings.Cut(field, holdFieldSeparator); rid == requestID {
			freed += v
			delete(f.holds, field)
		}
	}
	return freed, nil
}

func (f *fakeFundsReserver) Outstanding(_ context.Context, _, scope string) (int64, error) {
	if f.err != nil {
		return 0, f.err
	}
	sum := int64(0)
	for field, v := range f.holds {
		if s, _, _ := strings.Cut(field, holdFieldSeparator); s == scope {
			sum += v
		}
	}
	return sum, nil
}

// A scoped cap is enforced atomically too: the project ceiling runs out even
// though the organization still has plenty of balance.
func TestReserveEnforcesEveryScope(t *testing.T) {
	reserver, _ := newTestReserver(t, time.Minute)
	ctx := context.Background()

	bounds := func(org, project int64) []FundsBound {
		return []FundsBound{
			{Scope: scopeOrganization, AvailableMicros: org},
			{Scope: projectScope("proj-1"), AvailableMicros: project},
		}
	}

	// The project has one cent left, the organization ten.
	ok, err := reserver.Reserve(ctx, "org-a", "req-1", CentsToMicros(1), bounds(CentsToMicros(10), CentsToMicros(1)))
	require.NoError(t, err)
	require.True(t, ok)

	ok, err = reserver.Reserve(ctx, "org-a", "req-2", CentsToMicros(1), bounds(CentsToMicros(10), CentsToMicros(1)))
	require.NoError(t, err)
	assert.False(t, ok, "the project cap is what runs out")

	// A request in another project is unaffected by proj-1's holds.
	other := []FundsBound{
		{Scope: scopeOrganization, AvailableMicros: CentsToMicros(10)},
		{Scope: projectScope("proj-2"), AvailableMicros: CentsToMicros(1)},
	}
	ok, err = reserver.Reserve(ctx, "org-a", "req-3", CentsToMicros(1), other)
	require.NoError(t, err)
	assert.True(t, ok)
}

// All or nothing: a request refused by one scope must not leave holds in the
// others, or the tenant's balance would shrink for a call that never ran.
func TestReserveIsAllOrNothing(t *testing.T) {
	reserver, _ := newTestReserver(t, time.Minute)
	ctx := context.Background()

	ok, err := reserver.Reserve(ctx, "org-a", "req-1", CentsToMicros(5), []FundsBound{
		{Scope: scopeOrganization, AvailableMicros: CentsToMicros(100)},
		{Scope: userScope("user-1"), AvailableMicros: CentsToMicros(1)},
	})
	require.NoError(t, err)
	assert.False(t, ok)

	for _, scope := range []string{scopeOrganization, userScope("user-1")} {
		held, err := reserver.Outstanding(ctx, "org-a", scope)
		require.NoError(t, err)
		assert.Zero(t, held, "scope %q must not keep a hold from a refused admission", scope)
	}
}

// Release frees every scope of the request at once: metering reports one
// request, not one request per cap.
func TestReleaseFreesEveryScope(t *testing.T) {
	reserver, _ := newTestReserver(t, time.Minute)
	ctx := context.Background()

	_, err := reserver.Reserve(ctx, "org-a", "req-1", CentsToMicros(3), []FundsBound{
		{Scope: scopeOrganization, AvailableMicros: CentsToMicros(10)},
		{Scope: userScope("user-1"), AvailableMicros: CentsToMicros(10)},
		{Scope: projectScope("proj-1"), AvailableMicros: CentsToMicros(10)},
		{Scope: projectMemberScope("proj-1", "user-1"), AvailableMicros: CentsToMicros(10)},
	})
	require.NoError(t, err)

	freed, err := reserver.Release(ctx, "org-a", "req-1")
	require.NoError(t, err)
	assert.Equal(t, CentsToMicros(3)*4, freed, "four scopes were held")

	for _, scope := range []string{scopeOrganization, userScope("user-1"), projectScope("proj-1"), projectMemberScope("proj-1", "user-1")} {
		held, err := reserver.Outstanding(ctx, "org-a", scope)
		require.NoError(t, err)
		assert.Zero(t, held)
	}
}

// The admission path holds against every cap that applies, so a burst cannot
// push a project past its own ceiling while the organization is still rich.
func TestAdmissionHoldsAgainstTheProjectCap(t *testing.T) {
	svc := newBillingTestService(t)
	enableReservation(t, time.Minute)
	reserver := &fakeFundsReserver{}
	svc.SetFundsReserver(reserver)
	svc.SetSpendLimitResolver(fakeSpendLimits{projectLimit: 1})
	ctx := context.Background()

	// One dollar of balance: the organization cap is not the binding one.
	createTestAccount(t, svc, "org-pre", AccountModePrepaid, 100, 0)
	require.NoError(t, svc.repo.DB(ctx).Create(&PriceEntry{
		ModelID: "model-a", AcceleratorType: defaultCardType,
		OutputPricePerMillion: 9.765625,
		Currency:              "USD", EffectiveFrom: time.Now().Unix() - 60, TiersJSON: []byte("[]"),
	}).Error)

	query := FundsQuery{
		OrganizationID: "org-pre", ProjectID: "proj-1",
		Model: "model-a", MaxOutputTokens: 1024,
	}
	query.RequestID = "req-1"
	allowed, _, err := svc.evaluateAdmission(ctx, query)
	require.NoError(t, err)
	require.True(t, allowed)

	// The project's one cent is now held: a second request in the same project
	// is refused even though the balance covers it.
	query.RequestID = "req-2"
	allowed, reason, err := svc.evaluateAdmission(ctx, query)
	require.NoError(t, err)
	assert.False(t, allowed)
	assert.Equal(t, "insufficient_funds", reason)

	// Another project still has its own ceiling.
	query.RequestID, query.ProjectID = "req-3", "proj-2"
	allowed, _, err = svc.evaluateAdmission(ctx, query)
	require.NoError(t, err)
	assert.True(t, allowed)
}

// A per-user cap is held the same way, and a different person is unaffected.
func TestAdmissionHoldsAgainstTheUserCap(t *testing.T) {
	svc := newBillingTestService(t)
	enableReservation(t, time.Minute)
	svc.SetFundsReserver(&fakeFundsReserver{})
	svc.SetSpendLimitResolver(fakeSpendLimits{userLimit: 1})
	ctx := context.Background()

	createTestAccount(t, svc, "org-pre", AccountModePrepaid, 100, 0)
	require.NoError(t, svc.repo.DB(ctx).Create(&PriceEntry{
		ModelID: "model-a", AcceleratorType: defaultCardType,
		OutputPricePerMillion: 9.765625,
		Currency:              "USD", EffectiveFrom: time.Now().Unix() - 60, TiersJSON: []byte("[]"),
	}).Error)

	query := FundsQuery{OrganizationID: "org-pre", OwnerUserID: "user-1", Model: "model-a", MaxOutputTokens: 1024}
	query.RequestID = "req-1"
	allowed, _, err := svc.evaluateAdmission(ctx, query)
	require.NoError(t, err)
	require.True(t, allowed)

	query.RequestID = "req-2"
	allowed, _, err = svc.evaluateAdmission(ctx, query)
	require.NoError(t, err)
	assert.False(t, allowed)

	query.RequestID, query.OwnerUserID = "req-3", "user-2"
	allowed, _, err = svc.evaluateAdmission(ctx, query)
	require.NoError(t, err)
	assert.True(t, allowed)
}

// The read-only position report counts held money: the console must show why
// a funded account is being refused.
func TestCheckFundsCountsHolds(t *testing.T) {
	svc := newBillingTestService(t)
	enableReservation(t, time.Minute)
	reserver := &fakeFundsReserver{}
	svc.SetFundsReserver(reserver)
	ctx := context.Background()

	createTestAccount(t, svc, "org-pre", AccountModePrepaid, 1, 0)
	reserver.holds = map[string]int64{scopeOrganization + holdFieldSeparator + "req-1": CentsToMicros(1)}

	resp, err := svc.CheckFunds(ctx, &billingv1.CheckFundsRequest{OrganizationId: "org-pre"})
	require.NoError(t, err)
	assert.False(t, resp.GetAllowed(), "the only cent is held by an in-flight request")
}
