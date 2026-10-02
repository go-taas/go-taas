package account

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
	"gorm.io/gorm"

	apierrors "github.com/go-taas/go-taas/pkg/errors"
	"github.com/go-taas/go-taas/pkg/server"
	accountv1 "github.com/go-taas/go-taas/proto/taas/account/v1"
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

func newService(t *testing.T, db *gorm.DB, roleAllowed bool) *Service {
	t.Helper()
	svc := NewForFVT(db)
	svc.SetSessionOrgResolver(fakeSessionOrgResolver{org: "org-1"})
	svc.SetSessionUserResolver(fakeSessionUserResolver{user: "user-1"})
	svc.SetRoleGuard(fakeRoleGuard{allowed: roleAllowed})
	return svc
}

func TestCreateAndListExports(t *testing.T) {
	db := newTestDB(t)
	svc := newService(t, db, true)

	// AC1: create an export returns status=pending.
	resp, err := svc.CreateDataExport(ctxWithOrg(context.Background(), "org-1"), &accountv1.CreateDataExportRequest{
		Type: ExportTypeUsage, Format: ExportFormatJSON,
	})
	require.NoError(t, err)
	assert.Equal(t, ExportStatusPending, resp.Export.Status)
	assert.NotEmpty(t, resp.Export.ExportId)

	// AC1: invalid type -> 12502.
	_, err = svc.CreateDataExport(ctxWithOrg(context.Background(), "org-1"), &accountv1.CreateDataExportRequest{
		Type: "bogus", Format: ExportFormatJSON,
	})
	require.Error(t, err)
	assert.Equal(t, apierrors.CodeDataExportTypeInvalid, apierrors.CodeOf(err))

	// AC1: invalid format -> 12503.
	_, err = svc.CreateDataExport(ctxWithOrg(context.Background(), "org-1"), &accountv1.CreateDataExportRequest{
		Type: ExportTypeUsage, Format: "xml",
	})
	require.Error(t, err)
	assert.Equal(t, apierrors.CodeDataExportFormatInvalid, apierrors.CodeOf(err))

	// AC1: invalid range -> 10404.
	_, err = svc.CreateDataExport(ctxWithOrg(context.Background(), "org-1"), &accountv1.CreateDataExportRequest{
		Type: ExportTypeUsage, Format: ExportFormatJSON, Since: 200, Until: 100,
	})
	require.Error(t, err)
	assert.Equal(t, apierrors.CodeMeteringRangeInvalid, apierrors.CodeOf(err))

	// AC2: list returns the export.
	list, err := svc.ListDataExports(ctxWithOrg(context.Background(), "org-1"), &accountv1.ListDataExportsRequest{})
	require.NoError(t, err)
	require.Len(t, list.Exports, 1)
	assert.Equal(t, ExportTypeUsage, list.Exports[0].Type)
}

func TestGetAndDownloadExport(t *testing.T) {
	db := newTestDB(t)
	svc := newService(t, db, true)

	// Create an export.
	resp, err := svc.CreateDataExport(ctxWithOrg(context.Background(), "org-1"), &accountv1.CreateDataExportRequest{
		Type: ExportTypeUsage, Format: ExportFormatJSON,
	})
	require.NoError(t, err)

	// AC2: get the export.
	got, err := svc.GetDataExport(ctxWithOrg(context.Background(), "org-1"), &accountv1.GetDataExportRequest{ExportId: resp.Export.ExportId})
	require.NoError(t, err)
	assert.Equal(t, ExportStatusPending, got.Export.Status)

	// AC3: download before ready -> 12504.
	_, err = svc.DownloadDataExport(ctxWithOrg(context.Background(), "org-1"), &accountv1.DownloadDataExportRequest{ExportId: resp.Export.ExportId})
	require.Error(t, err)
	assert.Equal(t, apierrors.CodeDataExportNotReady, apierrors.CodeOf(err))

	// Mark ready and download.
	repo, err := svc.repository()
	require.NoError(t, err)
	require.NoError(t, repo.UpdateExportStatus(context.Background(), resp.Export.ExportId, ExportStatusReady, 1, `{"usage":[]}`, ""))
	dl, err := svc.DownloadDataExport(ctxWithOrg(context.Background(), "org-1"), &accountv1.DownloadDataExportRequest{ExportId: resp.Export.ExportId})
	require.NoError(t, err)
	assert.Equal(t, `{"usage":[]}`, dl.File)
	assert.Equal(t, "application/json", dl.ContentType)

	// AC2: unknown export -> 12501.
	_, err = svc.GetDataExport(ctxWithOrg(context.Background(), "org-1"), &accountv1.GetDataExportRequest{ExportId: "missing"})
	require.Error(t, err)
	assert.Equal(t, apierrors.CodeDataExportNotFound, apierrors.CodeOf(err))
}

func TestTenantScoped(t *testing.T) {
	db := newTestDB(t)
	svc := newService(t, db, true)

	// Create an export for org-1.
	resp, err := svc.CreateDataExport(ctxWithOrg(context.Background(), "org-1"), &accountv1.CreateDataExportRequest{
		Type: ExportTypeUsage, Format: ExportFormatJSON,
	})
	require.NoError(t, err)

	// AC4: a different org cannot read it.
	svc.SetSessionOrgResolver(fakeSessionOrgResolver{org: "org-2"})
	_, err = svc.GetDataExport(ctxWithOrg(context.Background(), "org-2"), &accountv1.GetDataExportRequest{ExportId: resp.Export.ExportId})
	require.Error(t, err)
	assert.Equal(t, apierrors.CodeDataExportNotFound, apierrors.CodeOf(err))
}

