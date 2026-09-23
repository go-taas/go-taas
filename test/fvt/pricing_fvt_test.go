package fvt

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/grpc-ecosystem/grpc-gateway/v2/runtime"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	"github.com/go-taas/go-taas/pkg/config"
	"github.com/go-taas/go-taas/pkg/grpcmiddleware"
	"github.com/go-taas/go-taas/pkg/mq"
	"github.com/go-taas/go-taas/pkg/server"
	billingv1 "github.com/go-taas/go-taas/proto/taas/billing/v1"
	"github.com/go-taas/go-taas/services/billing"
)

// billingEnv is the in-process stack for the pricing feature: the
// billing service over one gRPC server with the gateway mux in front,
// a shared file-backed SQLite database, and a recording MQ bus with
// the real event and settlements consumers subscribed.
type billingEnv struct {
	db    *gorm.DB
	gwSrv *httptest.Server
	mqBus *recordingBus

	svc      *billing.Service
	recon    *billing.ReconciliationRunner
}

func newBillingEnv(t *testing.T) *billingEnv {
	t.Helper()

	// Install a config with the billing currency (chargeGroup and
	// SetPrice read it).
	cfg := &config.Configuration{}
	cfg.Billing.Currency = "USD"
	config.SetConfigForTest(cfg)
	t.Cleanup(func() { config.SetConfigForTest(&config.Configuration{}) })

	// A file-backed temp database avoids sqlite shared-cache locking
	// between pooled connections.
	dbPath := filepath.Join(t.TempDir(), "billing.db")
	db, err := gorm.Open(sqlite.Open(dbPath), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, billing.MigrateSchemaForFVT(db))
	sqlDB, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })

	// Real gRPC server on an ephemeral port with the production
	// interceptor chain.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	grpcSrv := grpc.NewServer(grpc.ChainUnaryInterceptor(grpcmiddleware.UnaryServerInterceptor()))
	t.Cleanup(grpcSrv.Stop)

	bus := newRecordingBus()
	svc := billing.NewForFVT(db, bus)

	// The real consumers run against the bus, so metering.events and
	// billing.settlements flow through the production path.
	consumerCtx, cancelConsumers := context.WithCancel(context.Background())
	t.Cleanup(cancelConsumers)
	eventConsumer := billing.NewEventConsumer(bus, svc, 1)
	go func() { _ = eventConsumer.Run(consumerCtx) }()
	settlementsConsumer := billing.NewSettlementsConsumer(bus, svc, 1)
	go func() { _ = settlementsConsumer.Run(consumerCtx) }()

	billingv1.RegisterBillingServiceServer(grpcSrv, svc)
	go func() { _ = grpcSrv.Serve(ln) }()

	// Gateway mux in front of the gRPC server.
	conn, err := grpc.NewClient(ln.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })

	mux := runtime.NewServeMux(
		runtime.WithIncomingHeaderMatcher(server.FVTHeaderMatcher),
	)
	require.NoError(t, billingv1.RegisterBillingServiceHandler(context.Background(), mux, conn))

	gwSrv := httptest.NewServer(mux)
	t.Cleanup(gwSrv.Close)

	return &billingEnv{
		db: db, gwSrv: gwSrv, mqBus: bus, svc: svc,
		// Zero grace: the FVT hours are already closed.
		recon: billing.NewReconciliationRunner(billing.NewRepository(db), svc, time.Minute, 0, 1),
	}
}

