package fvt

import (
	"bytes"
	"context"
	"encoding/json"
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
	promptv1 "github.com/go-taas/go-taas/proto/taas/prompt/v1"
	"github.com/go-taas/go-taas/services/prompt"
	"github.com/go-taas/go-taas/services/tenancy"
)

// promptEnv is the in-process stack for the prompt management feature.
type promptEnv struct {
	db    *gorm.DB
	gwSrv *httptest.Server
	svc   *prompt.Service
}

func newPromptEnv(t *testing.T) *promptEnv {
	t.Helper()

	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, prompt.MigrateSchemaForFVT(db))
	require.NoError(t, tenancy.MigrateSchemaForFVT(db))
	for _, orgID := range []string{"org-fvt", "org-a", "org-b"} {
		require.NoError(t, db.Create(&tenancy.Organization{
			ID: orgID, DisplayName: orgID, State: tenancy.StateActive,
		}).Error)
	}
	t.Cleanup(func() {
		sqlDB, _ := db.DB()
		_ = sqlDB.Close()
	})

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	grpcSrv := grpc.NewServer(grpc.ChainUnaryInterceptor(grpcmiddleware.UnaryServerInterceptor()))
	t.Cleanup(grpcSrv.Stop)

	svc := prompt.NewForFVT(db)

	promptv1.RegisterPromptServiceServer(grpcSrv, svc)
	go func() { _ = grpcSrv.Serve(ln) }()

	conn, err := grpc.NewClient(ln.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })

	mux := runtime.NewServeMux(runtime.WithIncomingHeaderMatcher(server.FVTHeaderMatcher))
	require.NoError(t, promptv1.RegisterPromptServiceHandler(context.Background(), mux, conn))

	gwSrv := httptest.NewServer(mux)
	t.Cleanup(gwSrv.Close)

	return &promptEnv{db: db, gwSrv: gwSrv, svc: svc}
}

