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
	tenancyv1 "github.com/go-taas/go-taas/proto/taas/tenancy/v1"
	"github.com/go-taas/go-taas/services/tenancy"
)

// rbacEnv is the in-process stack for the org-members RBAC feature: the
// tenancy service with the RoleGuard and a fake SessionResolver wired,
// over one gRPC server with the gateway mux in front.
type rbacEnv struct {
	db    *gorm.DB
	gwSrv *httptest.Server

	tenancySvc *tenancy.Service
}

// fakeRBACSession is a SessionResolver that returns a fixed caller.
type fakeRBACSession struct {
	userID string
	email  string
}

func (f *fakeRBACSession) SessionUserID(_ context.Context) (string, error) {
	return f.userID, nil
}

func (f *fakeRBACSession) SessionUserEmail(_ context.Context) (string, error) {
	return f.email, nil
}

func newRBACEnv(t *testing.T) *rbacEnv {
	t.Helper()

	cfg := &config.Configuration{}
	cfg.Tenancy.InvitationTTL = 7 * 24 * time.Hour
	config.SetConfigForTest(cfg)
	t.Cleanup(func() { config.SetConfigForTest(&config.Configuration{}) })

	dbPath := filepath.Join(t.TempDir(), "rbac.db")
	db, err := gorm.Open(sqlite.Open(dbPath), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, tenancy.MigrateSchemaForFVT(db))
	// The organization summary counts platform assets owned by other
	// modules; the tenancy-only fixture provides minimal stand-ins so the
	// list/delete paths run against the real service.
	require.NoError(t, db.Exec(`CREATE TABLE IF NOT EXISTS api_keys (
		id TEXT PRIMARY KEY, organization_id TEXT, revoked BOOLEAN DEFAULT 0)`).Error)
	require.NoError(t, db.Exec(`CREATE TABLE IF NOT EXISTS inference_services (
		id TEXT PRIMARY KEY, organization_id TEXT, state TEXT)`).Error)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	grpcSrv := grpc.NewServer(grpc.ChainUnaryInterceptor(grpcmiddleware.UnaryServerInterceptor()))
	t.Cleanup(grpcSrv.Stop)

	tenancySvc := tenancy.NewForFVT(db)
	tenancySvc.SetRoleGuard(tenancy.NewRoleGuard(db))
	tenancySvc.SetSessionResolver(&fakeRBACSession{userID: "u-admin", email: "admin@x.com"})

	tenancyv1.RegisterTenancyServiceServer(grpcSrv, tenancySvc)
	go func() { _ = grpcSrv.Serve(ln) }()

	conn, err := grpc.NewClient(ln.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })

	mux := runtime.NewServeMux(runtime.WithIncomingHeaderMatcher(server.FVTHeaderMatcher))
	require.NoError(t, tenancyv1.RegisterTenancyServiceHandler(context.Background(), mux, conn))

	gwSrv := httptest.NewServer(mux)
	t.Cleanup(gwSrv.Close)

	return &rbacEnv{db: db, gwSrv: gwSrv, tenancySvc: tenancySvc}
}

// call issues a JSON request against the gateway and decodes the body.
func (e *rbacEnv) call(t *testing.T, method, path string, body any) (int, map[string]any) {
	t.Helper()
	var rdr io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		require.NoError(t, err)
		rdr = bytes.NewReader(raw)
	}
	req, err := http.NewRequest(method, e.gwSrv.URL+path, rdr)
	require.NoError(t, err)
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

// seedRBACMember adds a member directly.
func (e *rbacEnv) seedRBACMember(t *testing.T, orgID, userID, role string) {
	t.Helper()
	repo := tenancy.NewMembershipRepository(e.db)
	require.NoError(t, repo.AddMember(context.Background(), &tenancy.OrgMember{
		OrganizationID: orgID, UserID: userID, Role: role, JoinedAt: time.Now().UTC(),
	}))
}