// call issues a JSON request against the gateway and decodes the body.
func (e *billingEnv) call(t *testing.T, method, path string, body any, org string) (int, map[string]any) {
	t.Helper()
	var rdr *bytes.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		require.NoError(t, err)
		rdr = bytes.NewReader(raw)
	} else {
		rdr = bytes.NewReader(nil)
	}
	req, err := http.NewRequest(method, e.gwSrv.URL+path, rdr)
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	if org != "" {
		req.Header.Set("X-Organization-Id", org)
	}
	resp, err := e.gwSrv.Client().Do(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	out := map[string]any{}
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

// publishMeteringEvent publishes one token-usage event on
// metering.events; the real billing consumer ingests it as a usage
// line.
func (e *billingEnv) publishMeteringEvent(t *testing.T, requestID, orgID, keyID, modelID, card string, completedAt time.Time, prompt, completion int64) {
	t.Helper()
	body, err := json.Marshal(map[string]any{
		"request_id":      requestID,
		"organization_id": orgID,
		"api_key_id":      keyID,
		"model_id":        modelID,
		"accelerator_type": card,
		"completed_at":    completedAt.Unix(),
		"usage": map[string]any{
			"prompt_tokens":     prompt,
			"completion_tokens": completion,
			"cached_tokens":     0,
			"reasoning_tokens":  0,
		},
	})
	require.NoError(t, err)
	require.NoError(t, e.mqBus.Publish(context.Background(), mq.DefaultSubjects().MeteringEvents, body, nil))
}

// publishSettlement publishes one settlement event on
// billing.settlements; the real consumer charges the key-hour.
func (e *billingEnv) publishSettlement(t *testing.T, usageRecordID, keyID, orgID string, periodStart int64) {
	t.Helper()
	body, err := json.Marshal(map[string]any{
		"usage_record_id": usageRecordID,
		"api_key_id":      keyID,
		"organization_id": orgID,
		"period_start":    periodStart,
		"period_end":      periodStart + 3600,
	})
	require.NoError(t, err)
	require.NoError(t, e.mqBus.Publish(context.Background(), mq.DefaultSubjects().Settlements, body, nil))
}

// waitForLines polls until the event consumer has ingested at least n
// usage lines.
func (e *billingEnv) waitForLines(t *testing.T, n int64) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		var count int64
		require.NoError(t, e.db.Table("usage_lines").Count(&count).Error)
		if count >= n {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %d usage lines, got %d", n, count)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// waitForCharges polls until at least n charge records exist.
func (e *billingEnv) waitForCharges(t *testing.T, n int64) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		var count int64
		require.NoError(t, e.db.Table("charge_records").Count(&count).Error)
		if count >= n {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %d charges, got %d", n, count)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// waitForIdle waits until the bus has no in-flight handler calls.
func (e *billingEnv) waitForIdle(t *testing.T) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		e.mqBus.mu.Lock()
		busy := e.mqBus.inFlight > 0
		e.mqBus.mu.Unlock()
		if !busy {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for the bus to go idle")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// TestFVTPricingEndToEnd walks the pricing acceptance criteria through
// the production path: price upsert (AC1/AC2), event ingestion with
// card resolution (AC4/AC5), settlement-driven charging (AC6/AC7),
// tier progression (AC8), the default-card fallback and unpriced
// groups (AC9), idempotent re-delivery (AC10), reconciliation without
// events (AC11), bills and charges queries (AC12/AC13), range
// validation and org isolation (AC16).
func TestFVTPricingEndToEnd(t *testing.T) {
	env := newBillingEnv(t)
	ctx := context.Background()

	now := time.Now().UTC()
	hour1 := now.Add(-3 * time.Hour).Truncate(time.Hour)
	hour2 := now.Add(-2 * time.Hour).Truncate(time.Hour)
	hour3 := now.Add(-4 * time.Hour).Truncate(time.Hour)
	// Prices take effect before the earliest event so ApplicablePrice
	// resolves them.
	priceFrom := hour3.Add(-time.Hour).Unix()

	// AC2: an invalid price is rejected with 10507 and nothing is
	// written.
	code, body := env.call(t, http.MethodPut, "/api/v1/admin/billing/prices", map[string]any{
		"modelId": "model-a", "acceleratorType": "A800",
		"inputPricePerMillion": -1, "outputPricePerMillion": 2,
	}, "")
	assert.NotEqual(t, http.StatusOK, code, "body: %v", body)
	assert.EqualValues(t, float64(10507), body["code"])

	// AC1: set prices — an A800 tiered price for model-a, a default
	// fallback for model-b.
	code, body = env.call(t, http.MethodPut, "/api/v1/admin/billing/prices", map[string]any{
		"modelId": "model-a", "acceleratorType": "A800",
		"inputPricePerMillion": 4, "outputPricePerMillion": 8,
		"effectiveFrom": priceFrom,
		"tiers": []map[string]any{
			{"upToTokens": 1000000, "inputPricePerMillion": 4, "outputPricePerMillion": 8},
			{"upToTokens": 0, "inputPricePerMillion": 2, "outputPricePerMillion": 4},
		},
	}, "")
	require.Equal(t, http.StatusOK, code, "body: %v", body)
	priceA, _ := body["priceId"].(string)
	require.NotEmpty(t, priceA)

	code, body = env.call(t, http.MethodPut, "/api/v1/admin/billing/prices", map[string]any{
		"modelId": "model-b", "acceleratorType": "default",
		"inputPricePerMillion": 1, "outputPricePerMillion": 2,
		"effectiveFrom": priceFrom,
	}, "")
	require.Equal(t, http.StatusOK, code, "body: %v", body)

	// AC1: a repeated save keeps the price_id.
	code, body = env.call(t, http.MethodPut, "/api/v1/admin/billing/prices", map[string]any{
		"modelId": "model-a", "acceleratorType": "A800",
		"inputPricePerMillion": 4, "outputPricePerMillion": 8,
		"effectiveFrom": priceFrom,
		"tiers": []map[string]any{
			{"upToTokens": 1000000, "inputPricePerMillion": 4, "outputPricePerMillion": 8},
			{"upToTokens": 0, "inputPricePerMillion": 2, "outputPricePerMillion": 4},
		},
	}, "")
	require.Equal(t, http.StatusOK, code, "body: %v", body)
	assert.Equal(t, priceA, body["priceId"])

	// FR2: the matrix is queryable — current view.
	code, body = env.call(t, http.MethodGet, "/api/v1/admin/billing/prices", nil, "")
	require.Equal(t, http.StatusOK, code, "body: %v", body)
	prices, _ := body["prices"].([]any)
	assert.Len(t, prices, 2)

	// AC4/AC5: events flow through metering.events with card
	// resolution.
	env.publishMeteringEvent(t, "fvt-r1", "org-fvt", "key-1", "model-a", "A800", hour1.Add(10*time.Minute), 600_000, 0)
	env.publishMeteringEvent(t, "fvt-r2", "org-fvt", "key-1", "model-a", "A800", hour2.Add(10*time.Minute), 600_000, 0)
	// model-b on an unpriced card: the default fallback applies.
	env.publishMeteringEvent(t, "fvt-r3", "org-fvt", "key-1", "model-b", "BI-V150", hour1.Add(10*time.Minute), 1_000_000, 500_000)
	// model-c has no price at all: unpriced.
	env.publishMeteringEvent(t, "fvt-r4", "org-fvt", "key-1", "model-c", "A800", hour1.Add(10*time.Minute), 100, 50)
	// A duplicate delivery converges (AC4).
	env.publishMeteringEvent(t, "fvt-r1", "org-fvt", "key-1", "model-a", "A800", hour1.Add(10*time.Minute), 600_000, 0)
	// An invalid event is skipped.
	env.publishMeteringEvent(t, "", "org-fvt", "key-1", "model-a", "A800", hour1.Add(10*time.Minute), 1, 1)

	env.waitForLines(t, 4)

	// AC6/AC7/AC8: settlement events drive charging with tier
	// progression.
	env.publishSettlement(t, "ur-1", "key-1", "org-fvt", hour1.Unix())
	env.waitForCharges(t, 3) // model-a, model-b, model-c of hour1

	// hour2: month-to-date is now 600K — still tier 0.
	env.publishSettlement(t, "ur-2", "key-1", "org-fvt", hour2.Unix())
	env.waitForCharges(t, 4)

	// AC10: a redelivered settlement event is absorbed.
	env.publishSettlement(t, "ur-1", "key-1", "org-fvt", hour1.Unix())
	env.waitForIdle(t)

	// AC12/AC13: charges are queryable, newest period first.
	code, body = env.call(t, http.MethodGet, "/api/v1/admin/billing/charges", nil, "org-fvt")
	require.Equal(t, http.StatusOK, code, "body: %v", body)
	charges, _ := body["charges"].([]any)
	assert.Len(t, charges, 4, "redelivery adds no charge")
	pageMeta, _ := body["pageMeta"].(map[string]any)
	assert.EqualValues(t, "4", fmt.Sprint(pageMeta["total"]), "int64 fields serialize as strings")

	// Verify the charge contents by (model, period).
	type chargeKey struct{ model, period string }
	byKey := map[chargeKey]map[string]any{}
	for _, c := range charges {
		m, _ := c.(map[string]any)
		key := chargeKey{fmt.Sprint(m["modelId"]), fmt.Sprint(m["periodStart"])}
		byKey[key] = m
	}

	// model-a hour1: 600K prompt at tier 0 (4/1M) = 2.4.
	ma1 := byKey[chargeKey{"model-a", fmt.Sprint(hour1.Unix())}]
	require.NotNil(t, ma1, "model-a hour1 charge missing")
	assert.Equal(t, true, ma1["priced"])
	assert.EqualValues(t, float64(0), ma1["tierIndex"])
	assert.EqualValues(t, 2.4, ma1["amount"])
	assert.Equal(t, "USD", ma1["currency"])
	assert.Equal(t, priceA, ma1["priceId"])

	// model-a hour2: month-to-date 600K < 1M → still tier 0.
	ma2 := byKey[chargeKey{"model-a", fmt.Sprint(hour2.Unix())}]
	require.NotNil(t, ma2)
	assert.EqualValues(t, float64(0), ma2["tierIndex"])
	assert.EqualValues(t, 2.4, ma2["amount"])

	// model-b hour1: default-card fallback (1*1 + 2*0.5 = 2).
	mb1 := byKey[chargeKey{"model-b", fmt.Sprint(hour1.Unix())}]
	require.NotNil(t, mb1)
	assert.Equal(t, true, mb1["priced"])
	assert.Equal(t, "BI-V150", mb1["acceleratorType"], "the line's card is kept")
	assert.EqualValues(t, 2.0, mb1["amount"])

	// model-c hour1: unpriced → amount 0, priced false.
	mc1 := byKey[chargeKey{"model-c", fmt.Sprint(hour1.Unix())}]
	require.NotNil(t, mc1)
	assert.Equal(t, false, mc1["priced"])
	assert.EqualValues(t, 0.0, mc1["amount"])

	// AC12: bills summarize the month.
	thisMonth := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
	code, body = env.call(t, http.MethodGet, fmt.Sprintf("/api/v1/admin/billing/bills?since=%d&until=%d",
		thisMonth.Unix(), now.Unix()), nil, "org-fvt")
	require.Equal(t, http.StatusOK, code, "body: %v", body)
	bills, _ := body["bills"].([]any)
	require.Len(t, bills, 1)
	bill, _ := bills[0].(map[string]any)
	assert.Equal(t, fmt.Sprintf("org-fvt-%s", thisMonth.Format("200601")), bill["billId"])
	assert.InDelta(t, 2.4+2.4+2.0+0.0, bill["amount"], 1e-9)
	assert.EqualValues(t, "4", fmt.Sprint(bill["chargeCount"]), "int64 fields serialize as strings")
	assert.EqualValues(t, "1", fmt.Sprint(bill["unpricedCount"]))

	// AC11: reconciliation prices hours without settlement events.
	env.publishMeteringEvent(t, "fvt-r5", "org-fvt", "key-2", "model-a", "A800", hour3.Add(10*time.Minute), 100_000, 0)
	env.waitForLines(t, 5)
	require.NoError(t, env.recon.ReconcileOnce(ctx))
	env.waitForCharges(t, 5)

	// The reconciled charge is visible under key-2's filter.
	code, body = env.call(t, http.MethodGet, "/api/v1/admin/billing/charges?api_key_id=key-2", nil, "org-fvt")
	require.Equal(t, http.StatusOK, code, "body: %v", body)
	charges, _ = body["charges"].([]any)
	require.Len(t, charges, 1)
	rec, _ := charges[0].(map[string]any)
	assert.Equal(t, "model-a", rec["modelId"])
	assert.EqualValues(t, 0.4, rec["amount"], "100K * 4/1M")

	// 10508: an inverted range is rejected.
	code, body = env.call(t, http.MethodGet,
		fmt.Sprintf("/api/v1/admin/billing/charges?since=%d&until=%d", now.Unix(), now.Unix()-10), nil, "org-fvt")
	assert.NotEqual(t, http.StatusOK, code)
	assert.EqualValues(t, float64(10508), body["code"])

	// FR2.2: the history view returns every version.
	code, body = env.call(t, http.MethodGet, "/api/v1/admin/billing/prices?include_history=true", nil, "")
	require.Equal(t, http.StatusOK, code, "body: %v", body)
	prices, _ = body["prices"].([]any)
	assert.Len(t, prices, 2, "in-place upserts leave no extra versions")
}

// TestFVTPricingOrgIsolation verifies billing queries are scoped to
// the caller organization and reject a missing header (AC16).
func TestFVTPricingOrgIsolation(t *testing.T) {
	env := newBillingEnv(t)

	hour := time.Now().UTC().Add(-3 * time.Hour).Truncate(time.Hour)
	env.publishMeteringEvent(t, "iso-1", "org-a", "key-1", "model-a", "A800", hour.Add(10*time.Minute), 100, 40)
	env.waitForLines(t, 1)
	env.publishSettlement(t, "iso-ur-1", "key-1", "org-a", hour.Unix())
	env.waitForCharges(t, 1)

	// org-b sees nothing.
	code, body := env.call(t, http.MethodGet, "/api/v1/admin/billing/charges", nil, "org-b")
	require.Equal(t, http.StatusOK, code, "body: %v", body)
	charges, _ := body["charges"].([]any)
	assert.Empty(t, charges)

	code, body = env.call(t, http.MethodGet, "/api/v1/admin/billing/bills", nil, "org-b")
	require.Equal(t, http.StatusOK, code, "body: %v", body)
	bills, _ := body["bills"].([]any)
	assert.Empty(t, bills)

	// org-a sees its charge.
	code, body = env.call(t, http.MethodGet, "/api/v1/admin/billing/charges", nil, "org-a")
	require.Equal(t, http.StatusOK, code, "body: %v", body)
	charges, _ = body["charges"].([]any)
	assert.Len(t, charges, 1)

	// A missing organization header is unauthorized (10001).
	code, body = env.call(t, http.MethodGet, "/api/v1/admin/billing/charges", nil, "")
	assert.NotEqual(t, http.StatusOK, code)
	assert.EqualValues(t, float64(10001), body["code"])
}
