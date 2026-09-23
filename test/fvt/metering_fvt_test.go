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

	"github.com/go-taas/go-taas/pkg/grpcmiddleware"
	"github.com/go-taas/go-taas/pkg/mq"
	"github.com/go-taas/go-taas/pkg/server"
	meteringv1 "github.com/go-taas/go-taas/proto/taas/metering/v1"
	"github.com/go-taas/go-taas/services/metering"
)

// meteringEnv is the in-process stack for the metering feature: the
// metering service over one gRPC server with the gateway mux in front,
// a shared SQLite database, and a recording MQ bus with the real event
// consumer subscribed to metering.events.
type meteringEnv struct {
	db     *gorm.DB
	gwSrv  *httptest.Server
	grpcLn net.Listener
	mqBus  *recordingBus

	svc     *metering.Service
	settler *metering.SettlementRunner
}

func newMeteringEnv(t *testing.T) *meteringEnv {
	t.Helper()

	// A file-backed temp database avoids sqlite shared-cache locking
	// between pooled connections: the consumer, gateway handlers and
	// settlement passes all write concurrently, and "table is locked"
	// under cache=shared is not worth fighting in a test.
	dbPath := filepath.Join(t.TempDir(), "metering.db")
	// The consumer, gateway handlers and settlement passes write
	// concurrently. _busy_timeout makes writers wait for each other,
	// and _txlock=immediate acquires the write lock at BEGIN so a
	// deferred read-to-write upgrade cannot fail with "database is
	// locked" (sqlite does not invoke the busy handler for upgrades).
	db, err := gorm.Open(sqlite.Open(dbPath+"?_busy_timeout=10000&_txlock=immediate&_journal_mode=WAL"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, metering.MigrateSchemaForFVT(db))
	t.Cleanup(func() {
		sqlDB, _ := db.DB()
		_ = sqlDB.Close()
	})

	// Real gRPC server on an ephemeral port with the production
	// interceptor chain (business errors → gRPC status → gateway
	// envelope).
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	grpcSrv := grpc.NewServer(grpc.ChainUnaryInterceptor(grpcmiddleware.UnaryServerInterceptor()))
	t.Cleanup(grpcSrv.Stop)

	bus := newRecordingBus()
	svc := metering.NewForFVT(db, bus)

	// The real event consumer runs against the bus, so metering.events
	// flow through the production path.
	consumerCtx, cancelConsumer := context.WithCancel(context.Background())
	t.Cleanup(cancelConsumer)
	consumer := metering.NewEventConsumer(bus, svc, 1)
	go func() { _ = consumer.Run(consumerCtx) }()

	meteringv1.RegisterMeteringServiceServer(grpcSrv, svc)
	go func() { _ = grpcSrv.Serve(ln) }()

	// Gateway mux in front of the gRPC server.
	conn, err := grpc.NewClient(ln.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })

	mux := runtime.NewServeMux(
		runtime.WithIncomingHeaderMatcher(server.FVTHeaderMatcher),
	)
	require.NoError(t, meteringv1.RegisterMeteringServiceHandler(context.Background(), mux, conn))

	gwSrv := httptest.NewServer(mux)
	t.Cleanup(gwSrv.Close)

	return &meteringEnv{
		db: db, gwSrv: gwSrv, grpcLn: ln, mqBus: bus,
		svc:     svc,
		settler: metering.NewSettlementRunner(metering.NewRepository(db), bus, time.Minute, 0, 2),
	}
}

