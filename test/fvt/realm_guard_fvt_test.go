// Feature-17 (console surface separation) full-verification suite: the
// gateway realm guard over a real auth service, exercising the
// acceptance criteria of docs/architecture/console-surface-separation.md
// (AC9, AC10, AC11, AC24).

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
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	"github.com/go-taas/go-taas/pkg/grpcmiddleware"
	"github.com/go-taas/go-taas/pkg/server"
	authv1 "github.com/go-taas/go-taas/proto/taas/auth/v1"
	"github.com/go-taas/go-taas/services/auth"
	"github.com/go-taas/go-taas/services/tenancy"
)

// realmEnv is the in-process stack for the realm guard: the auth service
// over one gRPC server, with the gateway mux wrapped in the production
// RealmGuard.
type realmEnv struct {
	gwSrv *httptest.Server
	svc   *auth.Service
}

func newRealmEnv(t *testing.T) *realmEnv {
	t.Helper()

	dbPath := filepath.Join(t.TempDir(), "realm.db")
	db, err := gorm.Open(sqlite.Open(dbPath), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, auth.MigrateSchemaForFVT(db))
	require.NoError(t, tenancy.MigrateSchemaForFVT(db))
	for _, orgID := range []string{"org-fvt", "org-a"} {
		require.NoError(t, db.Create(&tenancy.Organization{
			ID: orgID, DisplayName: orgID, State: tenancy.StateActive,
		}).Error)
	}
	sqlDB, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })

	redisSrv := newFakeRedisServer(t)
	redisClient := redis.NewClient(&redis.Options{Addr: redisSrv.addr})
	t.Cleanup(func() { _ = redisClient.Close() })

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	grpcSrv := grpc.NewServer(grpc.ChainUnaryInterceptor(grpcmiddleware.UnaryServerInterceptor()))
	t.Cleanup(grpcSrv.Stop)

	svc := auth.NewForFVT(db)
	svc.SetOrgGuard(tenancy.NewOrgGuard(db))
	svc.SetSessionStore(auth.NewSessionStore(redisClient, time.Hour))
	authv1.RegisterAuthServiceServer(grpcSrv, svc)
	go func() { _ = grpcSrv.Serve(ln) }()

	conn, err := grpc.NewClient(ln.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })

	mux := runtime.NewServeMux(
		runtime.WithIncomingHeaderMatcher(server.FVTHeaderMatcher),
	)
	require.NoError(t, authv1.RegisterAuthServiceHandler(context.Background(), mux, conn))

	// Wrap the mux in the production realm guard (feature-17 AD2/AD3).
	gwSrv := httptest.NewServer(server.RealmGuard(mux, svc))
	t.Cleanup(gwSrv.Close)

	return &realmEnv{gwSrv: gwSrv, svc: svc}
}

// realmCall performs an HTTP call against the realm-guarded gateway.
func (e *realmEnv) realmCall(t *testing.T, method, path string, headers map[string]string) (int, map[string]any) {
	t.Helper()
	req, err := http.NewRequest(method, e.gwSrv.URL+path, nil)
	require.NoError(t, err)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	var body map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&body)
	return resp.StatusCode, body
}

// seedRealmSession creates a session with the given realm and returns its
// token.
func (e *realmEnv) seedRealmSession(t *testing.T, realm string) string {
	t.Helper()
	sess := &auth.Session{
		SessionID:      "sess-" + realm + "-" + t.Name(),
		UserID:         "user-1",
		Username:       "alice",
		AccessibleOrgs: []string{"org-fvt"},
		ActiveOrg:      "org-fvt",
		ExpiresAt:      time.Now().Add(time.Hour).Unix(),
		CreatedAt:      time.Now().Unix(),
		Realm:          realm,
	}
	require.NoError(t, e.svc.CreateSessionForTest(context.Background(), sess, "token"))
	return sess.SessionID
}

// TestRealmGuardCrossRealmRejection covers AC9: a user-realm session on
// the admin prefix answers 10038, and a realm-less session answers 10027
// (AC24).
func TestRealmGuardCrossRealmRejection(t *testing.T) {
	env := newRealmEnv(t)

	// A user-realm session on the admin prefix → 10038 (AC9).
	userToken := env.seedRealmSession(t, auth.RealmUser)
	code, body := env.realmCall(t, http.MethodGet, "/api/v1/admin/auth/session",
		map[string]string{"Authorization": "Bearer " + userToken})
	assert.Equal(t, http.StatusInternalServerError, code)
	assert.EqualValues(t, 10038, body["code"])

	// The same user-realm session on the user prefix → 200 (AC10).
	code, body = env.realmCall(t, http.MethodGet, "/api/v1/auth/session",
		map[string]string{"Authorization": "Bearer " + userToken})
	assert.Equal(t, http.StatusOK, code)
	assert.Equal(t, "user", body["realm"])

	// An admin-realm session on the user prefix → 10038.
	adminToken := env.seedRealmSession(t, auth.RealmAdmin)
	code, body = env.realmCall(t, http.MethodGet, "/api/v1/auth/session",
		map[string]string{"Authorization": "Bearer " + adminToken})
	assert.Equal(t, http.StatusInternalServerError, code)
	assert.EqualValues(t, 10038, body["code"])

	// A realm-less session → 10027 (AC24).
	realmless := env.seedRealmSession(t, "")
	code, body = env.realmCall(t, http.MethodGet, "/api/v1/auth/session",
		map[string]string{"Authorization": "Bearer " + realmless})
	assert.Equal(t, http.StatusInternalServerError, code)
	assert.EqualValues(t, 10027, body["code"])
}

// TestRealmGuardRealmPinnedSession covers AC10: GetSession returns the
// realm of the session on its own prefix.
func TestRealmGuardRealmPinnedSession(t *testing.T) {
	env := newRealmEnv(t)
	userToken := env.seedRealmSession(t, auth.RealmUser)
	code, body := env.realmCall(t, http.MethodGet, "/api/v1/auth/session",
		map[string]string{"Authorization": "Bearer " + userToken})
	assert.Equal(t, http.StatusOK, code)
	assert.Equal(t, "user", body["realm"])
	assert.Equal(t, "org-fvt", body["activeOrg"])
}
