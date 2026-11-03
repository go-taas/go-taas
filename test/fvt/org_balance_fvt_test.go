// The money closed loop, end to end through the gateway:
//
//	platform administrator funds an organization
//	  → the tenant administrator sees the balance and budgets a project
//	    → the project administrator budgets a member
//	      → the member sees what is left
//
// Every step runs over the real HTTP gateway with the real services, so the
// numbers the consoles read are the numbers the data plane enforces.

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
	"strings"
	"testing"
	"time"

	"github.com/grpc-ecosystem/grpc-gateway/v2/runtime"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	"github.com/go-taas/go-taas/pkg/authctx"
	"github.com/go-taas/go-taas/pkg/config"
	"github.com/go-taas/go-taas/pkg/grpcmiddleware"
	"github.com/go-taas/go-taas/pkg/server"
	billingv1 "github.com/go-taas/go-taas/proto/taas/billing/v1"
	tenancyv1 "github.com/go-taas/go-taas/proto/taas/tenancy/v1"
	"github.com/go-taas/go-taas/services/billing"
	"github.com/go-taas/go-taas/services/tenancy"
)

// balanceFvtEnv is the billing and tenancy services over one gateway.
type balanceFvtEnv struct {
	db      *gorm.DB
	gwSrv   *httptest.Server
	billing *billing.Service
	tenancy *tenancy.Service
}

// fakeRoles implements authctx.RolesResolver: the FVT gateway has no session
// interceptor, so the platform administrator's role is set on the context the
// service reads.
type fakeRoles struct{ roles []string }

func (f fakeRoles) SessionRoles(context.Context) ([]string, error) { return f.roles, nil }

func newBalanceFvtEnv(t *testing.T) *balanceFvtEnv {
	t.Helper()
	cfg := &config.Configuration{}
	cfg.Billing.Currency = "CNY"
	config.SetConfigForTest(cfg)
	t.Cleanup(func() { config.SetConfigForTest(&config.Configuration{}) })

	dbPath := filepath.Join(t.TempDir(), "balance.db")
	db, err := gorm.Open(sqlite.Open(dbPath), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, billing.MigrateSchemaForFVT(db))
	require.NoError(t, tenancy.MigrateSchemaForFVT(db))
	require.NoError(t, db.Create(&tenancy.Organization{
		ID: "org-fvt", DisplayName: "FVT Tenant", State: tenancy.StateActive,
	}).Error)
	require.NoError(t, db.Create(&tenancy.Project{
		ID: "proj-a", OrganizationID: "org-fvt", DisplayName: "Project A", State: tenancy.StateActive,
	}).Error)
	require.NoError(t, db.Create(&tenancy.OrgMember{
		OrganizationID: "org-fvt", UserID: "11111111-1111-4111-8111-111111111111",
		Role: tenancy.RoleMember, JoinedAt: time.Now().UTC(),
	}).Error)
	require.NoError(t, db.Create(&tenancy.ProjectMember{
		ProjectID: "proj-a", UserID: "11111111-1111-4111-8111-111111111111",
		Role: tenancy.RoleMember, JoinedAt: time.Now().UTC(),
	}).Error)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	// The suite has no auth service, so the caller's roles arrive as metadata
	// and this interceptor parks them on the context exactly as the production
	// session interceptor does.
	grpcSrv := grpc.NewServer(grpc.ChainUnaryInterceptor(
		fvtRolesInterceptor(),
		grpcmiddleware.UnaryServerInterceptor(),
	))
	t.Cleanup(grpcSrv.Stop)

	billingSvc := billing.NewForFVT(db, nil)
	billingSvc.SetOrgGuard(tenancy.NewOrgGuard(db))
	// The tenant-scoped billing reads (balance, ledger) resolve the caller's
	// organization, and the movement records the acting administrator.
	billingSvc.SetSessionOrgResolver(fakeQuotaOrg{org: "org-fvt"})
	billingSvc.SetSessionUserResolver(fakeQuotaSession{user: "33333333-3333-4333-8333-333333333333"})
	tenancySvc := tenancy.NewForFVT(db)
	tenancySvc.SetSpendReader(billingSvc)
	tenancySvc.SetSessionResolver(fakeQuotaSession{user: "33333333-3333-4333-8333-333333333333"})
	tenancySvc.SetSessionOrgResolver(fakeQuotaOrg{org: "org-fvt"})
	// Project management answers to the organization, never to the platform
	// role, so the tenant administrator is an org admin here.
	require.NoError(t, db.Create(&tenancy.OrgMember{
		OrganizationID: "org-fvt", UserID: "33333333-3333-4333-8333-333333333333",
		Role: tenancy.RoleAdmin, JoinedAt: time.Now().UTC(),
	}).Error)
	tenancySvc.SetRoleGuard(tenancy.NewRoleGuard(db))
	tenancySvc.SetSessionRolesResolver(fakeRoles{roles: []string{tenancy.RoleAdmin}})

	billingv1.RegisterBillingServiceServer(grpcSrv, billingSvc)
	tenancyv1.RegisterTenancyServiceServer(grpcSrv, tenancySvc)
	go func() { _ = grpcSrv.Serve(ln) }()

	conn, err := grpc.NewClient(ln.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })
	mux := runtime.NewServeMux(runtime.WithIncomingHeaderMatcher(server.FVTHeaderMatcher))
	require.NoError(t, billingv1.RegisterBillingServiceHandler(context.Background(), mux, conn))
	require.NoError(t, tenancyv1.RegisterTenancyServiceHandler(context.Background(), mux, conn))
	gwSrv := httptest.NewServer(mux)
	t.Cleanup(gwSrv.Close)

	return &balanceFvtEnv{db: db, gwSrv: gwSrv, billing: billingSvc, tenancy: tenancySvc}
}

