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
	"strings"
	"testing"
	"time"

	"github.com/grpc-ecosystem/grpc-gateway/v2/runtime"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	apierrors "github.com/go-taas/go-taas/pkg/errors"
	"github.com/go-taas/go-taas/pkg/grpcmiddleware"
	"github.com/go-taas/go-taas/pkg/server"
	auditv1 "github.com/go-taas/go-taas/proto/taas/audit/v1"
	"github.com/go-taas/go-taas/services/audit"
	"github.com/go-taas/go-taas/services/tenancy"
)

// auditEnv is the in-process stack for the audit feature: the audit
// service over one gRPC server with the gateway mux in front, a shared
// SQLite database, and the tenancy guard.
type auditEnv struct {
	db     *gorm.DB
	gwSrv  *httptest.Server
	grpcLn net.Listener

	svc *audit.Service
}

func newAuditEnv(t *testing.T) *auditEnv {
	t.Helper()

	dbPath := filepath.Join(t.TempDir(), "audit.db")
	db, err := gorm.Open(sqlite.Open(dbPath+"?_busy_timeout=10000&_txlock=immediate&_journal_mode=WAL"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, audit.MigrateSchemaForFVT(db))
	require.NoError(t, tenancy.MigrateSchemaForFVT(db))
	for _, orgID := range []string{"org-fvt", "org-a", "org-b"} {
		require.NoError(t, db.Create(&tenancy.Organization{
			ID: orgID, DisplayName: orgID, State: tenancy.StateActive,
		}).Error)
		// Seed a member for each org so the transitional (session-less)
		// path — where the caller is identified by the X-Organization-Id
		// header — passes the RoleGuard.
		require.NoError(t, db.Create(&tenancy.OrgMember{
			OrganizationID: orgID, UserID: orgID, Role: tenancy.RoleMember, JoinedAt: time.Now().UTC(),
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

	svc := audit.NewForFVT(db)
	svc.SetRoleGuard(tenancy.NewRoleGuard(db))
	auditv1.RegisterAuditServiceServer(grpcSrv, svc)
	go func() { _ = grpcSrv.Serve(ln) }()

	conn, err := grpc.NewClient(ln.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })

	mux := runtime.NewServeMux(
		runtime.WithIncomingHeaderMatcher(server.FVTHeaderMatcher),
	)
	require.NoError(t, auditv1.RegisterAuditServiceHandler(context.Background(), mux, conn))

	gwSrv := httptest.NewServer(mux)
	t.Cleanup(gwSrv.Close)

	return &auditEnv{db: db, gwSrv: gwSrv, grpcLn: ln, svc: svc}
}

// call issues a JSON request against the gateway and decodes the body.
func (e *auditEnv) call(t *testing.T, method, path string, body any, org string) (int, map[string]any) {
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

// recordEvent records an audit event through the internal gRPC recorder
// path (the production recorder call).
func (e *auditEnv) recordEvent(t *testing.T, orgID, actorID, action, resourceType, resourceID, result string) string {
	t.Helper()
	ctx := context.Background()
	resp, err := e.svc.RecordAuditEvent(ctx, &auditv1.RecordAuditEventRequest{
		OrganizationId: orgID,
		ActorUserId:    actorID,
		ActorType:      auditv1.AuditActorType_AUDIT_ACTOR_TYPE_USER,
		Action:         action,
		ResourceType:   resourceType,
		ResourceId:     resourceID,
		Result:         auditResultEnum(result),
		IpAddress:      "10.0.0.1",
		UserAgent:      "fvt-agent",
	})
	require.NoError(t, err)
	return resp.GetAuditEventId()
}

// auditResultEnum maps a result string to the proto enum.
func auditResultEnum(result string) auditv1.AuditResult {
	if result == "failure" {
		return auditv1.AuditResult_AUDIT_RESULT_FAILURE
	}
	return auditv1.AuditResult_AUDIT_RESULT_SUCCESS
}

// TestFVTAuditLogging walks the audit acceptance criteria through the
// gateway: record events (AC1/AC3), list/filter/drill (AC4/AC5), export
// (AC6), end-user scoping (AC7), admin org scoping (AC8).
func TestFVTAuditLogging(t *testing.T) {
	env := newAuditEnv(t)

	// AC1/AC3: record a success event (key revoke) and a failure event
	// (failed login) — both results are recorded.
	revokeID := env.recordEvent(t, "org-fvt", "u-1", "api_key.revoke", "api_key", "key-1", "success")
	env.recordEvent(t, "org-fvt", "u-1", "auth.login", "session", "sess-fail", "failure")

	// Debug: count stored events.
	var count int64
	require.NoError(t, env.db.Model(&audit.AuditEvent{}).Count(&count).Error)
	t.Logf("stored audit events: %d", count)

	// AC4: ListAuditEvents returns the events, newest first.
	code, body := env.call(t, http.MethodGet, "/api/v1/admin/audit/events", nil, "org-fvt")
	require.Equal(t, http.StatusOK, code, "body: %v", body)
	events, _ := body["auditEvents"].([]any)
	require.Len(t, events, 2, "both results are recorded")
	first, _ := events[0].(map[string]any)
	assert.Equal(t, "auth.login", first["action"], "newest first")
	assert.Equal(t, "AUDIT_RESULT_FAILURE", first["result"])

	// Filter by action.
	code, body = env.call(t, http.MethodGet, "/api/v1/admin/audit/events?action=api_key.revoke", nil, "org-fvt")
	require.Equal(t, http.StatusOK, code, "body: %v", body)
	events, _ = body["auditEvents"].([]any)
	require.Len(t, events, 1)
	assert.Equal(t, "api_key.revoke", events[0].(map[string]any)["action"])

	// AC4: range validation -> 10603.
	now := time.Now().Unix()
	code, body = env.call(t, http.MethodGet,
		fmt.Sprintf("/api/v1/admin/audit/events?since=%d&until=%d", now, now-10), nil, "org-fvt")
	assert.NotEqual(t, http.StatusOK, code)
	assert.EqualValues(t, float64(10603), body["code"])

	// AC5: GetAuditEvent returns full metadata; unknown id -> 10601.
	code, body = env.call(t, http.MethodGet, "/api/v1/admin/audit/events/"+revokeID, nil, "org-fvt")
	require.Equal(t, http.StatusOK, code, "body: %v", body)
	detail, _ := body["auditEvent"].(map[string]any)
	assert.Equal(t, "api_key.revoke", detail["action"])
	assert.Equal(t, "10.0.0.1", detail["ipAddress"])

	code, body = env.call(t, http.MethodGet,
		"/api/v1/admin/audit/events/00000000-0000-0000-0000-000000000000", nil, "org-fvt")
	assert.EqualValues(t, float64(10601), body["code"])
	_ = code

	// AC6: ExportAuditEvents returns CSV and JSON; invalid format ->
	// 10602.
	code, body = env.call(t, http.MethodGet, "/api/v1/admin/audit/events:export?format=AUDIT_EXPORT_FORMAT_CSV", nil, "org-fvt")
	require.Equal(t, http.StatusOK, code, "body: %v", body)
	content, _ := body["content"].(string)
	assert.True(t, strings.HasPrefix(content, "audit_event_id,"), "CSV has a header row")
	assert.Equal(t, false, body["truncated"])

	code, body = env.call(t, http.MethodGet, "/api/v1/admin/audit/events:export?format=AUDIT_EXPORT_FORMAT_JSON", nil, "org-fvt")
	require.Equal(t, http.StatusOK, code, "body: %v", body)
	content, _ = body["content"].(string)
	assert.True(t, strings.HasPrefix(content, "["), "JSON is an array")

	// AC6: an invalid format returns 10602. The gateway rejects an
	// invalid enum string before the handler, so exercise the gRPC-direct
	// path (the production recorder call surface).
	_, err := env.svc.ExportAuditEvents(context.Background(), &auditv1.ExportAuditEventsRequest{
		Format: auditv1.AuditExportFormat_AUDIT_EXPORT_FORMAT_UNSPECIFIED,
	})
	require.Error(t, err)
	ae, ok := apierrors.As(err)
	require.True(t, ok)
	assert.Equal(t, apierrors.CodeAuditExportInvalid, ae.Code)
}

// TestFVTAuditEndUserScoping walks AC7: ListMyActivity/ExportMyActivity
// return only the caller's own events.
func TestFVTAuditEndUserScoping(t *testing.T) {
	env := newAuditEnv(t)

	// In the transitional (session-less) path the caller is identified
	// by the X-Organization-Id header, so the caller's own events carry
	// actor_user_id = the org header value.
	env.recordEvent(t, "org-fvt", "org-fvt", "auth.login", "session", "s1", "success")
	env.recordEvent(t, "org-fvt", "org-a", "auth.login", "session", "s2", "success")

	// The end-user activity list is hard-scoped to the caller.
	code, body := env.call(t, http.MethodGet, "/api/v1/audit/activity", nil, "org-fvt")
	require.Equal(t, http.StatusOK, code, "body: %v", body)
	events, _ := body["auditEvents"].([]any)
	require.Len(t, events, 1, "only the caller's own events")
	first, _ := events[0].(map[string]any)
	assert.Equal(t, "org-fvt", first["actorUserId"], "hard-scoped to the caller")

	// ExportMyActivity returns the caller's events.
	code, body = env.call(t, http.MethodGet, "/api/v1/audit/activity:export?format=AUDIT_EXPORT_FORMAT_JSON", nil, "org-fvt")
	require.Equal(t, http.StatusOK, code, "body: %v", body)
	content, _ := body["content"].(string)
	assert.True(t, strings.Contains(content, "org-fvt"))
	assert.False(t, strings.Contains(content, "org-a"), "never exports another user's events")
}

// TestFVTAuditAdminOrgScoping walks AC8: org isolation — a caller only
// sees events for orgs they can access.
func TestFVTAuditAdminOrgScoping(t *testing.T) {
	env := newAuditEnv(t)

	env.recordEvent(t, "org-a", "org-a", "auth.login", "session", "s1", "success")

	// org-b never sees org-a's events.
	code, body := env.call(t, http.MethodGet, "/api/v1/admin/audit/events", nil, "org-b")
	require.Equal(t, http.StatusOK, code, "body: %v", body)
	events, _ := body["auditEvents"].([]any)
	assert.Len(t, events, 0, "org-b sees no org-a events")

	// org-a sees its own events.
	code, body = env.call(t, http.MethodGet, "/api/v1/admin/audit/events", nil, "org-a")
	require.Equal(t, http.StatusOK, code, "body: %v", body)
	events, _ = body["auditEvents"].([]any)
	assert.Len(t, events, 1, "org-a sees its own events")
}