// AC1: AddOrgMember + ListOrgMembers round-trip through the gateway.
func TestFVTRBACMemberCRUD(t *testing.T) {
	env := newRBACEnv(t)
	env.seedRBACMember(t, "org-a", "u-admin", tenancy.RoleOwner)

	code, body := env.call(t, "POST", "/api/v1/admin/tenancy/organizations/org-a/members",
		map[string]any{"userId": "u-2", "role": "member"})
	require.Equal(t, 200, code)
	assert.Equal(t, float64(0), body["response"].(map[string]any)["code"])

	code, body = env.call(t, "GET", "/api/v1/admin/tenancy/organizations/org-a/members", nil)
	require.Equal(t, 200, code)
	members := body["members"].([]any)
	assert.Len(t, members, 2)
}

// AC1: duplicate member -> 10029.
func TestFVTRBACMemberDuplicate(t *testing.T) {
	env := newRBACEnv(t)
	env.seedRBACMember(t, "org-a", "u-admin", tenancy.RoleOwner)
	env.seedRBACMember(t, "org-a", "u-2", tenancy.RoleMember)

	_, body := env.call(t, "POST", "/api/v1/admin/tenancy/organizations/org-a/members",
		map[string]any{"userId": "u-2", "role": "admin"})
	assert.Equal(t, float64(10029), body["code"])
}

// AC3: removing the owner -> 10032.
func TestFVTRBACOwnerProtected(t *testing.T) {
	env := newRBACEnv(t)
	env.seedRBACMember(t, "org-a", "u-admin", tenancy.RoleOwner)

	_, body := env.call(t, "DELETE", "/api/v1/admin/tenancy/organizations/org-a/members/u-admin", nil)
	assert.Equal(t, float64(10032), body["code"])
}

// AC8: a member (not admin) is forbidden from managing the roster.
func TestFVTRBACForbidden(t *testing.T) {
	env := newRBACEnv(t)
	env.seedRBACMember(t, "org-a", "u-member", tenancy.RoleMember)

	_, body := env.call(t, "GET", "/api/v1/admin/tenancy/organizations/org-a/members", nil)
	assert.Equal(t, float64(10036), body["code"])
}

// AC4: CreateInvitation returns a token once; duplicate pending -> 10035.
func TestFVTRBACInvitationCreate(t *testing.T) {
	env := newRBACEnv(t)
	env.seedRBACMember(t, "org-a", "u-admin", tenancy.RoleOwner)

	code, body := env.call(t, "POST", "/api/v1/admin/tenancy/organizations/org-a/invitations",
		map[string]any{"email": "invitee@x.com", "role": "member"})
	require.Equal(t, 200, code)
	assert.Equal(t, float64(0), body["response"].(map[string]any)["code"])
	assert.NotEmpty(t, body["token"])

	// Duplicate pending.
	_, body = env.call(t, "POST", "/api/v1/admin/tenancy/organizations/org-a/invitations",
		map[string]any{"email": "invitee@x.com", "role": "admin"})
	assert.Equal(t, float64(10035), body["code"])
}

// AC4: ListInvitations returns the pipeline.
func TestFVTRBACInvitationList(t *testing.T) {
	env := newRBACEnv(t)
	env.seedRBACMember(t, "org-a", "u-admin", tenancy.RoleOwner)

	_, body := env.call(t, "POST", "/api/v1/admin/tenancy/organizations/org-a/invitations",
		map[string]any{"email": "invitee@x.com", "role": "member"})
	require.Equal(t, float64(0), body["response"].(map[string]any)["code"])

	code, body := env.call(t, "GET", "/api/v1/admin/tenancy/organizations/org-a/invitations", nil)
	require.Equal(t, 200, code)
	invitations := body["invitations"].([]any)
	assert.Len(t, invitations, 1)
}