// call issues one request with the given caller roles on the context.
//
// The gateway cannot carry session roles, so the platform administrator's role
// is attached by wrapping the mux: the delegating handler puts the resolver on
// the request context, which the gRPC interceptor chain forwards to the
// service.
func (e *balanceFvtEnv) call(t *testing.T, roles []string, method, path string, body any) (int, map[string]any) {
	t.Helper()
	ctx := authctx.WithResolver(context.Background(), fakeRoles{roles: roles})
	var reader io.Reader = http.NoBody
	if body != nil {
		raw, err := json.Marshal(body)
		require.NoError(t, err)
		reader = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, e.gwSrv.URL+path, reader)
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	if len(roles) > 0 {
		req.Header.Set("Grpc-Metadata-"+fvtRolesHeader, strings.Join(roles, ","))
	}
	resp, err := e.gwSrv.Client().Do(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	out := map[string]any{}
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

// TestFVTOrganizationBalanceClosedLoop covers the four steps of the money loop.
func TestFVTOrganizationBalanceClosedLoop(t *testing.T) {
	env := newBalanceFvtEnv(t)
	admin := []string{authctx.PlatformAdminRole}

	// 1. The platform administrator lists every tenant, including the one
	// without an account.
	code, body := env.call(t, admin, http.MethodGet, "/api/v1/admin/billing/organization-balances", nil)
	require.Equal(t, http.StatusOK, code, "list: %v", body)
	rows, _ := body["balances"].([]any)
	require.Len(t, rows, 1)
	assert.Equal(t, false, rows[0].(map[string]any)["hasAccount"])

	// An ordinary caller never sees the fleet's money.
	code, body = env.call(t, nil, http.MethodGet, "/api/v1/admin/billing/organization-balances", nil)
	assert.NotEqual(t, http.StatusOK, code, "a plain caller must be refused: %v", body)
	assert.EqualValues(t, 10036, body["code"])

	// 2. Fund it: the account is created and the movement records the
	// administrator's source.
	code, body = env.call(t, admin, http.MethodPost,
		"/api/v1/admin/billing/organizations/org-fvt/recharge",
		map[string]any{"amountCents": 100000, "idempotencyKey": "fvt-topup-1", "note": "首笔充值", "source": "admin_manual"})
	require.Equal(t, http.StatusOK, code, "recharge: %v", body)
	assert.Equal(t, true, body["accountCreated"])
	balance := body["balance"].(map[string]any)
	assert.EqualValues(t, 100000, numberOf(t, balance["balanceCents"]))
	tx := body["transaction"].(map[string]any)
	assert.Equal(t, "admin_manual", tx["source"])
	assert.Equal(t, "33333333-3333-4333-8333-333333333333", tx["actorUserId"])
	assert.Equal(t, "首笔充值", tx["reference"])

	// 3. The tenant administrator sees the balance.
	code, body = env.call(t, nil, http.MethodGet, "/api/v1/billing/balance", nil)
	require.Equal(t, http.StatusOK, code, "balance: %v", body)
	assert.EqualValues(t, 100000, numberOf(t, body["balanceCents"]))
	assert.Equal(t, true, body["hasAccount"])

	// 4. …and budgets a project.
	code, body = env.call(t, nil, http.MethodPatch,
		"/api/v1/admin/tenancy/projects/proj-a/quota",
		map[string]any{"monthlySpendLimitCents": 30000})
	require.Equal(t, http.StatusOK, code, "project budget: %v", body)
	assert.EqualValues(t, 30000, numberOf(t, body["quota"].(map[string]any)["monthlySpendLimitCents"]))

	// 5. The project administrator budgets the member (the same person here).
	code, body = env.call(t, nil, http.MethodPatch,
		"/api/v1/admin/tenancy/projects/proj-a/member-quotas/11111111-1111-4111-8111-111111111111",
		map[string]any{"monthlySpendLimitCents": 12000})
	require.Equal(t, http.StatusOK, code, "member budget: %v", body)
	assert.EqualValues(t, 12000, numberOf(t, body["quota"].(map[string]any)["monthlySpendLimitCents"]))

	// 6. The member sees their remaining budget. Their own organization-wide
	// budget is a second tier the tenant administrator sets.
	code, body = env.call(t, nil, http.MethodPatch,
		"/api/v1/admin/tenancy/user-quotas/11111111-1111-4111-8111-111111111111",
		map[string]any{"monthlySpendLimitCents": 5000})
	require.Equal(t, http.StatusOK, code, "personal budget: %v", body)

	// The last read is the member's own surface, so the session switches to
	// them: every other step ran as the tenant administrator.
	env.tenancy.SetSessionResolver(fakeQuotaSession{user: "11111111-1111-4111-8111-111111111111"})
	code, body = env.call(t, nil, http.MethodGet, "/api/v1/quotas", nil)
	require.Equal(t, http.StatusOK, code, "my quota: %v", body)
	assert.EqualValues(t, 5000, numberOf(t, body["personalRemainingCents"]))
	assert.Equal(t, "CNY", body["currency"])
	projects, _ := body["projectMemberQuotas"].([]any)
	require.Len(t, projects, 1)
	assert.EqualValues(t, 12000, numberOf(t, projects[0].(map[string]any)["remainingCents"]))
}

// TestFVTOrganizationDebitAndLedger covers the correction direction and the
// ledger an operator reads afterwards: the debit comes off the balance, names
// the administrator, and appears in the account's movements.
func TestFVTOrganizationDebitAndLedger(t *testing.T) {
	env := newBalanceFvtEnv(t)
	admin := []string{authctx.PlatformAdminRole}

	code, body := env.call(t, admin, http.MethodPost,
		"/api/v1/admin/billing/organizations/org-fvt/recharge",
		map[string]any{"amountCents": 50000, "idempotencyKey": "seed-1", "source": "admin_manual"})
	require.Equal(t, http.StatusOK, code, "recharge: %v", body)
	accountID, _ := body["balance"].(map[string]any)["accountId"].(string)
	require.NotEmpty(t, accountID)

	// A debit without a reason is refused: a correction must say why.
	code, body = env.call(t, admin, http.MethodPost,
		"/api/v1/admin/billing/organizations/org-fvt/debit",
		map[string]any{"amountCents": 1000, "idempotencyKey": "debit-0"})
	assert.NotEqual(t, http.StatusOK, code)
	assert.EqualValues(t, 10510, body["code"], "missing reason: %v", body)

	code, body = env.call(t, admin, http.MethodPost,
		"/api/v1/admin/billing/organizations/org-fvt/debit",
		map[string]any{"amountCents": 1000, "idempotencyKey": "debit-1", "note": "冲正", "source": "admin_correction"})
	require.Equal(t, http.StatusOK, code, "debit: %v", body)
	assert.EqualValues(t, 49000, numberOf(t, body["balance"].(map[string]any)["balanceCents"]))
	assert.Equal(t, "refund", body["transaction"].(map[string]any)["type"])
	assert.Equal(t, "admin_correction", body["transaction"].(map[string]any)["source"])

	// The ledger lists both movements with their source.
	code, body = env.call(t, admin, http.MethodGet,
		fmt.Sprintf("/api/v1/admin/billing/transactions?account_id=%s&page.limit=10", accountID), nil)
	require.Equal(t, http.StatusOK, code, "ledger: %v", body)
	txs, _ := body["transactions"].([]any)
	require.Len(t, txs, 2)
	sources := map[string]bool{}
	for _, raw := range txs {
		row := raw.(map[string]any)
		sources[row["source"].(string)] = true
		assert.Equal(t, "33333333-3333-4333-8333-333333333333", row["actorUserId"])
	}
	assert.True(t, sources["admin_manual"])
	assert.True(t, sources["admin_correction"])
}

// fakeQuotaSession is a SessionResolver returning a fixed caller; the FVT
// environment has no auth service, so the tenant administrator is injected.
type fakeQuotaSession struct{ user string }

func (f fakeQuotaSession) SessionUserID(context.Context) (string, error) { return f.user, nil }
func (f fakeQuotaSession) SessionUserEmail(context.Context) (string, error) {
	return f.user + "@example.com", nil
}

// fakeQuotaOrg is a SessionOrgResolver returning a fixed organization.
type fakeQuotaOrg struct{ org string }

func (f fakeQuotaOrg) SessionActiveOrg(context.Context) (string, error) { return f.org, nil }

// fvtRolesHeader carries the caller's roles over the FVT gateway.
const fvtRolesHeader = "X-Fvt-Roles"

// fvtRolesInterceptor turns the FVT roles header into a context resolver, which
// is the seam the services read for a platform administrator check.
func fvtRolesInterceptor() grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, _ *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		if md, ok := metadata.FromIncomingContext(ctx); ok {
			if values := md.Get(strings.ToLower(fvtRolesHeader)); len(values) > 0 && values[0] != "" {
				ctx = authctx.WithResolver(ctx, fakeRoles{roles: strings.Split(values[0], ",")})
			}
		}
		return handler(ctx, req)
	}
}

