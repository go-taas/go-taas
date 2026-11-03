package fvt

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/grpc-ecosystem/grpc-gateway/v2/runtime"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	"github.com/go-taas/go-taas/pkg/config"
	"github.com/go-taas/go-taas/pkg/grpcmiddleware"
	"github.com/go-taas/go-taas/pkg/server"
	billingv1 "github.com/go-taas/go-taas/proto/taas/billing/v1"
	meteringv1 "github.com/go-taas/go-taas/proto/taas/metering/v1"
	"github.com/go-taas/go-taas/services/billing"
	"github.com/go-taas/go-taas/services/metering"
	"github.com/go-taas/go-taas/services/tenancy"
)

// dashboardEnv is the in-process stack for the usage dashboard feature:
// both the metering and billing services over one gRPC server with the
// gateway mux in front, a shared file-backed SQLite database, and a
// recording MQ bus. The dashboard reads billing's charge_records and
// price_entries read-only over the shared database (AD1).
type dashboardEnv struct {
	db    *gorm.DB
	gwSrv *httptest.Server
	mqBus *recordingBus

	meteringSvc *metering.Service
	billingSvc  *billing.Service
}

func newDashboardEnv(t *testing.T) *dashboardEnv {
	t.Helper()

	// Install a config with the billing currency.
	cfg := &config.Configuration{}
	cfg.Billing.Currency = "USD"
	config.SetConfigForTest(cfg)
	t.Cleanup(func() { config.SetConfigForTest(&config.Configuration{}) })

	dbPath := filepath.Join(t.TempDir(), "dashboard.db")
	db, err := gorm.Open(sqlite.Open(dbPath), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, metering.MigrateSchemaForFVT(db))
	require.NoError(t, billing.MigrateSchemaForFVT(db))
	require.NoError(t, tenancy.MigrateSchemaForFVT(db))
	for _, orgID := range []string{"org-fvt", "org-a", "org-b"} {
		require.NoError(t, db.Create(&tenancy.Organization{
			ID: orgID, DisplayName: orgID, State: tenancy.StateActive,
		}).Error)
	}
	sqlDB, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	grpcSrv := grpc.NewServer(grpc.ChainUnaryInterceptor(grpcmiddleware.UnaryServerInterceptor()))
	t.Cleanup(grpcSrv.Stop)

	bus := newRecordingBus()
	meteringSvc := metering.NewForFVT(db, bus)
	meteringSvc.SetOrgGuard(tenancy.NewOrgGuard(db))
	meteringSvc.SetCostAttributor(metering.NewCostAttributor(db))
	billingSvc := billing.NewForFVT(db, bus)
	billingSvc.SetOrgGuard(tenancy.NewOrgGuard(db))

	meteringv1.RegisterMeteringServiceServer(grpcSrv, meteringSvc)
	billingv1.RegisterBillingServiceServer(grpcSrv, billingSvc)
	go func() { _ = grpcSrv.Serve(ln) }()

	conn, err := grpc.NewClient(ln.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })

	mux := runtime.NewServeMux(
		runtime.WithIncomingHeaderMatcher(server.FVTHeaderMatcher),
	)
	require.NoError(t, meteringv1.RegisterMeteringServiceHandler(context.Background(), mux, conn))
	require.NoError(t, billingv1.RegisterBillingServiceHandler(context.Background(), mux, conn))

	gwSrv := httptest.NewServer(mux)
	t.Cleanup(gwSrv.Close)

	return &dashboardEnv{
		db: db, gwSrv: gwSrv, mqBus: bus,
		meteringSvc: meteringSvc,
		billingSvc:  billingSvc,
	}
}

// call issues a JSON request against the gateway and decodes the body.
func (e *dashboardEnv) call(t *testing.T, method, path string, body any, org string) (int, map[string]any) {
	t.Helper()
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		require.NoError(t, err)
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, e.gwSrv.URL+path, rdr)
	require.NoError(t, err)
	if org != "" {
		req.Header.Set("X-Organization-Id", org)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	var out map[string]any
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&out))
	return resp.StatusCode, out
}

// seedCharge inserts a charge record directly into the shared DB.
func (e *dashboardEnv) seedCharge(t *testing.T, orgID, keyID, modelID, card string, periodStart int64, amount float64, priced bool) {
	t.Helper()
	row := billing.ChargeRecord{
		ID:               uuid.NewString(),
		OrganizationID:   orgID,
		APIKeyID:         keyID,
		ModelID:          modelID,
		AcceleratorType:  card,
		PeriodStart:      periodStart,
		PeriodEnd:        periodStart + 3600,
		PromptTokens:     1000,
		CompletionTokens: 500,
		CachedTokens:     100,
		ReasoningTokens:  50,
		RequestCount:     3,
		Amount:           amount,
		Currency:         "USD",
		TierIndex:        -1,
		Priced:           priced,
	}
	require.NoError(t, e.db.Create(&row).Error)
}

