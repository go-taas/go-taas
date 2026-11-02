// API key ownership full-verification suite: a key belongs to a person
// inside an organization and optionally a project. The suite exercises
// the create/list/manage authorization matrix, project validation, the
// ownership transfer, and the three-level quota the data-plane verdict
// carries.
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
	"time"

	"github.com/grpc-ecosystem/grpc-gateway/v2/runtime"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	authv1 "github.com/go-taas/go-taas/proto/taas/auth/v1"
	tenancyv1 "github.com/go-taas/go-taas/proto/taas/tenancy/v1"

	"github.com/go-taas/go-taas/pkg/grpcmiddleware"
	"github.com/go-taas/go-taas/pkg/server"
	"github.com/go-taas/go-taas/services/auth"
	"github.com/go-taas/go-taas/services/tenancy"
)

// ownershipFvtEnv is the in-process stack for API key ownership.
type ownershipFvtEnv struct {
	db     *gorm.DB
	gwSrv  *httptest.Server
	conn   *grpc.ClientConn
	auth   *auth.Service
	ten    *tenancy.Service
	tokens map[string]string
}

// newOwnershipFvtEnv wires the real auth and tenancy services over one
// SQLite database, with real Redis-backed sessions (the fake RESP
// server) so the ownership checks run against the production session
// path.
func newOwnershipFvtEnv(t *testing.T) *ownershipFvtEnv {
	t.Helper()

	db, err := gorm.Open(sqlite.Open(fmt.Sprintf("file:%s?mode=memory&cache=shared", t.Name())), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, auth.MigrateSchemaForFVT(db))
	require.NoError(t, tenancy.MigrateSchemaForFVT(db))
	for _, orgID := range []string{"org-fvt", "org-other"} {
		require.NoError(t, db.Create(&tenancy.Organization{
			ID: orgID, DisplayName: orgID, State: tenancy.StateActive,
		}).Error)
	}
	for _, m := range []*tenancy.OrgMember{
		{OrganizationID: "org-fvt", UserID: "admin-fvt", Role: tenancy.RoleAdmin, JoinedAt: time.Now().UTC()},
		{OrganizationID: "org-fvt", UserID: "alice", Role: tenancy.RoleMember, JoinedAt: time.Now().UTC()},
		{OrganizationID: "org-fvt", UserID: "bob", Role: tenancy.RoleMember, JoinedAt: time.Now().UTC()},
	} {
		require.NoError(t, db.Create(m).Error)
	}
	for _, p := range []*tenancy.Project{
		{ID: "proj-a", OrganizationID: "org-fvt", DisplayName: "Project A", State: tenancy.StateActive},
		{ID: "proj-other", OrganizationID: "org-other", DisplayName: "Other", State: tenancy.StateActive},
	} {
		require.NoError(t, db.Create(p).Error)
	}
	require.NoError(t, db.Create(&tenancy.ProjectMember{
		ProjectID: "proj-a", UserID: "alice", Role: tenancy.RoleMember, JoinedAt: time.Now().UTC(),
	}).Error)
	t.Cleanup(func() {
		sqlDB, _ := db.DB()
		_ = sqlDB.Close()
	})

	redisSrv := newFakeRedisServer(t)
	redisClient := redis.NewClient(&redis.Options{Addr: redisSrv.addr})
	t.Cleanup(func() { _ = redisClient.Close() })

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	grpcSrv := grpc.NewServer(grpc.ChainUnaryInterceptor(grpcmiddleware.UnaryServerInterceptor()))
	t.Cleanup(grpcSrv.Stop)

	authSvc := auth.NewForFVT(db)
	authSvc.SetOrgGuard(tenancy.NewOrgGuard(db))
	authSvc.SetSessionStore(auth.NewSessionStore(redisClient, time.Hour))
	authSvc.SetRoleGuard(tenancy.NewRoleGuard(db))

	// Tenancy resolves the same real session, so the admin gates see the
	// actual caller rather than a fixed test user.
	tenSvc := tenancy.NewForFVT(db)
	tenSvc.SetRoleGuard(tenancy.NewRoleGuard(db))
	tenSvc.SetSessionResolver(authSvc)
	tenSvc.SetSessionOrgResolver(authSvc)
	tenSvc.SetSessionRolesResolver(authSvc)

	authSvc.SetProjectLookup(tenSvc)
	authSvc.SetQuotaResolver(tenSvc)

	tenancyv1.RegisterTenancyServiceServer(grpcSrv, tenSvc)
	authv1.RegisterAuthServiceServer(grpcSrv, authSvc)
	go func() { _ = grpcSrv.Serve(ln) }()

	conn, err := grpc.NewClient(ln.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })

	mux := runtime.NewServeMux(runtime.WithIncomingHeaderMatcher(server.FVTHeaderMatcher))
	require.NoError(t, tenancyv1.RegisterTenancyServiceHandler(context.Background(), mux, conn))
	require.NoError(t, authv1.RegisterAuthServiceHandler(context.Background(), mux, conn))
	gwSrv := httptest.NewServer(mux)
	t.Cleanup(gwSrv.Close)

	env := &ownershipFvtEnv{db: db, gwSrv: gwSrv, conn: conn, auth: authSvc, ten: tenSvc, tokens: map[string]string{}}
	for _, user := range []string{"admin-fvt", "alice", "bob"} {
		// The session store is keyed by session id; the gateway forwards the
		// bearer value the session layer looks up.
		token := "sess-" + user
		require.NoError(t, authSvc.CreateSessionForTest(context.Background(), &auth.Session{
			SessionID:      token,
			UserID:         user,
			Username:       user,
			Roles:          []string{tenancy.RoleMember},
			AccessibleOrgs: []string{"org-fvt"},
			ActiveOrg:      "org-fvt",
			ExpiresAt:      time.Now().Add(time.Hour).Unix(),
			CreatedAt:      time.Now().Unix(),
			Realm:          auth.RealmUser,
		}, token))
		env.tokens[user] = token
	}
	return env
}