// TestFVTUnfundedOrganizationBalance covers the state every tenant starts in:
// the organization exists but was never funded. The tenant surfaces must report
// the emptiness rather than fail, because the console has to draw the unfunded
// card before the first top-up.
func TestFVTUnfundedOrganizationBalance(t *testing.T) {
	env := newBalanceFvtEnv(t)

	code, body := env.call(t, nil, http.MethodGet, "/api/v1/billing/balance", nil)
	require.Equal(t, http.StatusOK, code, "unfunded balance: %v", body)
	assert.Equal(t, false, body["hasAccount"])
	assert.EqualValues(t, 0, numberOf(t, body["balanceCents"]))
	assert.Equal(t, "CNY", body["currency"])

	// A project budget can still be configured before the money arrives; the
	// spend side simply reports zero.
	code, body = env.call(t, nil, http.MethodPatch,
		"/api/v1/admin/tenancy/projects/proj-a/quota",
		map[string]any{"monthlySpendLimitCents": 20000})
	require.Equal(t, http.StatusOK, code, "project budget: %v", body)
	quota := body["quota"].(map[string]any)
	assert.EqualValues(t, 0, numberOf(t, quota["spentThisCycleCents"]))
	assert.EqualValues(t, 20000, numberOf(t, quota["remainingCents"]))

	// The operator's page lists the organization as unfunded instead of
	// erroring on the missing account.
	code, body = env.call(t, []string{authctx.PlatformAdminRole}, http.MethodGet,
		"/api/v1/admin/billing/organization-balances", nil)
	require.Equal(t, http.StatusOK, code, "list: %v", body)
	rows, _ := body["balances"].([]any)
	require.Len(t, rows, 1)
	assert.Equal(t, false, rows[0].(map[string]any)["hasAccount"])
}