// seedPrice inserts a price entry directly into the shared DB.
func (e *dashboardEnv) seedPrice(t *testing.T, modelID, card string, effectiveFrom int64, in, out, cached float64) {
	t.Helper()
	row := billing.PriceEntry{
		ModelID:               modelID,
		AcceleratorType:       card,
		InputPricePerMillion:  in,
		OutputPricePerMillion: out,
		CachedPricePerMillion: cached,
		Currency:              "USD",
		EffectiveFrom:         effectiveFrom,
		TiersJSON:             []byte("[]"),
	}
	require.NoError(t, e.db.Create(&row).Error)
}

// AC1: the dashboard returns cards and daily buckets summing the charge
// records through the gateway.
func TestFVTDashboardCardsAndBuckets(t *testing.T) {
	env := newDashboardEnv(t)
	day := time.Date(2026, 9, 24, 0, 0, 0, 0, time.UTC)

	env.seedCharge(t, "org-fvt", "key-1", "model-a", "A800", day.Unix(), 1.25, true)
	env.seedCharge(t, "org-fvt", "key-1", "model-a", "A800", day.Add(time.Hour).Unix(), 0.75, true)
	env.seedCharge(t, "org-fvt", "key-2", "model-b", "default", day.Unix(), 2.00, false)

	code, body := env.call(t, "GET",
		fmt.Sprintf("/api/v1/admin/metering/usage-dashboard?since=%d&until=%d", day.Unix(), day.Add(24*time.Hour).Unix()),
		nil, "org-fvt")
	require.Equal(t, 200, code)
	require.Equal(t, float64(0), body["response"].(map[string]any)["code"])

	cards := body["cards"].(map[string]any)
	// 1.25 + 0.75 + 2.00 = 4.00 -> 400 cents.
	assert.Equal(t, "400", cards["totalCostCents"])
	assert.Equal(t, "9", cards["requestCount"])
	// key-2 is unpriced -> 3 requests unpriced.
	assert.Equal(t, "3", cards["unpricedRequestCount"])

	buckets := body["dailyBuckets"].([]any)
	require.Len(t, buckets, 1)
	groups := buckets[0].(map[string]any)["groups"].([]any)
	require.Len(t, groups, 2)
}

// AC2: group-by model regroups the buckets by model_id.
func TestFVTDashboardGroupByModel(t *testing.T) {
	env := newDashboardEnv(t)
	day := time.Date(2026, 9, 24, 0, 0, 0, 0, time.UTC)

	env.seedCharge(t, "org-fvt", "key-1", "model-a", "A800", day.Unix(), 1.00, true)
	env.seedCharge(t, "org-fvt", "key-2", "model-a", "A800", day.Unix(), 1.00, true)
	env.seedCharge(t, "org-fvt", "key-3", "model-b", "default", day.Unix(), 3.00, true)

	code, body := env.call(t, "GET",
		fmt.Sprintf("/api/v1/admin/metering/usage-dashboard?since=%d&until=%d&group_by=model", day.Unix(), day.Add(24*time.Hour).Unix()),
		nil, "org-fvt")
	require.Equal(t, 200, code)
	buckets := body["dailyBuckets"].([]any)
	require.Len(t, buckets, 1)
	groups := buckets[0].(map[string]any)["groups"].([]any)
	require.Len(t, groups, 2)
	byModel := map[string]map[string]any{}
	for _, g := range groups {
		gm := g.(map[string]any)
		byModel[gm["groupKey"].(string)] = gm
	}
	assert.Equal(t, "200", byModel["model-a"]["costCents"])
	assert.Equal(t, "300", byModel["model-b"]["costCents"])
}

// AC7: an invalid range returns 10404.
func TestFVTDashboardRangeInvalid(t *testing.T) {
	env := newDashboardEnv(t)
	day := time.Date(2026, 9, 24, 0, 0, 0, 0, time.UTC)

	// since > until.
	code, body := env.call(t, "GET",
		fmt.Sprintf("/api/v1/admin/metering/usage-dashboard?since=%d&until=%d", day.Add(24*time.Hour).Unix(), day.Unix()),
		nil, "org-fvt")
	assert.NotEqual(t, 200, code)
	assert.EqualValues(t, float64(10404), body["code"])

	// range > 92 days.
	code, body = env.call(t, "GET",
		fmt.Sprintf("/api/v1/admin/metering/usage-dashboard?since=%d&until=%d", day.Unix(), day.Add(93*24*time.Hour).Unix()),
		nil, "org-fvt")
	assert.NotEqual(t, 200, code)
	assert.EqualValues(t, float64(10404), body["code"])
}