// call issues a JSON request against the gateway and decodes the body.
func (e *meteringEnv) call(t *testing.T, method, path string, body any, org string) (int, map[string]any) {
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

// publishEvent publishes one token-usage event on metering.events; the
// real consumer ingests it.
func (e *meteringEnv) publishEvent(t *testing.T, requestID, orgID, keyID, modelID string, completedAt time.Time, prompt, completion int64) {
	t.Helper()
	body, err := json.Marshal(map[string]any{
		"request_id":      requestID,
		"organization_id": orgID,
		"api_key_id":      keyID,
		"model_id":        modelID,
		"service_id":      "svc-fvt",
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

// waitForSubscription blocks until the consumer goroutine has registered
// its metering.events handler. Without this the first publish could
// race the Subscribe call and be silently dropped.
func (e *meteringEnv) waitForSubscription(t *testing.T) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		e.mqBus.mu.Lock()
		n := len(e.mqBus.subs[mq.DefaultSubjects().MeteringEvents])
		e.mqBus.mu.Unlock()
		if n > 0 {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for the metering.events subscription")
}

// waitForVouchers polls until the consumer has ingested n vouchers. The
// consumer subscribes from a background goroutine, so the very first
// publish may race the Subscribe call and be dropped.
func (e *meteringEnv) waitForVouchers(t *testing.T, n int64) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		var count int64
		if err := e.db.Model(&metering.Voucher{}).Count(&count).Error; err == nil && count >= n {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %d vouchers", n)
}

// waitForIdle blocks until no in-flight publish dispatch remains.
func (e *meteringEnv) waitForIdle(t *testing.T) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		e.mqBus.mu.Lock()
		inFlight := e.mqBus.inFlight
		e.mqBus.mu.Unlock()
		if inFlight == 0 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for the bus to go idle")
}

// TestFVTMeteringEndToEnd walks the acceptance criteria of the metering
// architecture doc through the production path: MQ ingestion (AC1/AC3),
// hourly settlement + publish (AC4/AC5/AC7), usage queries (AC8/AC9),
// range validation (AC10) and org isolation (AC14).
func TestFVTMeteringEndToEnd(t *testing.T) {
	env := newMeteringEnv(t)
	ctx := context.Background()
	env.waitForSubscription(t)

	// Two closed hours for key-1 and one closed hour for key-2.
	now := time.Now().UTC()
	hour1 := now.Add(-3 * time.Hour).Truncate(time.Hour)
	hour2 := now.Add(-2 * time.Hour).Truncate(time.Hour)

	// AC1/AC3: events flow through metering.events and the real
	// consumer ingests them.
	env.publishEvent(t, "fvt-r1", "org-fvt", "key-1", "model-a", hour1.Add(10*time.Minute), 100, 40)
	env.publishEvent(t, "fvt-r2", "org-fvt", "key-1", "model-a", hour1.Add(20*time.Minute), 200, 60)
	env.publishEvent(t, "fvt-r3", "org-fvt", "key-1", "model-b", hour2.Add(10*time.Minute), 50, 20)
	env.publishEvent(t, "fvt-r4", "org-fvt", "key-2", "model-a", hour1.Add(15*time.Minute), 70, 30)

	// AC1: a duplicate delivery converges on the same voucher.
	env.publishEvent(t, "fvt-r1", "org-fvt", "key-1", "model-a", hour1.Add(10*time.Minute), 100, 40)

	// AC2: an invalid event is skipped without wedging the consumer.
	env.publishEvent(t, "", "org-fvt", "key-1", "model-a", hour1.Add(10*time.Minute), 1, 1)

	// Wait for the consumer goroutine to apply the events.
	env.waitForVouchers(t, 4)
	env.waitForIdle(t)

	// Vouchers are queryable (FR4.2): 4 unique events.
	code, body := env.call(t, http.MethodGet, "/api/v1/admin/metering/vouchers", nil, "org-fvt")
	require.Equal(t, http.StatusOK, code, "body: %v", body)
	vouchers, _ := body["vouchers"].([]any)
	assert.Len(t, vouchers, 4, "duplicate and invalid events are not double-counted")
	pageMeta, _ := body["pageMeta"].(map[string]any)
	assert.EqualValues(t, "4", fmt.Sprint(pageMeta["total"]), "int64 fields serialize as strings")

	// Filter by api key.
	code, body = env.call(t, http.MethodGet, "/api/v1/admin/metering/vouchers?api_key_id=key-2", nil, "org-fvt")
	require.Equal(t, http.StatusOK, code, "body: %v", body)
	vouchers, _ = body["vouchers"].([]any)
	assert.Len(t, vouchers, 1)

	// GetVoucher by id (FR4.3).
	first, _ := vouchers[0].(map[string]any)
	voucherID, _ := first["voucherId"].(string)
	require.NotEmpty(t, voucherID)
	code, body = env.call(t, http.MethodGet, "/api/v1/admin/metering/vouchers/"+voucherID, nil, "org-fvt")
	require.Equal(t, http.StatusOK, code, "body: %v", body)
	voucher, _ := body["voucher"].(map[string]any)
	assert.Equal(t, "fvt-r4", voucher["requestId"])
	assert.Equal(t, "svc-fvt", voucher["serviceId"])
	assert.Equal(t, false, voucher["settled"])

	// AC9: an unknown voucher id is 10403.
	code, body = env.call(t, http.MethodGet, "/api/v1/admin/metering/vouchers/voucher-missing", nil, "org-fvt")
	assert.NotEqual(t, http.StatusOK, code)
	assert.EqualValues(t, float64(10403), body["code"])

	// Pending usage shows up in the summary before settlement (AC8).
	code, body = env.call(t, http.MethodGet, "/api/v1/admin/metering/usage-summary", nil, "org-fvt")
	require.Equal(t, http.StatusOK, code, "body: %v", body)
	rows, _ := body["rows"].([]any)
	require.Len(t, rows, 2)
	rowByKey := map[string]map[string]any{}
	for _, r := range rows {
		row, _ := r.(map[string]any)
		rowByKey[fmt.Sprint(row["groupKey"])] = row
	}
	k1 := rowByKey["key-1"]
	require.NotNil(t, k1)
	assert.EqualValues(t, "350", fmt.Sprint(k1["promptTokens"]), "int64 fields serialize as strings")
	assert.EqualValues(t, "0", fmt.Sprint(k1["settledHours"]))
	assert.EqualValues(t, "2", fmt.Sprint(k1["pendingHours"]))

	// AC4/AC5/AC7: one settlement pass settles the closed hours and
	// publishes exactly one event per usage record.
	require.NoError(t, env.settler.SettleOnce(ctx))
	settlements := env.mqBus.settle
	assert.Len(t, settlements, 3, "key-1 hour1, key-1 hour2, key-2 hour1")
	for _, s := range settlements {
		assert.Equal(t, mq.DefaultSubjects().Settlements, s.Subject)
		assert.NotEmpty(t, s.Headers["usage_record_id"])
	}

	// A second pass publishes nothing new (AC5/AC7).
	require.NoError(t, env.settler.SettleOnce(ctx))
	assert.Len(t, env.mqBus.settle, 3)

	// AC8: the summary now splits settled vs pending hours.
	code, body = env.call(t, http.MethodGet, "/api/v1/admin/metering/usage-summary", nil, "org-fvt")
	require.Equal(t, http.StatusOK, code, "body: %v", body)
	rows, _ = body["rows"].([]any)
	rowByKey = map[string]map[string]any{}
	for _, r := range rows {
		row, _ := r.(map[string]any)
		rowByKey[fmt.Sprint(row["groupKey"])] = row
	}
	k1 = rowByKey["key-1"]
	require.NotNil(t, k1)
	assert.EqualValues(t, "350", fmt.Sprint(k1["promptTokens"]))
	assert.EqualValues(t, "3", fmt.Sprint(k1["requestCount"]))
	assert.EqualValues(t, "2", fmt.Sprint(k1["settledHours"]))
	assert.EqualValues(t, "0", fmt.Sprint(k1["pendingHours"]))

	// Group by model.
	code, body = env.call(t, http.MethodGet, "/api/v1/admin/metering/usage-summary?group_by=model", nil, "org-fvt")
	require.Equal(t, http.StatusOK, code, "body: %v", body)
	rows, _ = body["rows"].([]any)
	rowByModel := map[string]map[string]any{}
	for _, r := range rows {
		row, _ := r.(map[string]any)
		rowByModel[fmt.Sprint(row["groupKey"])] = row
	}
	ma := rowByModel["model-a"]
	require.NotNil(t, ma)
	assert.EqualValues(t, "370", fmt.Sprint(ma["promptTokens"]), "model-a: r1 + r2 + r4")

	// FR4.4: settled usage records are queryable.
	code, body = env.call(t, http.MethodGet, "/api/v1/admin/metering/usage-records", nil, "org-fvt")
	require.Equal(t, http.StatusOK, code, "body: %v", body)
	records, _ := body["records"].([]any)
	assert.Len(t, records, 3)
	pageMeta, _ = body["pageMeta"].(map[string]any)
	assert.EqualValues(t, "3", fmt.Sprint(pageMeta["total"]))

	// Filter by key.
	code, body = env.call(t, http.MethodGet, "/api/v1/admin/metering/usage-records?api_key_id=key-2", nil, "org-fvt")
	require.Equal(t, http.StatusOK, code, "body: %v", body)
	records, _ = body["records"].([]any)
	assert.Len(t, records, 1)
	rec, _ := records[0].(map[string]any)
	assert.Equal(t, "key-2", rec["apiKeyId"])
	assert.EqualValues(t, "70", fmt.Sprint(rec["promptTokens"]))

	// Settled vouchers are flagged.
	code, body = env.call(t, http.MethodGet, "/api/v1/admin/metering/vouchers/"+voucherID, nil, "org-fvt")
	require.Equal(t, http.StatusOK, code, "body: %v", body)
	voucher, _ = body["voucher"].(map[string]any)
	assert.Equal(t, true, voucher["settled"])

	// AC10: an inverted range is 10404.
	code, body = env.call(t, http.MethodGet,
		fmt.Sprintf("/api/v1/admin/metering/usage-summary?since=%d&until=%d", now.Unix(), now.Unix()-10), nil, "org-fvt")
	assert.NotEqual(t, http.StatusOK, code)
	assert.EqualValues(t, float64(10404), body["code"])

	// AC10: a range over 92 days is 10404.
	code, body = env.call(t, http.MethodGet,
		fmt.Sprintf("/api/v1/admin/metering/usage-summary?since=%d&until=%d", now.Unix()-93*24*3600, now.Unix()), nil, "org-fvt")
	assert.NotEqual(t, http.StatusOK, code)
	assert.EqualValues(t, float64(10404), body["code"])
}

// TestFVTMeteringOrgIsolation verifies metering queries are scoped to
// the caller organization (AC14).
func TestFVTMeteringOrgIsolation(t *testing.T) {
	env := newMeteringEnv(t)
	env.waitForSubscription(t)

	hour := time.Now().UTC().Add(-3 * time.Hour).Truncate(time.Hour)
	env.publishEvent(t, "iso-1", "org-a", "key-1", "model-a", hour.Add(10*time.Minute), 100, 40)
	env.waitForVouchers(t, 1)
	env.waitForIdle(t)

	// org-b sees nothing.
	code, body := env.call(t, http.MethodGet, "/api/v1/admin/metering/vouchers", nil, "org-b")
	require.Equal(t, http.StatusOK, code, "body: %v", body)
	vouchers, _ := body["vouchers"].([]any)
	assert.Empty(t, vouchers)

	code, body = env.call(t, http.MethodGet, "/api/v1/admin/metering/usage-summary", nil, "org-b")
	require.Equal(t, http.StatusOK, code, "body: %v", body)
	rows, _ := body["rows"].([]any)
	assert.Empty(t, rows)

	// org-a sees its voucher.
	code, body = env.call(t, http.MethodGet, "/api/v1/admin/metering/vouchers", nil, "org-a")
	require.Equal(t, http.StatusOK, code, "body: %v", body)
	vouchers, _ = body["vouchers"].([]any)
	assert.Len(t, vouchers, 1)

	// A missing organization header is unauthorized (10001).
	code, body = env.call(t, http.MethodGet, "/api/v1/admin/metering/vouchers", nil, "")
	assert.NotEqual(t, http.StatusOK, code)
	assert.EqualValues(t, float64(10001), body["code"])
}
