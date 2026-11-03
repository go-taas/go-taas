// The user-realm model catalog as a rate card: every model the caller's
// organization can use, with the price per million input / output / cache-hit
// tokens the billing module would charge.
//
// The suite wires the real model and billing services over one gateway, so the
// price crosses the module seam exactly as it does in production (billing
// implements the model module's PriceLookup).

package fvt

import (
	"context"
	"encoding/json"
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
	"github.com/go-taas/go-taas/pkg/server"
	billingv1 "github.com/go-taas/go-taas/proto/taas/billing/v1"
	modelv1 "github.com/go-taas/go-taas/proto/taas/model/v1"
	"github.com/go-taas/go-taas/services/billing"
	"github.com/go-taas/go-taas/services/model"
	"github.com/go-taas/go-taas/services/tenancy"
)

// pricingCatalogEnv is the in-process stack for the rate-card projection.
type pricingCatalogEnv struct {
	db       *gorm.DB
	gwSrv    *httptest.Server
	modelSvc *model.Service
	priceSvc *billing.Service
}

func newPricingCatalogEnv(t *testing.T) *pricingCatalogEnv {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "pricing-catalog.db")
	db, err := gorm.Open(sqlite.Open(dbPath), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, model.MigrateSchemaForFVT(db))
	require.NoError(t, billing.MigrateSchemaForFVT(db))
	require.NoError(t, tenancy.MigrateSchemaForFVT(db))
	require.NoError(t, db.Create(&tenancy.Organization{
		ID: "org-fvt", DisplayName: "org-fvt", State: tenancy.StateActive,
	}).Error)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	grpcSrv := grpc.NewServer(grpc.ChainUnaryInterceptor(grpcmiddleware.UnaryServerInterceptor()))
	t.Cleanup(grpcSrv.Stop)

	modelSvc := model.NewForFVT(db)
	modelSvc.SetOrgGuard(tenancy.NewOrgGuard(db))
	priceSvc := billing.NewForFVT(db, nil)
	priceSvc.SetOrgGuard(tenancy.NewOrgGuard(db))
	// The seam under test: the model module asks billing for the rates.
	modelSvc.SetPriceLookup(priceSvc)

	modelv1.RegisterModelServiceServer(grpcSrv, modelSvc)
	billingv1.RegisterBillingServiceServer(grpcSrv, priceSvc)
	go func() { _ = grpcSrv.Serve(ln) }()

	conn, err := grpc.NewClient(ln.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })

	mux := runtime.NewServeMux(runtime.WithIncomingHeaderMatcher(server.FVTHeaderMatcher))
	require.NoError(t, modelv1.RegisterModelServiceHandler(context.Background(), mux, conn))
	require.NoError(t, billingv1.RegisterBillingServiceHandler(context.Background(), mux, conn))
	gwSrv := httptest.NewServer(mux)
	t.Cleanup(gwSrv.Close)

	return &pricingCatalogEnv{db: db, gwSrv: gwSrv, modelSvc: modelSvc, priceSvc: priceSvc}
}

// userModels lists the user-realm catalog and returns it by model name.
func (e *pricingCatalogEnv) userModels(t *testing.T) map[string]map[string]any {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, e.gwSrv.URL+"/api/v1/models?page.limit=50", nil)
	require.NoError(t, err)
	req.Header.Set("X-Organization-Id", "org-fvt")
	resp, err := e.gwSrv.Client().Do(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	var body struct {
		Models []map[string]any `json:"models"`
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
	out := map[string]map[string]any{}
	for _, m := range body.Models {
		if name, ok := m["name"].(string); ok {
			out[name] = m
		}
	}
	return out
}

// TestFVTUserCatalogCarriesPricing covers the rate card the user page shows:
// a priced model carries its rates on the user surface, an unpriced one
// carries none, and the same model's rates are the current versions only.
func TestFVTUserCatalogCarriesPricing(t *testing.T) {
	env := newPricingCatalogEnv(t)
	ctx := context.Background()
	repo := billing.NewRepository(env.db)
	now := time.Now().Unix()

	for _, name := range []string{"priced-model", "unpriced-model"} {
		_, err := env.modelSvc.RegisterModel(ctx, &modelv1.RegisterModelRequest{
			Name: name, Version: "v1", WeightPath: "models/" + name + "/v1",
		})
		require.NoError(t, err)
	}

	// Two versions of priced-model's price: only the current one shows.
	_, err := repo.UpsertPrice(ctx, &billing.PriceEntry{
		ModelID: "priced-model", AcceleratorType: "default",
		InputPricePerMillion: 1, OutputPricePerMillion: 2, CachedPricePerMillion: 0.1,
		Currency: "CNY", EffectiveFrom: now - 7200, TiersJSON: []byte("[]"),
	})
	require.NoError(t, err)
	_, err = repo.UpsertPrice(ctx, &billing.PriceEntry{
		ModelID: "priced-model", AcceleratorType: "default",
		InputPricePerMillion: 2, OutputPricePerMillion: 8, CachedPricePerMillion: 0.4,
		Currency: "CNY", EffectiveFrom: now - 60, TiersJSON: []byte("[]"),
	})
	require.NoError(t, err)

	models := env.userModels(t)
	require.Contains(t, models, "priced-model")

	pricing, ok := models["priced-model"]["pricing"].(map[string]any)
	require.True(t, ok, "a priced model carries its rate card on the user surface: %v", models["priced-model"])
	assert.EqualValues(t, 2, pricing["inputPricePerMillion"])
	assert.EqualValues(t, 8, pricing["outputPricePerMillion"])
	assert.EqualValues(t, 0.4, pricing["cachedPricePerMillion"])
	assert.Equal(t, "CNY", pricing["currency"])
	assert.Equal(t, "default", pricing["acceleratorType"])

	// The projection stays masked: no price id and no version.
	assert.NotContains(t, pricing, "priceId")
	assert.NotContains(t, pricing, "effectiveFrom")

	assert.Nil(t, models["unpriced-model"]["pricing"], "an unpriced model carries no rate card")
}
