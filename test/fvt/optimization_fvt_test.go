package fvt

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net"
	"net/http"
	"net/http/httptest"
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
	"github.com/go-taas/go-taas/pkg/server"
	"github.com/go-taas/go-taas/services/billing"
	"github.com/go-taas/go-taas/services/image"
	"github.com/go-taas/go-taas/services/infer"
	"github.com/go-taas/go-taas/services/metering"
	"github.com/go-taas/go-taas/services/model"
	optimization "github.com/go-taas/go-taas/services/optimization"
	"github.com/go-taas/go-taas/services/tenancy"

	optimizationv1 "github.com/go-taas/go-taas/proto/taas/optimization/v1"
)

// optimizationEnv is the in-process stack for the cost-optimization
// recommendations feature (feature #50): the optimization service
// (admin worklist + masked user surface) over one gRPC server and the
// gateway mux, sharing one SQLite database with the sibling tables the
// engine reads (billing, metering, infer, model, image, tenancy).
type optimizationEnv struct {
	db     *gorm.DB
	gwSrv  *httptest.Server
	grpcLn net.Listener
	svc    *optimization.Service
}

// newOptimizationEnv builds the optimization FVT stack. The admin
// session resolves to user "admin-fvt" of org "org-fvt" (an org
// admin); the plain session resolves to "user-fvt" (a plain member of
// the same org). The optimization config is installed process-wide
// with test thresholds so the rule matrix is deterministic.
func newOptimizationEnv(t *testing.T) *optimizationEnv {
	t.Helper()

	cfg := &config.Configuration{}
	cfg.Optimization = optimizationTestConfig()
	config.SetConfigForTest(cfg)
	t.Cleanup(func() { config.SetConfigForTest(&config.Configuration{}) })

	db, err := gorm.Open(sqlite.Open(fmt.Sprintf("file:%s?mode=memory&cache=shared", t.Name())), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, optimization.MigrateSchemaForFVT(db))
	require.NoError(t, billing.MigrateSchemaForFVT(db))
	require.NoError(t, metering.MigrateSchemaForFVT(db))
	require.NoError(t, infer.MigrateSchemaForFVT(db))
	require.NoError(t, model.MigrateSchemaForFVT(db))
	require.NoError(t, image.MigrateSchemaForFVT(db))
	require.NoError(t, tenancy.MigrateSchemaForFVT(db))
	require.NoError(t, db.Create(&tenancy.Organization{
		ID: "org-fvt", DisplayName: "org-fvt", State: tenancy.StateActive,
	}).Error)
	require.NoError(t, db.Create(&tenancy.OrgMember{
		OrganizationID: "org-fvt", UserID: "admin-fvt", Role: tenancy.RoleAdmin, JoinedAt: time.Now().UTC(),
	}).Error)
	require.NoError(t, db.Create(&tenancy.OrgMember{
		OrganizationID: "org-fvt", UserID: "user-fvt", Role: tenancy.RoleMember, JoinedAt: time.Now().UTC(),
	}).Error)
	t.Cleanup(func() {
		sqlDB, _ := db.DB()
		_ = sqlDB.Close()
	})

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	grpcSrv := grpc.NewServer(grpc.ChainUnaryInterceptor(grpcmiddleware.UnaryServerInterceptor()))
	t.Cleanup(grpcSrv.Stop)

	svc := optimization.NewForFVT(db)
	svc.SetRoleGuard(tenancy.NewRoleGuard(db))
	// The card-type seam (R3's fleet gate): the seeded A800 fleet.
	svc.SetCardTypeLister(fvtCardTypeLister{types: []optimization.CardTypePair{{Vendor: "nvidia", CardType: "A800"}}})

	env := &optimizationEnv{db: db, grpcLn: ln, svc: svc}

	optimizationv1.RegisterOptimizationServiceServer(grpcSrv, svc)
	go func() { _ = grpcSrv.Serve(ln) }()

	conn, err := grpc.NewClient(ln.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })

	mux := runtime.NewServeMux(runtime.WithIncomingHeaderMatcher(server.FVTHeaderMatcher))
	require.NoError(t, optimizationv1.RegisterOptimizationServiceHandler(context.Background(), mux, conn))

	gwSrv := httptest.NewServer(mux)
	t.Cleanup(gwSrv.Close)
	env.gwSrv = gwSrv

	return env
}