func TestRoleDenied(t *testing.T) {
	db := newTestDB(t)
	svc := newService(t, db, false)
	// AC8: role denied -> 10036.
	_, err := svc.ListDataExports(ctxWithOrg(context.Background(), "org-1"), &accountv1.ListDataExportsRequest{})
	require.Error(t, err)
	assert.Equal(t, apierrors.CodeForbidden, apierrors.CodeOf(err))
}

// TestListDataExportsRealRoleGuardNonMember verifies the export RPC's
// role check rejects a non-member session with 10036 when the real
// tenancy RoleGuard is wired (AC8). This is a regression test for the
// roleUser="user" defect: the minimum role must be a key of the tenancy
// roleRank map (RoleMember), otherwise a non-member's rank 0 is not
// below the minimum's rank 0 and the check is a no-op.
func TestListDataExportsRealRoleGuardNonMember(t *testing.T) {
	db := newTestDB(t)
	require.NoError(t, tenancy.MigrateSchemaForFVT(db))

	// Seed an org and a member with the member role.
	require.NoError(t, db.Create(&tenancy.Organization{
		ID: "org-1", DisplayName: "org-1", State: tenancy.StateActive,
	}).Error)
	require.NoError(t, db.Create(&tenancy.OrgMember{
		OrganizationID: "org-1", UserID: "member-uuid", Role: tenancy.RoleMember,
	}).Error)

	svc := NewForFVT(db)
	svc.SetSessionOrgResolver(fakeSessionOrgResolver{org: "org-1"})
	svc.SetSessionUserResolver(fakeSessionUserResolver{user: "member-uuid"})
	svc.SetRoleGuard(tenancy.NewRoleGuard(db))

	// A member session is allowed.
	resp, err := svc.ListDataExports(ctxWithOrg(context.Background(), "org-1"), &accountv1.ListDataExportsRequest{})
	require.NoError(t, err)
	require.NotNil(t, resp)

	// A non-member session (no membership row) is rejected with 10036.
	svc.SetSessionUserResolver(fakeSessionUserResolver{user: "non-member-uuid"})
	_, err = svc.ListDataExports(ctxWithOrg(context.Background(), "org-1"), &accountv1.ListDataExportsRequest{})
	require.Error(t, err)
	assert.Equal(t, apierrors.CodeForbidden, apierrors.CodeOf(err))
}

func TestServiceWiring(t *testing.T) {
	db := newTestDB(t)
	svc := NewForFVT(db)
	assert.Equal(t, ServiceName, svc.ServiceName())
	grpcSrv := grpc.NewServer()
	svc.AttachToServer(grpcSrv)
	assert.NotNil(t, svc.GetServiceHandlerRegisterFn())
	assert.NoError(t, svc.Migrate(context.Background()))
	assert.NotNil(t, grpcSrv.GetServiceInfo()["taas.account.v1.AccountService"])
}

func TestMigrateSchemaForFVT(t *testing.T) {
	db := newTestDB(t)
	require.NoError(t, MigrateSchemaForFVT(db))
	assert.True(t, db.Migrator().HasTable(&DataExport{}))
}

func TestTransitionalPath(t *testing.T) {
	db := newTestDB(t)
	svc := NewForFVT(db)
	resp, err := svc.ListDataExports(ctxWithOrg(context.Background(), "org-1"), &accountv1.ListDataExportsRequest{})
	require.NoError(t, err)
	assert.Empty(t, resp.Exports)
}

func TestNoOrg(t *testing.T) {
	db := newTestDB(t)
	svc := NewForFVT(db)
	_, err := svc.ListDataExports(context.Background(), &accountv1.ListDataExportsRequest{})
	require.Error(t, err)
	assert.Equal(t, apierrors.CodeUnauthorized, apierrors.CodeOf(err))
}

// fakeComponents provides a DB component for the production wiring path.
type fakeComponents struct {
	db *gorm.DB
}

func (f *fakeComponents) DB() server.DBComponent       { return &fakeDBComponent{db: f.db} }
func (f *fakeComponents) Redis() server.RedisComponent { return nil }
func (f *fakeComponents) MQ() server.MQComponent       { return nil }

type fakeDBComponent struct{ db *gorm.DB }

func (f *fakeDBComponent) GormDB() any { return f.db }

// TestProductionConstructor exercises the production New constructor and
// Migrate through the shared components.
func TestProductionConstructor(t *testing.T) {
	db := newTestDB(t)
	svc := New(&fakeComponents{db: db})
	assert.Equal(t, ServiceName, svc.ServiceName())
	require.NoError(t, svc.Migrate(context.Background()))
	svc.SetMaxRangeSeconds(3600)
	assert.Equal(t, int64(3600), svc.maxRangeSeconds)

	// Exercise the production repository path (via components).
	svc.repo = nil
	_, err := svc.repository()
	require.NoError(t, err)
}
