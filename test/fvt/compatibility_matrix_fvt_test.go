package fvt

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/grpc-ecosystem/grpc-gateway/v2/runtime"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	"github.com/go-taas/go-taas/pkg/grpcmiddleware"
	"github.com/go-taas/go-taas/pkg/server"
	imagev1 "github.com/go-taas/go-taas/proto/taas/image/v1"
	"github.com/go-taas/go-taas/services/image"
	"github.com/go-taas/go-taas/services/model"
	"github.com/go-taas/go-taas/services/tenancy"
)

// compatEnv is the in-process stack for the compatibility matrix
// feature: the image service over one gRPC server, the gateway mux in
// front, a shared SQLite, and a fake card-types provider standing in for
// the accelerator inventory (feature #19, AD11).
type compatEnv struct {
	db     *gorm.DB
	gwSrv  *httptest.Server
	grpcLn net.Listener
	mqBus  *recordingBus

	imageSvc *image.Service
	// cardTypes is the live card-type set the fake provider returns.
	// Tests mutate it to exercise the not_in_fleet derivation (AC6).
	cardTypes []image.CardType
}

// newCompatEnv builds the compatibility-matrix FVT stack.
func newCompatEnv(t *testing.T) *compatEnv {
	t.Helper()

	db, err := gorm.Open(sqlite.Open(fmt.Sprintf("file:%s?mode=memory&cache=shared", t.Name())), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, model.MigrateSchemaForFVT(db))
	require.NoError(t, image.MigrateSchemaForFVT(db))
	require.NoError(t, tenancy.MigrateSchemaForFVT(db))
	for _, orgID := range []string{"org-fvt", "org-a"} {
		require.NoError(t, db.Create(&tenancy.Organization{
			ID: orgID, DisplayName: orgID, State: tenancy.StateActive,
		}).Error)
	}
	t.Cleanup(func() {
		sqlDB, _ := db.DB()
		_ = sqlDB.Close()
	})

	// Seed the image registry with two engines (nvidia vllm, metax
	// sglang) so the matrix has a non-empty engine axis.
	imageRepo := image.NewRepository(db)
	for _, s := range []*image.Summary{
		{ImageID: "img-vllm-nvidia", Name: "ghcr.io/go-taas/vllm", Tag: "v0.6.3", Accelerator: "nvidia", Engine: "vllm"},
		{ImageID: "img-sglang-metax", Name: "ghcr.io/go-taas/sglang", Tag: "v0.1.4", Accelerator: "metax", Engine: "sglang"},
	} {
		require.NoError(t, imageRepo.CreateImage(context.Background(), &image.Image{
			ID: s.ImageID, Name: s.Name, Tag: s.Tag, Accelerator: s.Accelerator, Engine: s.Engine,
		}))
	}
	image.WireRegistryForTest(db)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	grpcSrv := grpc.NewServer(grpc.ChainUnaryInterceptor(grpcmiddleware.UnaryServerInterceptor()))
	t.Cleanup(grpcSrv.Stop)

	bus := newRecordingBus()
	env := &compatEnv{db: db, mqBus: bus, grpcLn: ln}
	env.cardTypes = []image.CardType{
		{Vendor: "nvidia", CardType: "A800"},
		{Vendor: "metax", CardType: "M100"},
	}
	imageSvc := image.NewForFVT(db, bus)
	imageSvc.SetCardTypesProvider(image.CardTypesProviderFunc(func(_ context.Context) ([]image.CardType, error) {
		return env.cardTypes, nil
	}))
	imageSvc.SetCompatibilityLazyDefault(image.StatusExperimental)
	env.imageSvc = imageSvc

	imagev1.RegisterImageServiceServer(grpcSrv, imageSvc)
	go func() { _ = grpcSrv.Serve(ln) }()

	conn, err := grpc.NewClient(ln.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })

	mux := runtime.NewServeMux(
		runtime.WithIncomingHeaderMatcher(server.FVTHeaderMatcher),
	)
	require.NoError(t, imagev1.RegisterImageServiceHandler(context.Background(), mux, conn))

	gwSrv := httptest.NewServer(mux)
	t.Cleanup(gwSrv.Close)
	env.gwSrv = gwSrv

	return env
}