// call issues a JSON request as one of the seeded users.
func (e *ownershipFvtEnv) call(t *testing.T, user, method, path string, body any) (int, map[string]any) {
	t.Helper()
	raw, err := json.Marshal(body)
	require.NoError(t, err)
	var rdr *bytes.Reader
	if body == nil {
		rdr = bytes.NewReader(nil)
	} else {
		rdr = bytes.NewReader(raw)
	}
	req, err := http.NewRequest(method, e.gwSrv.URL+path, rdr)
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	// Grpc-Metadata-Authorization is the gateway's documented pass-through
	// for an Authorization header onto the gRPC metadata key the session
	// layer reads.
	req.Header.Set("Grpc-Metadata-Authorization", "Bearer "+e.tokens[user])
	resp, err := e.gwSrv.Client().Do(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	out := map[string]any{}
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

// sessionCtx builds a context carrying one seeded user's session, for
// direct in-process service calls that skip the HTTP gateway.
func (e *ownershipFvtEnv) sessionCtx(user string) context.Context {
	return metadata.NewIncomingContext(context.Background(), metadata.Pairs(
		"authorization", "Bearer "+e.tokens[user]))
}

// createKey creates a key and returns its id.
func (e *ownershipFvtEnv) createKey(t *testing.T, user string, body map[string]any) (string, map[string]any) {
	t.Helper()
	code, out := e.call(t, user, http.MethodPost, "/api/v1/auth/api-keys", body)
	require.Equal(t, http.StatusOK, code, "create key: %v", out)
	return out["keyId"].(string), out
}

// TestFVTApiKeyOwnerLifecycle covers the ownership create/list matrix.
func TestFVTApiKeyOwnerLifecycle(t *testing.T) {
	env := newOwnershipFvtEnv(t)

	// A member's key belongs to that member.
	aliceKey, _ := env.createKey(t, "alice", map[string]any{"name": "alice-key"})

	code, body := env.call(t, "alice", http.MethodGet, "/api/v1/auth/api-keys", nil)
	require.Equal(t, http.StatusOK, code, "alice list: %v", body)
	keys := body["keys"].([]any)
	require.Len(t, keys, 1, "alice sees only her own key")
	aliceRow := keys[0].(map[string]any)
	assert.Equal(t, aliceKey, aliceRow["keyId"])
	assert.Equal(t, "alice", aliceRow["ownerUserId"])
	assert.Equal(t, false, aliceRow["shared"])
	assert.Equal(t, "org-fvt", aliceRow["organizationId"])

	// Bob does not see alice's key by default, and cannot manage it.
	code, body = env.call(t, "bob", http.MethodGet, "/api/v1/auth/api-keys", nil)
	require.Equal(t, http.StatusOK, code, "bob list: %v", body)
	assert.Empty(t, body["keys"], "bob has no keys")

	_, body = env.call(t, "bob", http.MethodPost, "/api/v1/auth/api-keys/"+aliceKey+":revoke", nil)
	assert.EqualValues(t, 10007, body["code"], "bob cannot revoke alice's key: %v", body)

	// An administrator sees every key of the organization.
	code, body = env.call(t, "admin-fvt", http.MethodGet, "/api/v1/auth/api-keys?scope=all", nil)
	require.Equal(t, http.StatusOK, code, "admin list: %v", body)
	assert.Len(t, body["keys"].([]any), 1)

	// A plain member may not ask for the organization-wide view.
	_, body = env.call(t, "alice", http.MethodGet, "/api/v1/auth/api-keys?scope=all", nil)
	assert.EqualValues(t, 10036, body["code"], "alice all-scope: %v", body)

	// An administrator creates an organization-shared key.
	sharedKey, _ := env.createKey(t, "admin-fvt", map[string]any{"name": "shared", "shared": true})
	code, body = env.call(t, "alice", http.MethodGet, "/api/v1/auth/api-keys?scope=shared", nil)
	require.Equal(t, http.StatusOK, code, "shared list: %v", body)
	sharedRows := body["keys"].([]any)
	require.Len(t, sharedRows, 1)
	assert.Equal(t, sharedKey, sharedRows[0].(map[string]any)["keyId"])
	assert.Equal(t, true, sharedRows[0].(map[string]any)["shared"])

	// A member cannot manage a shared key (administrators only).
	_, body = env.call(t, "alice", http.MethodPost, "/api/v1/auth/api-keys/"+sharedKey+":revoke", nil)
	assert.EqualValues(t, 10007, body["code"], "alice shared revoke: %v", body)

	// An administrator transfers alice's key to bob.
	code, body = env.call(t, "admin-fvt", http.MethodPut, "/api/v1/admin/auth/api-keys/"+aliceKey+"/ownership", map[string]any{
		"ownerUserId": "bob",
	})
	require.Equal(t, http.StatusOK, code, "transfer: %v", body)
	assert.Equal(t, "bob", body["key"].(map[string]any)["ownerUserId"])

	// Bob now owns and manages it; alice no longer sees it.
	code, body = env.call(t, "bob", http.MethodPut, "/api/v1/auth/api-keys/"+aliceKey, map[string]any{
		"name": "bob-renamed", "rateLimitRpm": 10,
	})
	require.Equal(t, http.StatusOK, code, "bob update: %v", body)
	code, body = env.call(t, "alice", http.MethodGet, "/api/v1/auth/api-keys", nil)
	require.Equal(t, http.StatusOK, code)
	assert.Empty(t, body["keys"], "alice no longer owns the key")

	// A member cannot transfer ownership.
	_, body = env.call(t, "bob", http.MethodPut, "/api/v1/admin/auth/api-keys/"+aliceKey+"/ownership", map[string]any{
		"ownerUserId": "alice",
	})
	assert.EqualValues(t, 10036, body["code"], "member transfer: %v", body)

	// An administrator clears the owner: the key becomes organization-shared
	// (the empty owner is never read as "assign the caller").
	code, body = env.call(t, "admin-fvt", http.MethodPut, "/api/v1/admin/auth/api-keys/"+aliceKey+"/ownership", map[string]any{
		"ownerUserId": "",
	})
	require.Equal(t, http.StatusOK, code, "clear owner: %v", body)
	assert.Equal(t, true, body["key"].(map[string]any)["shared"])
	assert.Empty(t, body["key"].(map[string]any)["ownerUserId"])
}

// TestFVTApiKeyProjectScope covers project assignment and validation.
func TestFVTApiKeyProjectScope(t *testing.T) {
	env := newOwnershipFvtEnv(t)

	// Alice belongs to proj-a, so she may place a key there.
	code, body := env.call(t, "alice", http.MethodPost, "/api/v1/auth/api-keys", map[string]any{
		"name": "proj-key", "projectId": "proj-a",
	})
	require.Equal(t, http.StatusOK, code, "project key: %v", body)
	keyID := body["keyId"].(string)

	code, body = env.call(t, "alice", http.MethodGet, "/api/v1/auth/api-keys?scope=mine", nil)
	require.Equal(t, http.StatusOK, code)
	row := body["keys"].([]any)[0].(map[string]any)
	assert.Equal(t, keyID, row["keyId"])
	assert.Equal(t, "proj-a", row["projectId"])
	assert.Equal(t, "Project A", row["projectDisplayName"])

	// A project of another organization is rejected.
	code, body = env.call(t, "alice", http.MethodPost, "/api/v1/auth/api-keys", map[string]any{
		"name": "bad-project", "projectId": "proj-other",
	})
	assert.NotEqual(t, http.StatusOK, code, "cross-org project: %v", body)
	assert.EqualValues(t, 10008, body["code"])

	// A member may not place a key in a project they do not belong to.
	_, body = env.call(t, "bob", http.MethodPost, "/api/v1/auth/api-keys", map[string]any{
		"name": "not-my-project", "projectId": "proj-a",
	})
	assert.EqualValues(t, 10036, body["code"], "non-member project: %v", body)

	// Filtering by project returns the assigned key.
	code, body = env.call(t, "admin-fvt", http.MethodGet, "/api/v1/auth/api-keys?scope=all&project_id=proj-a", nil)
	require.Equal(t, http.StatusOK, code, "project filter: %v", body)
	assert.Len(t, body["keys"].([]any), 1)
}

// TestFVTApiKeyVerdictOwnershipQuota covers the data-plane verdict: the
// owner/project attribution and the minimum of the three quota tiers.
func TestFVTApiKeyVerdictOwnershipQuota(t *testing.T) {
	env := newOwnershipFvtEnv(t)
	ctx := env.sessionCtx("admin-fvt")

	// Organization cap 10 requests, project cap 20, personal cap 5.
	_, err := env.ten.UpdateOrganizationQuota(ctx, &tenancyv1.UpdateOrganizationQuotaRequest{
		OrganizationId:        "org-fvt",
		MaxConcurrentRequests: ptrInt64(10),
	})
	require.NoError(t, err)
	_, err = env.ten.UpdateProjectQuota(ctx, &tenancyv1.UpdateProjectQuotaRequest{
		ProjectId:             "proj-a",
		MaxConcurrentRequests: ptrInt64(20),
	})
	require.NoError(t, err)
	_, err = env.ten.UpdateUserQuota(ctx, &tenancyv1.UpdateUserQuotaRequest{
		UserId:                "alice",
		OrganizationId:        "org-fvt",
		MaxConcurrentRequests: ptrInt64(5),
	})
	require.NoError(t, err)

	_, created := env.createKey(t, "alice", map[string]any{
		"name": "scoped-key", "projectId": "proj-a",
	})
	plaintext := created["apiKey"].(string)

	client := authv1.NewAuthServiceClient(env.conn)
	verdict, err := client.VerifyAPIKey(context.Background(), &authv1.VerifyAPIKeyRequest{KeyDigest: auth.KeyDigest(plaintext)})
	require.NoError(t, err)
	assert.Equal(t, "alice", verdict.GetOwnerUserId())
	assert.Equal(t, "proj-a", verdict.GetProjectId())
	assert.Equal(t, "org-fvt", verdict.GetOrganizationId())
	assert.Equal(t, int64(5), verdict.GetMaxConcurrentRequests(), "personal cap is the strictest")

	// Removing the personal cap leaves the organization cap (10).
	_, err = env.ten.UpdateUserQuota(ctx, &tenancyv1.UpdateUserQuotaRequest{
		UserId:                "alice",
		OrganizationId:        "org-fvt",
		MaxConcurrentRequests: ptrInt64(0),
	})
	require.NoError(t, err)
	// A new key gets a fresh verdict (the positive cache is keyed by digest).
	_, created2 := env.createKey(t, "alice", map[string]any{
		"name": "scoped-key-2", "projectId": "proj-a",
	})
	verdict, err = client.VerifyAPIKey(context.Background(), &authv1.VerifyAPIKeyRequest{
		KeyDigest: auth.KeyDigest(created2["apiKey"].(string)),
	})
	require.NoError(t, err)
	assert.Equal(t, int64(10), verdict.GetMaxConcurrentRequests())
}

// ptrInt64 returns a pointer for the proto3 optional fields.
func ptrInt64(v int64) *int64 { return &v }