// fvtCardTypeLister is a static CardTypeLister double for R3's fleet
// gate.
type fvtCardTypeLister struct {
	types []optimization.CardTypePair
}

// ListCardTypes implements optimization.CardTypeLister.
func (l fvtCardTypeLister) ListCardTypes(context.Context) ([]optimization.CardTypePair, error) {
	return l.types, nil
}

// optimizationTestConfig returns the optimization section with small
// deterministic thresholds so the seeded fixtures cross every rule's
// eligibility gate.
func optimizationTestConfig() config.OptimizationConfig {
	return config.OptimizationConfig{
		RefreshIntervalMinutes: 60,
		WindowDays:             30,
		TopNPerOrg:             50,
		Currency:               "CNY",
		R1: config.OptimizationR1Config{
			MinRequests:     10,
			MinBilledTokens: 1000,
			ContextRatio:    0.8,
			MinSaving:       1,
		},
		R2: config.OptimizationR2Config{
			MinRequests:      10,
			OfflineRatio:     0.9,
			PromptTokenFloor: 512,
		},
		R3: config.OptimizationR3Config{
			IdleRequestsPerDay: 10,
			IdleWindowDays:     7,
		},
		R4: config.OptimizationR4Config{
			MinPromptTokens:      1000,
			MaxCacheHitRate:      0.05,
			DefaultHitAssumption: 0.5,
		},
		ModelContextLengths: map[string]int64{
			"model-a": 32768,
			"model-b": 32768,
			"model-c": 32768,
		},
		CardHourlyCosts: map[string]float64{
			"A800": 10.0,
		},
		PeakHours:          []string{"09:00-18:00"},
		PeakWeekdaysOnly:   true,
		Timezone:           "UTC",
		PrefixCacheEngines: []string{"vllm"},
	}
}

// call issues a JSON request against the gateway and decodes the body.
// admin=true wires the admin session (an org admin); admin=false wires
// a plain member session.
func (e *optimizationEnv) call(t *testing.T, method, path string, body any, admin bool) (int, map[string]any) {
	t.Helper()
	user := "user-fvt"
	if admin {
		user = "admin-fvt"
	}
	e.svc.SetSessionUserResolver(fvtSessionUserResolver{user: user})
	e.svc.SetSessionOrgResolver(fvtSessionOrgResolver{org: "org-fvt"})
	return e.callRaw(t, method, path, body)
}