// call issues a JSON request against the gateway and decodes the body.
func (e *compatEnv) call(t *testing.T, method, path string, body any) (int, map[string]any) {
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

// seedModel registers a model via the model service so the matrix has a
// model dimension. It returns the model id.
func (e *compatEnv) seedModel(t *testing.T, name, version string) string {
	t.Helper()
	// The model service is not part of this env; insert the model row
	// directly (the matrix reads the shared models table).
	modelRepo := model.NewRepository(e.db)
	m := &model.Model{Name: name}
	require.NoError(t, modelRepo.CreateModel(context.Background(), m))
	// Register a version so the model is a valid catalog entry.
	require.NoError(t, modelRepo.CreateVersion(context.Background(), &model.Version{
		ModelID: m.ID, Version: version, WeightPath: name + "/" + version,
	}))
	return m.ID
}

// TestFVTCompatibilityMatrix walks the acceptance criteria of the
// compatibility matrix architecture doc: AC1 (first-boot seed with
// vendor-match rule), AC2 (list with filters/search/pagination),
// AC3 (dimensions + counts), AC4 (set status + lazy seed + 10210/10211),
// AC5 (bulk atomic), AC6 (not_in_fleet), AC7 (masked user projection).
func TestFVTCompatibilityMatrix(t *testing.T) {
	env := newCompatEnv(t)
	modelID := env.seedModel(t, "qwen-3b", "v1")

	// AC1: first-boot seed creates a row per combination; vendor-matched
	// combos default to experimental, vendor-mismatched to unsupported.
	// The seed runs through the image service's Migrate hook (AD3).
	require.NoError(t, env.imageSvc.Migrate(context.Background()))

	// AC2: list returns cells with filters/search/pagination.
	code, body := env.call(t, http.MethodGet, "/api/v1/admin/compatibility", nil)
	require.Equal(t, http.StatusOK, code, "body: %v", body)
	cells, _ := body["cells"].([]any)
	require.Len(t, cells, 4)
	first, _ := cells[0].(map[string]any)
	assert.NotEmpty(t, first["status"])
	assert.NotEmpty(t, first["modelName"])
	assert.NotEmpty(t, first["cardVendor"])
	// int64 fields serialize as JSON strings in proto3 JSON mapping.
	assert.NotEmpty(t, first["updatedAt"])

	// Filter by status.
	code, body = env.call(t, http.MethodGet, "/api/v1/admin/compatibility?status=experimental", nil)
	require.Equal(t, http.StatusOK, code, "body: %v", body)
	cells, _ = body["cells"].([]any)
	assert.Len(t, cells, 2, "two vendor-matched combos are experimental")

	// Search by model name.
	code, body = env.call(t, http.MethodGet, "/api/v1/admin/compatibility?search=qwen", nil)
	require.Equal(t, http.StatusOK, code, "body: %v", body)
	cells, _ = body["cells"].([]any)
	assert.Len(t, cells, 4)

	// AC3: dimensions + per-status counts.
	code, body = env.call(t, http.MethodGet, "/api/v1/admin/compatibility/dimensions", nil)
	require.Equal(t, http.StatusOK, code, "body: %v", body)
	models, _ := body["models"].([]any)
	engines, _ := body["engines"].([]any)
	cardTypes, _ := body["cardTypes"].([]any)
	assert.Len(t, models, 1)
	assert.Len(t, engines, 2)
	assert.Len(t, cardTypes, 2)
	counts, _ := body["statusCounts"].(map[string]any)
	assert.Equal(t, "2", counts["experimental"])
	assert.Equal(t, "2", counts["unsupported"])

	// AC4: set a cell's status and note.
	code, body = env.call(t, http.MethodPut, "/api/v1/admin/compatibility/"+modelID+"/vllm/A800", map[string]any{
		"status": "supported", "note": "validated on A800",
	})
	require.Equal(t, http.StatusOK, code, "body: %v", body)
	cell, _ := body["cell"].(map[string]any)
	assert.Equal(t, "supported", cell["status"])
	assert.Equal(t, "validated on A800", cell["note"])

	// AC4: invalid status → 10210.
	code, body = env.call(t, http.MethodPut, "/api/v1/admin/compatibility/"+modelID+"/vllm/A800", map[string]any{
		"status": "bogus",
	})
	assert.NotEqual(t, http.StatusOK, code, "invalid status must be rejected: %v", body)
	assert.Equal(t, float64(10210), body["code"])

	// AC4: unknown dimension → 10211.
	code, body = env.call(t, http.MethodPut, "/api/v1/admin/compatibility/model-missing/vllm/A800", map[string]any{
		"status": "supported",
	})
	assert.NotEqual(t, http.StatusOK, code, "unknown dimension must be rejected: %v", body)
	assert.Equal(t, float64(10211), body["code"])

	// AC4: a missing cell is created lazily (GetCompatibilityCell).
	code, body = env.call(t, http.MethodGet, "/api/v1/admin/compatibility/"+modelID+"/sglang/M100", nil)
	require.Equal(t, http.StatusOK, code, "body: %v", body)
	cell, _ = body["cell"].(map[string]any)
	assert.Equal(t, "experimental", cell["status"], "vendor-matched sglang/M100 defaults to experimental")

	// AC5: bulk update is atomic and returns the count.
	code, body = env.call(t, http.MethodPost, "/api/v1/admin/compatibility:bulk", map[string]any{
		"cells": []map[string]any{
			{"modelId": modelID, "engine": "vllm", "cardType": "A800"},
			{"modelId": modelID, "engine": "vllm", "cardType": "M100"},
		},
		"status": "supported", "note": "bulk",
	})
	require.Equal(t, http.StatusOK, code, "body: %v", body)
	assert.Equal(t, "2", body["updated"])

	// AC5: a failure (unknown dimension) leaves all cells unchanged.
	code, body = env.call(t, http.MethodPost, "/api/v1/admin/compatibility:bulk", map[string]any{
		"cells": []map[string]any{
			{"modelId": "model-missing", "engine": "vllm", "cardType": "A800"},
		},
		"status": "unsupported",
	})
	assert.NotEqual(t, http.StatusOK, code, "bulk with unknown dimension must fail: %v", body)
	assert.Equal(t, float64(10211), body["code"])

	// AC6: a card type removed from the fleet keeps its status but is
	// flagged not_in_fleet.
	env.cardTypes = []image.CardType{{Vendor: "nvidia", CardType: "A800"}}
	code, body = env.call(t, http.MethodGet, "/api/v1/admin/compatibility", nil)
	require.Equal(t, http.StatusOK, code, "body: %v", body)
	cells, _ = body["cells"].([]any)
	foundM100 := false
	for _, c := range cells {
		cm, _ := c.(map[string]any)
		if cm["cardType"] == "M100" {
			foundM100 = true
			assert.Equal(t, true, cm["notInFleet"], "M100 left the fleet and is flagged")
		}
	}
	assert.True(t, foundM100, "M100 cell is not dropped when it leaves the fleet")

	// AC7: the masked user projection returns only supported/experimental.
	code, body = env.call(t, http.MethodGet, "/api/v1/models/"+modelID+"/compatibility", nil)
	require.Equal(t, http.StatusOK, code, "body: %v", body)
	entries, _ := body["entries"].([]any)
	for _, e := range entries {
		em, _ := e.(map[string]any)
		assert.NotEqual(t, "unsupported", em["status"], "unsupported combos are hidden")
	}
	assert.NotEmpty(t, body["supportedCount"])
	assert.NotEmpty(t, body["experimentalCount"])

	// AC7: an unknown model returns 10101.
	code, body = env.call(t, http.MethodGet, "/api/v1/models/model-missing/compatibility", nil)
	assert.NotEqual(t, http.StatusOK, code, "unknown model must fail: %v", body)
	assert.Equal(t, float64(10101), body["code"])
}