func (e *promptEnv) call(t *testing.T, method, path string, body any, org string) (int, map[string]any) {
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

// TestFVTPromptManagement walks the acceptance criteria of the prompt
// management architecture doc (feature #43): AC1 (create returns version
// 1, 12703/12705/12706), AC2 (update/versions/rollback + 12702), AC3
// (folders + 12708), AC4 (usage), AC5 (tenant-scoped), AC6 (admin
// masked + templates + copy).
func TestFVTPromptManagement(t *testing.T) {
	env := newPromptEnv(t)

	// AC1: create a prompt returns version=1.
	code, body := env.call(t, http.MethodPost, "/api/v1/prompts", map[string]any{
		"name": "greeting", "content": "Hello ${topic}", "variables": []string{"${topic}"},
	}, "org-fvt")
	require.Equal(t, http.StatusOK, code, "body: %v", body)
	promptObj, _ := body["prompt"].(map[string]any)
	require.NotNil(t, promptObj)
	promptID, _ := promptObj["promptId"].(string)
	require.NotEmpty(t, promptID)
	assert.Equal(t, float64(1), promptObj["version"])

	// AC1: duplicate name -> 12703.
	code, body = env.call(t, http.MethodPost, "/api/v1/prompts", map[string]any{
		"name": "greeting", "content": "again",
	}, "org-fvt")
	assert.NotEqual(t, http.StatusOK, code, "duplicate name must be rejected: %v", body)
	assert.Equal(t, float64(12703), body["code"])

	// AC1: empty content -> 12705.
	code, body = env.call(t, http.MethodPost, "/api/v1/prompts", map[string]any{
		"name": "x", "content": "",
	}, "org-fvt")
	assert.NotEqual(t, http.StatusOK, code, "empty content must be rejected: %v", body)
	assert.Equal(t, float64(12705), body["code"])

	// AC1: invalid variable -> 12706.
	code, body = env.call(t, http.MethodPost, "/api/v1/prompts", map[string]any{
		"name": "x", "content": "hi", "variables": []string{"bad"},
	}, "org-fvt")
	assert.NotEqual(t, http.StatusOK, code, "invalid variable must be rejected: %v", body)
	assert.Equal(t, float64(12706), body["code"])

	// AC2: update creates version 2.
	code, body = env.call(t, http.MethodPost, "/api/v1/prompts/"+promptID, map[string]any{
		"content": "v2",
	}, "org-fvt")
	require.Equal(t, http.StatusOK, code, "body: %v", body)
	updated, _ := body["prompt"].(map[string]any)
	assert.Equal(t, float64(2), updated["version"])

	// AC2: list versions.
	code, body = env.call(t, http.MethodGet, "/api/v1/prompts/"+promptID+"/versions", nil, "org-fvt")
	require.Equal(t, http.StatusOK, code, "body: %v", body)
	versions, _ := body["versions"].([]any)
	require.Len(t, versions, 2)

	// AC2: rollback to version 1.
	code, body = env.call(t, http.MethodPost, "/api/v1/prompts/"+promptID+"/rollback", map[string]any{
		"version": 1,
	}, "org-fvt")
	require.Equal(t, http.StatusOK, code, "body: %v", body)
	rolled, _ := body["prompt"].(map[string]any)
	assert.Equal(t, float64(1), rolled["version"])

	// AC2: rollback to unknown version -> 12702.
	code, body = env.call(t, http.MethodPost, "/api/v1/prompts/"+promptID+"/rollback", map[string]any{
		"version": 99,
	}, "org-fvt")
	assert.NotEqual(t, http.StatusOK, code, "unknown version must be rejected: %v", body)
	assert.Equal(t, float64(12702), body["code"])

	// AC3: create a folder.
	code, body = env.call(t, http.MethodPost, "/api/v1/prompts/folders", map[string]any{
		"name": "work",
	}, "org-fvt")
	require.Equal(t, http.StatusOK, code, "body: %v", body)
	folder, _ := body["folder"].(map[string]any)
	folderID, _ := folder["folderId"].(string)
	require.NotEmpty(t, folderID)

	// AC3: list folders.
	code, body = env.call(t, http.MethodGet, "/api/v1/prompts/folders", nil, "org-fvt")
	require.Equal(t, http.StatusOK, code, "body: %v", body)
	folders, _ := body["folders"].([]any)
	require.Len(t, folders, 1)

	// AC3: delete empty folder.
	code, body = env.call(t, http.MethodDelete, "/api/v1/prompts/folders/"+folderID, nil, "org-fvt")
	require.Equal(t, http.StatusOK, code, "body: %v", body)

	// AC4: record usage.
	code, body = env.call(t, http.MethodPost, "/api/v1/prompts/"+promptID+"/usage", map[string]any{}, "org-fvt")
	require.Equal(t, http.StatusOK, code, "body: %v", body)
	assert.Equal(t, "1", body["timesUsed"])

	// AC4: get usage.
	code, body = env.call(t, http.MethodGet, "/api/v1/prompts/"+promptID+"/usage", nil, "org-fvt")
	require.Equal(t, http.StatusOK, code, "body: %v", body)
	assert.Equal(t, "1", body["timesUsed"])

	// AC5: tenant-scoped — org-b cannot see org-fvt's prompt.
	code, body = env.call(t, http.MethodGet, "/api/v1/prompts/"+promptID, nil, "org-b")
	assert.NotEqual(t, http.StatusOK, code, "cross-tenant get must be rejected: %v", body)
	assert.Equal(t, float64(12701), body["code"])

	// AC6: admin creates a template.
	code, body = env.call(t, http.MethodPost, "/api/v1/admin/prompts/templates", map[string]any{
		"name": "tpl", "content": "template content",
	}, "org-fvt")
	require.Equal(t, http.StatusOK, code, "body: %v", body)
	tpl, _ := body["template"].(map[string]any)
	tplID, _ := tpl["templateId"].(string)
	require.NotEmpty(t, tplID)

	// AC6: user lists templates.
	code, body = env.call(t, http.MethodGet, "/api/v1/prompts/templates", nil, "org-fvt")
	require.Equal(t, http.StatusOK, code, "body: %v", body)
	templates, _ := body["templates"].([]any)
	require.Len(t, templates, 1)

	// AC6: user copies the template.
	code, body = env.call(t, http.MethodPost, "/api/v1/prompts/templates/"+tplID+"/copy", map[string]any{}, "org-fvt")
	require.Equal(t, http.StatusOK, code, "body: %v", body)
	copied, _ := body["prompt"].(map[string]any)
	assert.Equal(t, float64(1), copied["version"])
	assert.Equal(t, "template content", copied["content"])

	// AC6: admin lists all prompts (masked).
	code, body = env.call(t, http.MethodGet, "/api/v1/admin/prompts", nil, "org-fvt")
	require.Equal(t, http.StatusOK, code, "body: %v", body)
	adminPrompts, _ := body["prompts"].([]any)
	require.Len(t, adminPrompts, 2)

	// AC6: admin usage analytics.
	code, body = env.call(t, http.MethodGet, "/api/v1/admin/prompts/usage", nil, "org-fvt")
	require.Equal(t, http.StatusOK, code, "body: %v", body)
	assert.Equal(t, "2", body["totalPrompts"])

	// AC6: admin deletes the template.
	code, body = env.call(t, http.MethodDelete, "/api/v1/admin/prompts/templates/"+tplID, nil, "org-fvt")
	require.Equal(t, http.StatusOK, code, "body: %v", body)
}