// AC5: AcceptInvitation creates a membership and marks the invite
// accepted; reuse -> 10033.
func TestFVTRBACInvitationAccept(t *testing.T) {
	env := newRBACEnv(t)
	env.seedRBACMember(t, "org-a", "u-admin", tenancy.RoleOwner)

	// Create the invite as admin.
	_, body := env.call(t, "POST", "/api/v1/admin/tenancy/organizations/org-a/invitations",
		map[string]any{"email": "invitee@x.com", "role": "member"})
	token := body["token"].(string)

	// Switch the session to the invitee for accept.
	env.tenancySvc.SetSessionResolver(&fakeRBACSession{userID: "u-invitee", email: "invitee@x.com"})

	code, body := env.call(t, "POST", "/api/v1/admin/tenancy/invitations/"+token+":accept", nil)
	require.Equal(t, 200, code)
	assert.Equal(t, float64(0), body["response"].(map[string]any)["code"])
	assert.Equal(t, "org-a", body["organizationId"])

	// Reuse -> 10033 (business error, HTTP 500).
	_, body = env.call(t, "POST", "/api/v1/admin/tenancy/invitations/"+token+":accept", nil)
	assert.Equal(t, float64(10033), body["code"])
}

// AC6: RejectInvitation marks the invite rejected.
func TestFVTRBACInvitationReject(t *testing.T) {
	env := newRBACEnv(t)
	env.seedRBACMember(t, "org-a", "u-admin", tenancy.RoleOwner)

	_, body := env.call(t, "POST", "/api/v1/admin/tenancy/organizations/org-a/invitations",
		map[string]any{"email": "invitee@x.com", "role": "member"})
	token := body["token"].(string)

	env.tenancySvc.SetSessionResolver(&fakeRBACSession{userID: "u-invitee", email: "invitee@x.com"})

	code, body := env.call(t, "POST", "/api/v1/admin/tenancy/invitations/"+token+":reject", nil)
	require.Equal(t, 200, code)
	assert.Equal(t, float64(0), body["response"].(map[string]any)["code"])
}

// AC6: RevokeInvitation cancels a pending invite.
func TestFVTRBACInvitationRevoke(t *testing.T) {
	env := newRBACEnv(t)
	env.seedRBACMember(t, "org-a", "u-admin", tenancy.RoleOwner)

	_, body := env.call(t, "POST", "/api/v1/admin/tenancy/organizations/org-a/invitations",
		map[string]any{"email": "invitee@x.com", "role": "member"})
	invID := body["invitation"].(map[string]any)["invitationId"].(string)

	code, body := env.call(t, "POST", "/api/v1/admin/tenancy/invitations/"+invID+":revoke", nil)
	require.Equal(t, 200, code)
	assert.Equal(t, float64(0), body["response"].(map[string]any)["code"])
	assert.Equal(t, "revoked", body["invitation"].(map[string]any)["status"])
}

// AC6: ResendInvitation issues a new token.
func TestFVTRBACInvitationResend(t *testing.T) {
	env := newRBACEnv(t)
	env.seedRBACMember(t, "org-a", "u-admin", tenancy.RoleOwner)

	_, body := env.call(t, "POST", "/api/v1/admin/tenancy/organizations/org-a/invitations",
		map[string]any{"email": "invitee@x.com", "role": "member"})
	invID := body["invitation"].(map[string]any)["invitationId"].(string)

	code, body := env.call(t, "POST", "/api/v1/admin/tenancy/invitations/"+invID+":resend", nil)
	require.Equal(t, 200, code)
	assert.Equal(t, float64(0), body["response"].(map[string]any)["code"])
	assert.NotEmpty(t, body["token"])
}

// AC9: org scoping — a member of org-a cannot manage org-b's roster.
func TestFVTRBACOrgScoping(t *testing.T) {
	env := newRBACEnv(t)
	env.seedRBACMember(t, "org-a", "u-admin", tenancy.RoleOwner)
	// u-admin is not a member of org-b.

	_, body := env.call(t, "GET", "/api/v1/admin/tenancy/organizations/org-b/members", nil)
	assert.Equal(t, float64(10036), body["code"])
}

// helper to keep fmt imported.
var _ = fmt.Sprintf