// AC8: the data_through watermark is the max charged hour.
func TestFVTDashboardWatermark(t *testing.T) {
	env := newDashboardEnv(t)
	day := time.Date(2026, 9, 24, 0, 0, 0, 0, time.UTC)

	env.seedCharge(t, "org-fvt", "key-1", "model-a", "A800", day.Unix(), 1.00, true)
	env.seedCharge(t, "org-fvt", "key-1", "model-a", "A800", day.Add(3*time.Hour).Unix(), 1.00, true)

	code, body := env.call(t, "GET",
		fmt.Sprintf("/api/v1/admin/metering/usage-dashboard?since=%d&until=%d", day.Unix(), day.Add(24*time.Hour).Unix()),
		nil, "org-fvt")
	require.Equal(t, 200, code)
	cards := body["cards"].(map[string]any)
	assert.Equal(t, fmt.Sprintf("%d", day.Add(3*time.Hour).Unix()), cards["dataThrough"])
}

// AC4: vouchers carry estimated cost and priced through the gateway.
func TestFVTDashboardVoucherCost(t *testing.T) {
	env := newDashboardEnv(t)
	at := time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC)
	env.seedPrice(t, "model-a", "default", 0, 3.00, 6.00, 1.00)

	// Ingest a voucher via the metering service.
	_, err := env.meteringSvc.IngestMeteringEvent(context.Background(), &meteringv1.IngestMeteringEventRequest{
		RequestId:      "req-dash-1",
		OrganizationId: "org-fvt",
		ApiKeyId:       "key-1",
		ModelId:        "model-a",
		CompletedAt:    at.Unix(),
		Usage: &meteringv1.TokenUsage{
			PromptTokens:     1000,
			CompletionTokens: 500,
			CachedTokens:     100,
		},
	})
	require.NoError(t, err)

	code, body := env.call(t, "GET",
		fmt.Sprintf("/api/v1/admin/metering/vouchers?since=%d&until=%d", at.Add(-time.Hour).Unix(), at.Add(time.Hour).Unix()),
		nil, "org-fvt")
	require.Equal(t, 200, code)
	vouchers := body["vouchers"].([]any)
	require.Len(t, vouchers, 1)
	v := vouchers[0].(map[string]any)
	assert.Equal(t, "1", v["estimatedCostCents"])
	assert.Equal(t, true, v["priced"])
}

// AC3: org isolation — a second org never sees the first org's data.
func TestFVTDashboardOrgIsolation(t *testing.T) {
	env := newDashboardEnv(t)
	day := time.Date(2026, 9, 24, 0, 0, 0, 0, time.UTC)

	env.seedCharge(t, "org-fvt", "key-1", "model-a", "A800", day.Unix(), 1.00, true)
	env.seedCharge(t, "org-a", "key-9", "model-z", "default", day.Unix(), 99.00, true)

	code, body := env.call(t, "GET",
		fmt.Sprintf("/api/v1/admin/metering/usage-dashboard?since=%d&until=%d", day.Unix(), day.Add(24*time.Hour).Unix()),
		nil, "org-fvt")
	require.Equal(t, 200, code)
	cards := body["cards"].(map[string]any)
	assert.Equal(t, "100", cards["totalCostCents"])
}

