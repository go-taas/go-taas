package docs

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	apierrors "github.com/go-taas/go-taas/pkg/errors"
	docsv1 "github.com/go-taas/go-taas/proto/taas/docs/v1"
	"github.com/go-taas/go-taas/services/tenancy"
)

type fakeSessionOrgResolver struct{ org string }

func (f fakeSessionOrgResolver) SessionActiveOrg(context.Context) (string, error) {
	return f.org, nil
}

type fakeSessionUserResolver struct{ user string }

func (f fakeSessionUserResolver) SessionUserID(context.Context) (string, error) {
	return f.user, nil
}

type fakeRoleGuard struct{ allowed bool }

func (f fakeRoleGuard) RequireRole(_ context.Context, _, _, _ string) error {
	if !f.allowed {
		return apierrors.New(apierrors.CodeForbidden)
	}
	return nil
}

func ctxWithOrg(ctx context.Context, org string) context.Context {
	return metadata.NewIncomingContext(ctx, metadata.Pairs("x-organization-id", org))
}

func newService(org string, roleAllowed bool) *Service {
	svc := NewForFVT()
	svc.SetSessionOrgResolver(fakeSessionOrgResolver{org: org})
	svc.SetSessionUserResolver(fakeSessionUserResolver{user: "user-1"})
	svc.SetRoleGuard(fakeRoleGuard{allowed: roleAllowed})
	return svc
}

// TestGetApiDocs verifies GetApiDocs returns the curated catalog (AC1).
func TestGetApiDocs(t *testing.T) {
	svc := newService("org-1", true)
	resp, err := svc.GetApiDocs(ctxWithOrg(context.Background(), "org-1"), &docsv1.GetApiDocsRequest{})
	require.NoError(t, err)
	require.NotEmpty(t, resp.Categories)
	assert.Equal(t, "inference", resp.Categories[0].CategoryId)
	assert.NotEmpty(t, resp.Categories[0].Endpoints)
}

// TestGetApiDocsRoleDenied verifies a session without the required role
// receives 10036 (AC8).
func TestGetApiDocsRoleDenied(t *testing.T) {
	svc := newService("org-1", false)
	_, err := svc.GetApiDocs(ctxWithOrg(context.Background(), "org-1"), &docsv1.GetApiDocsRequest{})
	require.Error(t, err)
	assert.Equal(t, apierrors.CodeForbidden, apierrors.CodeOf(err))
}

// TestServiceWiring exercises the production constructor, ServiceName,
// AttachToServer and gateway registration.
func TestServiceWiring(t *testing.T) {
	svc := New()
	assert.Equal(t, ServiceName, svc.ServiceName())
	grpcSrv := grpc.NewServer()
	svc.AttachToServer(grpcSrv)
	assert.NotNil(t, svc.GetServiceHandlerRegisterFn())
	assert.NoError(t, svc.Migrate(context.Background()))
}

// TestServerInterface verifies the service implements the server.Service
// / server.ServiceWithGateway / Migrator interfaces.
func TestServerInterface(t *testing.T) {
	svc := NewForFVT()
	assert.Equal(t, ServiceName, svc.ServiceName())
	assert.NotNil(t, svc.GetServiceHandlerRegisterFn())
	grpcSrv := grpc.NewServer()
	svc.AttachToServer(grpcSrv)
	assert.NotNil(t, grpcSrv.GetServiceInfo()["taas.docs.v1.DocsService"])
}

// TestSetCatalogVersion verifies the catalog version override.
func TestSetCatalogVersion(t *testing.T) {
	svc := NewForFVT()
	svc.SetCatalogVersion("v2")
	assert.Equal(t, "v2", svc.catalogVersion)
}

// TestGetApiDocsTransitional verifies the transitional (session-less)
// path resolves the org from the X-Organization-Id header.
func TestGetApiDocsTransitional(t *testing.T) {
	svc := NewForFVT()
	resp, err := svc.GetApiDocs(ctxWithOrg(context.Background(), "org-1"), &docsv1.GetApiDocsRequest{})
	require.NoError(t, err)
	require.NotEmpty(t, resp.Categories)
}

// TestGetApiDocsNoOrg verifies a missing org is unauthorized.
func TestGetApiDocsNoOrg(t *testing.T) {
	svc := NewForFVT()
	_, err := svc.GetApiDocs(context.Background(), &docsv1.GetApiDocsRequest{})
	require.Error(t, err)
	assert.Equal(t, apierrors.CodeUnauthorized, apierrors.CodeOf(err))
}

// TestGetApiDocsRealRoleGuardNonMember verifies the docs RPC's role
// check rejects a non-member session with 10036 when the real tenancy
// RoleGuard is wired (AC8). This is a regression test for the
// roleUser="user" defect: the minimum role must be a key of the tenancy
// roleRank map (RoleMember), otherwise a non-member's rank 0 is not
// below the minimum's rank 0 and the check is a no-op.
func TestGetApiDocsRealRoleGuardNonMember(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, tenancy.MigrateSchemaForFVT(db))
	t.Cleanup(func() {
		sqlDB, _ := db.DB()
		_ = sqlDB.Close()
	})

	// Seed an org and a member with the member role.
	require.NoError(t, db.Create(&tenancy.Organization{
		ID: "org-1", DisplayName: "org-1", State: tenancy.StateActive,
	}).Error)
	require.NoError(t, db.Create(&tenancy.OrgMember{
		OrganizationID: "org-1", UserID: "member-uuid", Role: tenancy.RoleMember,
	}).Error)

	svc := NewForFVT()
	svc.SetSessionOrgResolver(fakeSessionOrgResolver{org: "org-1"})
	svc.SetSessionUserResolver(fakeSessionUserResolver{user: "member-uuid"})
	svc.SetRoleGuard(tenancy.NewRoleGuard(db))

	// A member session is allowed.
	resp, err := svc.GetApiDocs(ctxWithOrg(context.Background(), "org-1"), &docsv1.GetApiDocsRequest{})
	require.NoError(t, err)
	require.NotEmpty(t, resp.Categories)

	// A non-member session (no membership row) is rejected with 10036.
	svc.SetSessionUserResolver(fakeSessionUserResolver{user: "non-member-uuid"})
	_, err = svc.GetApiDocs(ctxWithOrg(context.Background(), "org-1"), &docsv1.GetApiDocsRequest{})
	require.Error(t, err)
	assert.Equal(t, apierrors.CodeForbidden, apierrors.CodeOf(err))
}