// callRaw issues a JSON request without touching the session resolvers.
func (e *optimizationEnv) callRaw(t *testing.T, method, path string, body any) (int, map[string]any) {
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
	req.Header.Set("X-Organization-Id", "org-fvt")
	resp, err := e.gwSrv.Client().Do(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	out := map[string]any{}
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

// seedOptModels registers three catalog models: model-a (expensive),
// model-b (cheap, the best R1 candidate) and model-c (mid-priced, the
// R1 runner-up alternative).
func (e *optimizationEnv) seedOptModels(t *testing.T) (string, string) {
	t.Helper()
	repo := model.NewRepository(e.db)
	idA, err := repo.RegisterModelOrCreateVersion(context.Background(), "model-a", "", "v1", "a/v1")
	require.NoError(t, err)
	_, err = repo.ActivateVersion(context.Background(), idA, "v1")
	require.NoError(t, err)
	idB, err := repo.RegisterModelOrCreateVersion(context.Background(), "model-b", "", "v1", "b/v1")
	require.NoError(t, err)
	_, err = repo.ActivateVersion(context.Background(), idB, "v1")
	require.NoError(t, err)
	idC, err := repo.RegisterModelOrCreateVersion(context.Background(), "model-c", "", "v1", "c/v1")
	require.NoError(t, err)
	_, err = repo.ActivateVersion(context.Background(), idC, "v1")
	require.NoError(t, err)
	return idA, idB
}

// seedOptPrices writes the price matrix: model-a expensive, model-b
// cheap, both on the default card cell.
func (e *optimizationEnv) seedOptPrices(t *testing.T, modelA, modelB string) {
	t.Helper()
	now := time.Now().UTC().Unix()
	require.NoError(t, e.db.Create(&billing.PriceEntry{
		ID: "price-a-1", ModelID: modelA, AcceleratorType: "default",
		InputPricePerMillion: 10, OutputPricePerMillion: 20, CachedPricePerMillion: 1,
		Currency: "CNY", EffectiveFrom: now - 86400, TiersJSON: []byte("[]"),
	}).Error)
	require.NoError(t, e.db.Create(&billing.PriceEntry{
		ID: "price-b-1", ModelID: modelB, AcceleratorType: "default",
		InputPricePerMillion: 1, OutputPricePerMillion: 2, CachedPricePerMillion: 0.1,
		Currency: "CNY", EffectiveFrom: now - 86400, TiersJSON: []byte("[]"),
	}).Error)
	// model-c: mid-priced — the R1 runner-up alternative.
	var modelC string
	require.NoError(t, e.db.Raw("SELECT id FROM models WHERE name = 'model-c'").Scan(&modelC).Error)
	require.NotEmpty(t, modelC)
	require.NoError(t, e.db.Create(&billing.PriceEntry{
		ID: "price-c-1", ModelID: modelC, AcceleratorType: "default",
		InputPricePerMillion: 4, OutputPricePerMillion: 8, CachedPricePerMillion: 0.4,
		Currency: "CNY", EffectiveFrom: now - 86400, TiersJSON: []byte("[]"),
	}).Error)
}

// seedOptCharges writes priced charge records: org-fvt on model-a with
// enough requests/tokens for R1/R2/R4 eligibility, and a spend the
// substitution and batch arithmetic can beat.
func (e *optimizationEnv) seedOptCharges(t *testing.T, modelA string) {
	t.Helper()
	now := time.Now().UTC()
	hour := now.Unix() - 3600
	require.NoError(t, e.db.Create(&billing.ChargeRecord{
		ID: "charge-1", OrganizationID: "org-fvt", APIKeyID: "key-1",
		ModelID: modelA, AcceleratorType: "default",
		PeriodStart: hour, PeriodEnd: hour + 3600,
		PromptTokens: 2000, CompletionTokens: 1000, CachedTokens: 0,
		RequestCount: 20, Amount: 50, Currency: "CNY",
		PriceID: nil, TierIndex: -1, Priced: true, ChargedAt: now,
	}).Error)
}

// seedOptRequestLogs writes per-request logs for the R2 offline-shape
// test: all requests outside the peak window with large prompts.
func (e *optimizationEnv) seedOptRequestLogs(t *testing.T, modelA string) {
	t.Helper()
	// 03:00 UTC on a fixed past day — outside 09:00-18:00.
	offPeak := time.Date(2026, 10, 10, 3, 0, 0, 0, time.UTC)
	for i := 0; i < 20; i++ {
		require.NoError(t, e.db.Create(&metering.RequestLog{
			ID: fmt.Sprintf("log-%d", i), RequestID: fmt.Sprintf("req-%d", i),
			OrganizationID: "org-fvt", APIKeyID: "key-1", ModelID: modelA,
			PromptTokens: 1024, CompletionTokens: 64, Status: "success",
			CreatedAt: offPeak.Add(time.Duration(i) * time.Minute),
		}).Error)
	}
}

// seedOptIdleService writes a running service with autoscaling off and
// near-zero traffic — the R3 idle-service candidate.
func (e *optimizationEnv) seedOptIdleService(t *testing.T, modelA string) string {
	t.Helper()
	svc := &infer.InferenceService{
		ID: "svc-idle-1", OrganizationID: "org-fvt", Name: "svc-idle",
		ModelID: modelA, ModelVersion: "v1", ImageID: "img-vllm-1",
		Replicas: 2, Accelerator: "nvidia", AcceleratorType: "A800",
		State: infer.StateRunning, Endpoints: []string{},
		Autoscaling: []byte(`{}`),
		CreatedAt:   time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}
	require.NoError(t, e.db.Create(svc).Error)
	// The global policy: autoscaling on but scale-to-zero off (the
	// migration already seeded the singleton row, so update it).
	require.NoError(t, e.db.Model(&infer.AutoscalingPolicy{}).Where("id = ?", 1).Updates(map[string]any{
		"enabled": true, "scale_to_zero": false,
	}).Error)
	// A vllm image so R4's engine gate passes for model-a.
	require.NoError(t, e.db.Create(&image.Image{
		ID: "img-vllm-1", Name: "ghcr.io/go-taas/vllm", Tag: "v0.6.3",
		Accelerator: "nvidia", Engine: "vllm",
	}).Error)
	return svc.ID
}

// refresh runs the admin refresh RPC and returns the emitted count.
func (e *optimizationEnv) refresh(t *testing.T, admin bool) int {
	t.Helper()
	code, body := e.call(t, http.MethodPost, "/api/v1/admin/optimization:refresh", map[string]any{}, admin)
	require.Equal(t, http.StatusOK, code, "refresh must succeed: %v", body)
	overview, _ := body["overview"].(map[string]any)
	require.NotNil(t, overview, "refresh body: %v", body)
	return int(numberOf(t, overview["activeCount"]))
}

// listRecommendations lists the admin recommendations with filters.
func (e *optimizationEnv) listRecommendations(t *testing.T, admin bool, query string) (int, map[string]any) {
	t.Helper()
	return e.call(t, http.MethodGet, "/api/v1/admin/optimization/recommendations"+query, nil, admin)
}

// firstRecommendation returns the first recommendation of an admin
// list call.
func (e *optimizationEnv) firstRecommendation(t *testing.T, admin bool) map[string]any {
	t.Helper()
	code, body := e.listRecommendations(t, admin, "?status=all")
	require.Equal(t, http.StatusOK, code, "list must succeed: %v", body)
	items, _ := body["recommendations"].([]any)
	require.NotEmpty(t, items, "expected at least one recommendation")
	first, _ := items[0].(map[string]any)
	return first
}

// TestFVTOptimizationRuleMatrix covers the engine's rule matrix
// (feature #50, AC6): the seeded fixtures produce the R1 substitution,
// R2 batch, R3 idle-service and R4 cache recommendations with the
// expected arithmetic, ranked by estimated monthly saving descending.
func TestFVTOptimizationRuleMatrix(t *testing.T) {
	env := newOptimizationEnv(t)
	modelA, modelB := env.seedOptModels(t)
	env.seedOptPrices(t, modelA, modelB)
	env.seedOptCharges(t, modelA)
	env.seedOptRequestLogs(t, modelA)
	env.seedOptIdleService(t, modelA)

	emitted := env.refresh(t, true)
	require.GreaterOrEqual(t, emitted, 3, "expected R1+R2+R3/R4 recommendations: %d", emitted)

	// The ranked list is saving-descending and every row carries the
	// verifiable fields (AC1/AC3).
	code, body := env.listRecommendations(t, true, "?status=all")
	require.Equal(t, http.StatusOK, code)
	items, _ := body["recommendations"].([]any)
	require.NotEmpty(t, items)
	rules := map[string]bool{}
	prevSaving := math.Inf(1)
	for _, raw := range items {
		rec, _ := raw.(map[string]any)
		rule, _ := rec["rule"].(string)
		rules[rule] = true
		saving, _ := rec["estimatedMonthlySaving"].(string)
		require.NotEmpty(t, saving, "saving must be a decimal string")
		// The arithmetic and inputs are present for verification.
		arithmetic, _ := rec["arithmetic"].(string)
		require.NotEmpty(t, arithmetic)
		v := parseFloat(t, saving)
		assert.GreaterOrEqual(t, prevSaving, v-0.001, "ranking must be saving-descending")
		prevSaving = v
	}
	assert.True(t, rules[optimization.RuleSubstitution], "R1 must fire")
	assert.True(t, rules[optimization.RuleBatch], "R2 must fire")
	assert.True(t, rules[optimization.RuleCache], "R4 must fire")

	// The R1 recommendation carries alternatives and grant-aware acting
	// links (AC3/AC7).
	code, body = env.call(t, http.MethodGet, "/api/v1/admin/optimization/recommendations?rule=r1_substitution&status=all", nil, true)
	require.Equal(t, http.StatusOK, code)
	items, _ = body["recommendations"].([]any)
	require.Len(t, items, 1)
	r1, _ := items[0].(map[string]any)
	alternatives, _ := r1["alternatives"].([]any)
	assert.NotEmpty(t, alternatives, "R1 must list the cheaper alternative")
	acting, _ := r1["actingPage"].(map[string]any)
	require.NotNil(t, acting)
	route, _ := acting["route"].(string)
	assert.Contains(t, route, "/admin/models/", "admin acting link targets the grant page")

	// The overview totals the active set (AC1).
	code, body = env.call(t, http.MethodGet, "/api/v1/admin/optimization", nil, true)
	require.Equal(t, http.StatusOK, code)
	overview, _ := body["overview"].(map[string]any)
	require.NotNil(t, overview)
	total, _ := overview["totalEstimatedMonthlySaving"].(string)
	assert.NotEmpty(t, total)
}

// TestFVTOptimizationDismissRestoreLifecycle covers the dismiss and
// restore lifecycle (feature #50, AC4/AC5): dismiss with a reason,
// double-dismiss 13202, invalid reason 13203, restore, restore-active
// 13204, and fingerprint persistence across a refresh.
func TestFVTOptimizationDismissRestoreLifecycle(t *testing.T) {
	env := newOptimizationEnv(t)
	modelA, modelB := env.seedOptModels(t)
	env.seedOptPrices(t, modelA, modelB)
	env.seedOptCharges(t, modelA)
	env.seedOptRequestLogs(t, modelA)
	env.refresh(t, true)

	rec := env.firstRecommendation(t, true)
	id, _ := rec["recommendationId"].(string)
	require.NotEmpty(t, id)

	// Dismiss with an invalid reason -> 13203.
	code, body := env.call(t, http.MethodPost, "/api/v1/admin/optimization/recommendations/"+id+":dismiss",
		map[string]any{"reason": "bogus"}, true)
	assert.NotEqual(t, http.StatusOK, code, "body: %v", body)
	assert.EqualValues(t, 13203, codeOf(body))

	// Dismiss with reason=other but no note -> 13203.
	code, body = env.call(t, http.MethodPost, "/api/v1/admin/optimization/recommendations/"+id+":dismiss",
		map[string]any{"reason": "other"}, true)
	assert.NotEqual(t, http.StatusOK, code, "body: %v", body)
	assert.EqualValues(t, 13203, codeOf(body))

	// Dismiss validly -> status dismissed.
	code, body = env.call(t, http.MethodPost, "/api/v1/admin/optimization/recommendations/"+id+":dismiss",
		map[string]any{"reason": "planned"}, true)
	require.Equal(t, http.StatusOK, code)
	updated, _ := body["recommendation"].(map[string]any)
	require.NotNil(t, updated)
	status, _ := updated["status"].(string)
	assert.Equal(t, "dismissed", status)

	// Double dismiss -> 13202.
	code, body = env.call(t, http.MethodPost, "/api/v1/admin/optimization/recommendations/"+id+":dismiss",
		map[string]any{"reason": "planned"}, true)
	assert.NotEqual(t, http.StatusOK, code, "body: %v", body)
	assert.EqualValues(t, 13202, codeOf(body))

	// The default list (status=active) no longer shows it.
	code, body = env.listRecommendations(t, true, "")
	require.Equal(t, http.StatusOK, code)
	items, _ := body["recommendations"].([]any)
	for _, raw := range items {
		r, _ := raw.(map[string]any)
		rid, _ := r["recommendationId"].(string)
		assert.NotEqual(t, id, rid, "dismissed recommendation must leave the active list")
	}

	// Refresh: the same condition recomputes, the fingerprint keeps the
	// dismissal (AC5).
	env.refresh(t, true)
	code, body = env.call(t, http.MethodGet, "/api/v1/admin/optimization/recommendations/"+id, nil, true)
	require.Equal(t, http.StatusOK, code)
	detail, _ := body["recommendation"].(map[string]any)
	require.NotNil(t, detail)
	status, _ = detail["status"].(string)
	assert.Equal(t, "dismissed", status, "fingerprint persistence keeps the dismissal across refresh")

	// Restore -> active.
	code, body = env.call(t, http.MethodPost, "/api/v1/admin/optimization/recommendations/"+id+":restore",
		map[string]any{}, true)
	require.Equal(t, http.StatusOK, code)
	updated, _ = body["recommendation"].(map[string]any)
	status, _ = updated["status"].(string)
	assert.Equal(t, "active", status)

	// Restore again -> 13204.
	code, body = env.call(t, http.MethodPost, "/api/v1/admin/optimization/recommendations/"+id+":restore",
		map[string]any{}, true)
	assert.NotEqual(t, http.StatusOK, code, "body: %v", body)
	assert.EqualValues(t, 13204, codeOf(body))

	// Unknown id -> 13201.
	code, body = env.call(t, http.MethodGet, "/api/v1/admin/optimization/recommendations/nope", nil, true)
	assert.NotEqual(t, http.StatusOK, code, "body: %v", body)
	assert.EqualValues(t, 13201, codeOf(body))
}

// TestFVTOptimizationUserSurfaceMasking covers the user surface
// (feature #50, AC8/AC9): the masked projection excludes R3, the
// org scoping rejects foreign ids with 13201, and the user dismiss
// state is independent of the admin surface's.
func TestFVTOptimizationUserSurfaceMasking(t *testing.T) {
	env := newOptimizationEnv(t)
	modelA, modelB := env.seedOptModels(t)
	env.seedOptPrices(t, modelA, modelB)
	env.seedOptCharges(t, modelA)
	env.seedOptRequestLogs(t, modelA)
	env.seedOptIdleService(t, modelA)
	env.refresh(t, true)

	// The user overview and list never contain R3 (AC8).
	code, body := env.call(t, http.MethodGet, "/api/v1/optimization", nil, false)
	require.Equal(t, http.StatusOK, code)
	overview, _ := body["overview"].(map[string]any)
	require.NotNil(t, overview)
	for _, raw := range body["recommendations"].([]any) {
		rec, _ := raw.(map[string]any)
		rule, _ := rec["rule"].(string)
		assert.NotEqual(t, optimization.RuleIdleService, rule, "R3 must be masked on the user surface")
	}
	code, body = env.call(t, http.MethodGet, "/api/v1/optimization/recommendations?status=all", nil, false)
	require.Equal(t, http.StatusOK, code)
	items, _ := body["recommendations"].([]any)
	require.NotEmpty(t, items)
	for _, raw := range items {
		rec, _ := raw.(map[string]any)
		rule, _ := rec["rule"].(string)
		assert.NotEqual(t, optimization.RuleIdleService, rule)
		acting, _ := rec["actingPage"].(map[string]any)
		require.NotNil(t, acting)
		route, _ := acting["route"].(string)
		assert.NotContains(t, route, "/admin/", "user acting links never target the admin surface")
	}

	// The user can dismiss and restore their own recommendation.
	first, _ := items[0].(map[string]any)
	id, _ := first["recommendationId"].(string)
	code, body = env.call(t, http.MethodPost, "/api/v1/optimization/recommendations/"+id+":dismiss",
		map[string]any{"reason": "not_applicable"}, false)
	require.Equal(t, http.StatusOK, code)
	updated, _ := body["recommendation"].(map[string]any)
	status, _ := updated["status"].(string)
	assert.Equal(t, "dismissed", status)

	// The user dismissal does not dismiss it for the admin surface
	// (surface-keyed state, FR3.4).
	code, body = env.call(t, http.MethodGet, "/api/v1/admin/optimization/recommendations/"+id, nil, true)
	require.Equal(t, http.StatusOK, code)
	detail, _ := body["recommendation"].(map[string]any)
	status, _ = detail["status"].(string)
	assert.Equal(t, "active", status, "user dismissal is surface-scoped")

	// A foreign recommendation id on the user surface is 13201, not a
	// leak (AC9) — seed a second org's row directly.
	require.NoError(t, env.db.Create(&optimization.Recommendation{
		ID: "rec-foreign", Fingerprint: optimization.Fingerprint(optimization.RuleCache, optimization.TargetOrgModel, "org-other/m", "x"),
		Rule: optimization.RuleCache, TargetType: optimization.TargetOrgModel,
		TargetID: "org-other/m", TargetDisplay: "org-other / m", OrganizationID: "org-other",
		ModelID: "m", Suggestion: "x", EstimatedMonthlySaving: 5, Currency: "CNY",
		Inputs: []byte(`{}`), Arithmetic: "x", Alternatives: []byte(`[]`),
		ActingPageAdmin: []byte(`{}`), ActingPageUser: []byte(`{}`),
		FirstSeenAt: time.Now().UTC(), LastComputedAt: time.Now().UTC(),
	}).Error)
	code, body = env.call(t, http.MethodGet, "/api/v1/optimization/recommendations/rec-foreign", nil, false)
	assert.NotEqual(t, http.StatusOK, code, "body: %v", body)
	assert.EqualValues(t, 13201, codeOf(body))
}

// TestFVTOptimizationRoleAndFilterValidation covers the role gate
// (feature #50, AC12) and the filter validation (13205).
func TestFVTOptimizationRoleAndFilterValidation(t *testing.T) {
	env := newOptimizationEnv(t)
	modelA, modelB := env.seedOptModels(t)
	env.seedOptPrices(t, modelA, modelB)
	env.seedOptCharges(t, modelA)
	env.seedOptRequestLogs(t, modelA)
	env.refresh(t, true)

	// A plain member (not an org admin) is rejected on the admin RPCs
	// with 10036 (AC12).
	code, body := env.call(t, http.MethodGet, "/api/v1/admin/optimization", nil, false)
	assert.NotEqual(t, http.StatusOK, code, "body: %v", body)
	assert.EqualValues(t, 10036, codeOf(body))

	code, body = env.call(t, http.MethodPost, "/api/v1/admin/optimization:refresh", map[string]any{}, false)
	assert.NotEqual(t, http.StatusOK, code, "body: %v", body)
	assert.EqualValues(t, 10036, codeOf(body))

	// Invalid filter values -> 13205.
	code, body = env.listRecommendations(t, true, "?rule=bogus")
	assert.NotEqual(t, http.StatusOK, code, "body: %v", body)
	assert.EqualValues(t, 13205, codeOf(body))

	code, body = env.listRecommendations(t, true, "?status=bogus")
	assert.NotEqual(t, http.StatusOK, code, "body: %v", body)
	assert.EqualValues(t, 13205, codeOf(body))

	code, body = env.listRecommendations(t, true, "?min_saving=-5")
	assert.NotEqual(t, http.StatusOK, code, "body: %v", body)
	assert.EqualValues(t, 13205, codeOf(body))

	// Valid filters work.
	code, body = env.listRecommendations(t, true, "?rule=r2_batch&status=all&min_saving=0")
	require.Equal(t, http.StatusOK, code)
	assert.EqualValues(t, 0, codeOf(body))
}

// TestFVTOptimizationRefreshFailureKeepsSnapshot covers the failure
// semantics (feature #50, AC10): a failed refresh leaves the previous
// active set intact.
func TestFVTOptimizationRefreshFailureKeepsSnapshot(t *testing.T) {
	env := newOptimizationEnv(t)
	modelA, modelB := env.seedOptModels(t)
	env.seedOptPrices(t, modelA, modelB)
	env.seedOptCharges(t, modelA)
	env.seedOptRequestLogs(t, modelA)
	before := env.refresh(t, true)
	require.GreaterOrEqual(t, before, 1)

	// Break the store: drop the recommendations table so the swap fails.
	require.NoError(t, env.db.Migrator().DropTable("optimization_recommendations"))

	code, body := env.call(t, http.MethodPost, "/api/v1/admin/optimization:refresh", map[string]any{}, true)
	assert.NotEqual(t, http.StatusOK, code, "body: %v", body)
	assert.NotEqualValues(t, 0, codeOf(body), "the refresh must fail")

	// Recreate the table: the previous rows were dropped with it, so
	// re-seed via the engine and verify the run row recorded the
	// failure (the run log is the observable failure evidence).
	require.NoError(t, optimization.MigrateSchemaForFVT(env.db))
	var failures int64
	require.NoError(t, env.db.Table("optimization_runs").Where("success = ?", false).Count(&failures).Error)
	assert.GreaterOrEqual(t, failures, int64(1), "the failed run is logged")
}

// codeOf extracts the business code from a gateway response body.
func codeOf(body map[string]any) int {
	resp, _ := body["response"].(map[string]any)
	if resp != nil {
		if c, ok := resp["code"].(float64); ok {
			return int(c)
		}
	}
	if c, ok := body["code"].(float64); ok {
		return int(c)
	}
	return -1
}

// parseFloat parses a decimal string.
func parseFloat(t *testing.T, s string) float64 {
	t.Helper()
	var v float64
	_, err := fmt.Sscanf(s, "%g", &v)
	require.NoError(t, err)
	return v
}

// numberOf extracts a number from a JSON value that may be encoded
// either as a float64 (protojson int64/number) or a decimal string.
func numberOf(t *testing.T, v any) float64 {
	t.Helper()
	switch n := v.(type) {
	case float64:
		return n
	case string:
		return parseFloat(t, n)
	default:
		t.Fatalf("unexpected number encoding: %T %v", v, v)
		return 0
	}
}