// TestFVTDashboardFilters covers the usage page's model and API-key filters:
// the cards, the buckets and the lifetime total all narrow together, so a
// filtered page never shows one model's cost next to every model's tokens.
func TestFVTDashboardFilters(t *testing.T) {
	env := newDashboardEnv(t)
	day := time.Date(2026, 9, 24, 0, 0, 0, 0, time.UTC)
	old := day.Add(-45 * 24 * time.Hour).Unix()

	env.seedCharge(t, "org-fvt", "key-1", "model-a", "A800", day.Unix(), 1.00, true)
	env.seedCharge(t, "org-fvt", "key-1", "model-b", "A800", day.Unix(), 2.00, true)
	env.seedCharge(t, "org-fvt", "key-2", "model-a", "A800", day.Unix(), 4.00, true)
	// Outside the requested range, inside the lifetime total.
	env.seedCharge(t, "org-fvt", "key-1", "model-a", "A800", old, 10.00, true)

	path := func(query string) string {
		return fmt.Sprintf("/api/v1/admin/metering/usage-dashboard?since=%d&until=%d&group_by=model%s",
			day.Unix(), day.Add(24*time.Hour).Unix(), query)
	}

	// No filter: every model, both keys, range total 7.00, lifetime 17.00.
	code, body := env.call(t, "GET", path(""), nil, "org-fvt")
	require.Equal(t, 200, code)
	cards := body["cards"].(map[string]any)
	assert.Equal(t, "700", cards["totalCostCents"])
	assert.Equal(t, "17000000", cards["lifetimeCostMicros"])
	assert.Len(t, body["dailyBuckets"].([]any)[0].(map[string]any)["groups"].([]any), 2)

	// By model: model-a only, 5.00 in range, 15.00 lifetime.
	code, body = env.call(t, "GET", path("&model_id=model-a"), nil, "org-fvt")
	require.Equal(t, 200, code)
	cards = body["cards"].(map[string]any)
	assert.Equal(t, "500", cards["totalCostCents"])
	assert.Equal(t, "15000000", cards["lifetimeCostMicros"])
	groups := body["dailyBuckets"].([]any)[0].(map[string]any)["groups"].([]any)
	require.Len(t, groups, 1)
	assert.Equal(t, "model-a", groups[0].(map[string]any)["groupKey"])

	// By key: key-1 only, 3.00 in range, 13.00 lifetime.
	code, body = env.call(t, "GET", path("&api_key_id=key-1"), nil, "org-fvt")
	require.Equal(t, 200, code)
	cards = body["cards"].(map[string]any)
	assert.Equal(t, "300", cards["totalCostCents"])
	assert.Equal(t, "13000000", cards["lifetimeCostMicros"])

	// Both: key-1 on model-a, 1.00 in range, 11.00 lifetime.
	code, body = env.call(t, "GET", path("&model_id=model-a&api_key_id=key-1"), nil, "org-fvt")
	require.Equal(t, 200, code)
	cards = body["cards"].(map[string]any)
	assert.Equal(t, "100", cards["totalCostCents"])
	assert.Equal(t, "11000000", cards["lifetimeCostMicros"])
}

// TestFVTDashboardGroupByUserAndProject covers the two dimensions the console
// offered: filtering by them must regroup instead of silently falling back to
// the API key.
func TestFVTDashboardGroupByUserAndProject(t *testing.T) {
	env := newDashboardEnv(t)
	day := time.Date(2026, 9, 24, 0, 0, 0, 0, time.UTC)
	owner := uuid.NewString()
	other := uuid.NewString()

	seed := func(keyID, modelID string, amount float64, ownerID, projectID string) {
		require.NoError(t, env.db.Create(&billing.ChargeRecord{
			ID: uuid.NewString(), OrganizationID: "org-fvt", APIKeyID: keyID, ModelID: modelID,
			AcceleratorType: "A800", PeriodStart: day.Unix(), PeriodEnd: day.Unix() + 3600,
			RequestCount: 1, Amount: amount, Currency: "USD", TierIndex: -1, Priced: true,
			OwnerUserID: &ownerID, ProjectID: &projectID,
		}).Error)
	}
	seed("key-1", "model-a", 1.00, owner, "proj-1")
	seed("key-2", "model-a", 2.00, other, "proj-2")

	code, body := env.call(t, "GET",
		fmt.Sprintf("/api/v1/admin/metering/usage-dashboard?since=%d&until=%d&group_by=user",
			day.Unix(), day.Add(24*time.Hour).Unix()), nil, "org-fvt")
	require.Equal(t, 200, code)
	groups := body["dailyBuckets"].([]any)[0].(map[string]any)["groups"].([]any)
	require.Len(t, groups, 2)
	keys := []string{}
	for _, g := range groups {
		keys = append(keys, g.(map[string]any)["groupKey"].(string))
	}
	assert.ElementsMatch(t, []string{owner, other}, keys)

	code, body = env.call(t, "GET",
		fmt.Sprintf("/api/v1/admin/metering/usage-dashboard?since=%d&until=%d&group_by=project",
			day.Unix(), day.Add(24*time.Hour).Unix()), nil, "org-fvt")
	require.Equal(t, 200, code)
	groups = body["dailyBuckets"].([]any)[0].(map[string]any)["groups"].([]any)
	require.Len(t, groups, 2)
	keys = []string{}
	for _, g := range groups {
		keys = append(keys, g.(map[string]any)["groupKey"].(string))
	}
	assert.ElementsMatch(t, []string{"proj-1", "proj-2"}, keys)
}
