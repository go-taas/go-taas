package tenancy

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	commonv1 "github.com/go-taas/go-taas/proto/taas/common/v1"
	tenancyv1 "github.com/go-taas/go-taas/proto/taas/tenancy/v1"

	"github.com/go-taas/go-taas/pkg/config"
	apierrors "github.com/go-taas/go-taas/pkg/errors"
	"github.com/go-taas/go-taas/pkg/server"
)

// newTestService wires a Service against an in-memory SQLite database.
func newTestService(t *testing.T) *Service {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file::memory:?cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, MigrateSchemaForFVT(db))
	// The organization summary counts reference the api_keys and
	// inference_services tables; create minimal stubs so the count
	// queries resolve to zero rows.
	require.NoError(t, db.Exec(`CREATE TABLE api_keys (organization_id TEXT, revoked INTEGER)`).Error)
	require.NoError(t, db.Exec(`CREATE TABLE inference_services (organization_id TEXT, state TEXT)`).Error)
	t.Cleanup(func() {
		sqlDB, _ := db.DB()
		_ = sqlDB.Close()
	})
	return NewForFVT(db)
}

func TestServiceCreateOrganization(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()

	// Valid create.
	resp, err := svc.CreateOrganization(ctx, &tenancyv1.CreateOrganizationRequest{
		OrganizationId: "org-a",
		DisplayName:    "Org A",
		Description:    "desc",
	})
	require.NoError(t, err)
	assert.Equal(t, "org-a", resp.GetOrganization().GetOrganizationId())
	assert.Equal(t, "active", resp.GetOrganization().GetState())

	// Duplicate → 10015.
	_, err = svc.CreateOrganization(ctx, &tenancyv1.CreateOrganizationRequest{
		OrganizationId: "org-a",
		DisplayName:    "Dup",
	})
	assert.EqualValues(t, apierrors.CodeOrganizationExists, apierrors.CodeOf(err))

	// Invalid id → 10019.
	_, err = svc.CreateOrganization(ctx, &tenancyv1.CreateOrganizationRequest{
		OrganizationId: "BAD",
		DisplayName:    "Bad",
	})
	assert.EqualValues(t, apierrors.CodeTenancyInvalid, apierrors.CodeOf(err))

	// Empty name → 10019.
	_, err = svc.CreateOrganization(ctx, &tenancyv1.CreateOrganizationRequest{
		OrganizationId: "org-b",
		DisplayName:    "",
	})
	assert.EqualValues(t, apierrors.CodeTenancyInvalid, apierrors.CodeOf(err))
}

func TestServiceListAndGetOrganization(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()

	require.NoError(t, svc.repo.CreateOrganization(ctx, &Organization{ID: "org-a", DisplayName: "A", State: StateActive}))
	require.NoError(t, svc.repo.CreateOrganization(ctx, &Organization{ID: "org-b", DisplayName: "B", State: StateDisabled}))

	// List all.
	resp, err := svc.ListOrganizations(ctx, &tenancyv1.ListOrganizationsRequest{
		Page: &commonv1.PageRequest{Offset: 0, Limit: 10},
	})
	require.NoError(t, err)
	assert.Len(t, resp.GetOrganizations(), 2)
	assert.EqualValues(t, 2, resp.GetPageMeta().GetTotal())

	// State filter.
	resp, err = svc.ListOrganizations(ctx, &tenancyv1.ListOrganizationsRequest{State: StateDisabled})
	require.NoError(t, err)
	assert.Len(t, resp.GetOrganizations(), 1)
	assert.Equal(t, "org-b", resp.GetOrganizations()[0].GetOrganizationId())

	// Invalid state → 10019.
	_, err = svc.ListOrganizations(ctx, &tenancyv1.ListOrganizationsRequest{State: "bogus"})
	assert.EqualValues(t, apierrors.CodeTenancyInvalid, apierrors.CodeOf(err))

	// Get.
	got, err := svc.GetOrganization(ctx, &tenancyv1.GetOrganizationRequest{OrganizationId: "org-a"})
	require.NoError(t, err)
	assert.Equal(t, "A", got.GetOrganization().GetDisplayName())

	// Get unknown → 10005.
	_, err = svc.GetOrganization(ctx, &tenancyv1.GetOrganizationRequest{OrganizationId: "org-missing"})
	assert.EqualValues(t, apierrors.CodeOrganizationNotFound, apierrors.CodeOf(err))
}

func TestServiceUpdateAndStateOrganization(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()

	require.NoError(t, svc.repo.CreateOrganization(ctx, &Organization{ID: "org-a", DisplayName: "A", State: StateActive}))

	// Update.
	upd, err := svc.UpdateOrganization(ctx, &tenancyv1.UpdateOrganizationRequest{
		OrganizationId: "org-a",
		DisplayName:    "A2",
		Description:    "d",
	})
	require.NoError(t, err)
	assert.Equal(t, "A2", upd.GetOrganization().GetDisplayName())

	// Update unknown → 10005.
	_, err = svc.UpdateOrganization(ctx, &tenancyv1.UpdateOrganizationRequest{OrganizationId: "org-x", DisplayName: "X"})
	assert.EqualValues(t, apierrors.CodeOrganizationNotFound, apierrors.CodeOf(err))

	// Disable.
	dis, err := svc.DisableOrganization(ctx, &tenancyv1.DisableOrganizationRequest{OrganizationId: "org-a"})
	require.NoError(t, err)
	assert.Equal(t, StateDisabled, dis.GetOrganization().GetState())

	// Enable.
	en, err := svc.EnableOrganization(ctx, &tenancyv1.EnableOrganizationRequest{OrganizationId: "org-a"})
	require.NoError(t, err)
	assert.Equal(t, StateActive, en.GetOrganization().GetState())

	// Disable unknown → 10005.
	_, err = svc.DisableOrganization(ctx, &tenancyv1.DisableOrganizationRequest{OrganizationId: "org-x"})
	assert.EqualValues(t, apierrors.CodeOrganizationNotFound, apierrors.CodeOf(err))

	// Enable unknown → 10005.
	_, err = svc.EnableOrganization(ctx, &tenancyv1.EnableOrganizationRequest{OrganizationId: "org-x"})
	assert.EqualValues(t, apierrors.CodeOrganizationNotFound, apierrors.CodeOf(err))

	// Update with empty name → 10019.
	_, err = svc.UpdateOrganization(ctx, &tenancyv1.UpdateOrganizationRequest{OrganizationId: "org-a", DisplayName: ""})
	assert.EqualValues(t, apierrors.CodeTenancyInvalid, apierrors.CodeOf(err))
}

func TestServiceProjectLifecycle(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()

	require.NoError(t, svc.repo.CreateOrganization(ctx, &Organization{ID: "org-a", DisplayName: "A", State: StateActive}))

	// Create.
	resp, err := svc.CreateProject(ctx, &tenancyv1.CreateProjectRequest{
		OrganizationId: "org-a",
		ProjectId:      "proj-1",
		DisplayName:    "P1",
	})
	require.NoError(t, err)
	assert.Equal(t, "proj-1", resp.GetProject().GetProjectId())
	assert.Equal(t, "org-a", resp.GetProject().GetOrganizationId())

	// Duplicate → 10016.
	_, err = svc.CreateProject(ctx, &tenancyv1.CreateProjectRequest{
		OrganizationId: "org-a",
		ProjectId:      "proj-1",
		DisplayName:    "Dup",
	})
	assert.EqualValues(t, apierrors.CodeProjectExists, apierrors.CodeOf(err))

	// Unknown org → 10005.
	_, err = svc.CreateProject(ctx, &tenancyv1.CreateProjectRequest{
		OrganizationId: "org-missing",
		ProjectId:      "proj-2",
		DisplayName:    "P2",
	})
	assert.EqualValues(t, apierrors.CodeOrganizationNotFound, apierrors.CodeOf(err))

	// Disabled org → 10017.
	require.NoError(t, svc.repo.CreateOrganization(ctx, &Organization{ID: "org-off", DisplayName: "Off", State: StateDisabled}))
	_, err = svc.CreateProject(ctx, &tenancyv1.CreateProjectRequest{
		OrganizationId: "org-off",
		ProjectId:      "proj-3",
		DisplayName:    "P3",
	})
	assert.EqualValues(t, apierrors.CodeOrganizationDisabled, apierrors.CodeOf(err))

	// Invalid id → 10019.
	_, err = svc.CreateProject(ctx, &tenancyv1.CreateProjectRequest{
		OrganizationId: "org-a",
		ProjectId:      "BAD",
		DisplayName:    "P",
	})
	assert.EqualValues(t, apierrors.CodeTenancyInvalid, apierrors.CodeOf(err))

	// List with org filter.
	list, err := svc.ListProjects(ctx, &tenancyv1.ListProjectsRequest{OrganizationId: "org-a"})
	require.NoError(t, err)
	assert.Len(t, list.GetProjects(), 1)

	// Get.
	got, err := svc.GetProject(ctx, &tenancyv1.GetProjectRequest{ProjectId: "proj-1"})
	require.NoError(t, err)
	assert.Equal(t, "P1", got.GetProject().GetDisplayName())

	// Get unknown → 10006.
	_, err = svc.GetProject(ctx, &tenancyv1.GetProjectRequest{ProjectId: "proj-x"})
	assert.EqualValues(t, apierrors.CodeProjectNotFound, apierrors.CodeOf(err))

	// Update.
	upd, err := svc.UpdateProject(ctx, &tenancyv1.UpdateProjectRequest{ProjectId: "proj-1", DisplayName: "P1b"})
	require.NoError(t, err)
	assert.Equal(t, "P1b", upd.GetProject().GetDisplayName())

	// Disable.
	dis, err := svc.DisableProject(ctx, &tenancyv1.DisableProjectRequest{ProjectId: "proj-1"})
	require.NoError(t, err)
	assert.Equal(t, StateDisabled, dis.GetProject().GetState())

	// Enable under an active org succeeds.
	en, err := svc.EnableProject(ctx, &tenancyv1.EnableProjectRequest{ProjectId: "proj-1"})
	require.NoError(t, err)
	assert.Equal(t, StateActive, en.GetProject().GetState())

	// Enable under a disabled org → 10017.
	require.NoError(t, svc.repo.CreateProject(ctx, &Project{ID: "proj-off", OrganizationID: "org-off", DisplayName: "Off", State: StateDisabled}))
	_, err = svc.EnableProject(ctx, &tenancyv1.EnableProjectRequest{ProjectId: "proj-off"})
	assert.EqualValues(t, apierrors.CodeOrganizationDisabled, apierrors.CodeOf(err))

	// Disable unknown → 10006.
	_, err = svc.DisableProject(ctx, &tenancyv1.DisableProjectRequest{ProjectId: "proj-x"})
	assert.EqualValues(t, apierrors.CodeProjectNotFound, apierrors.CodeOf(err))

	// Enable unknown → 10006.
	_, err = svc.EnableProject(ctx, &tenancyv1.EnableProjectRequest{ProjectId: "proj-x"})
	assert.EqualValues(t, apierrors.CodeProjectNotFound, apierrors.CodeOf(err))

	// Update unknown → 10006.
	_, err = svc.UpdateProject(ctx, &tenancyv1.UpdateProjectRequest{ProjectId: "proj-x", DisplayName: "X"})
	assert.EqualValues(t, apierrors.CodeProjectNotFound, apierrors.CodeOf(err))

	// Update with empty name → 10019.
	_, err = svc.UpdateProject(ctx, &tenancyv1.UpdateProjectRequest{ProjectId: "proj-1", DisplayName: ""})
	assert.EqualValues(t, apierrors.CodeTenancyInvalid, apierrors.CodeOf(err))

	// List with invalid state → 10019.
	_, err = svc.ListProjects(ctx, &tenancyv1.ListProjectsRequest{State: "bogus"})
	assert.EqualValues(t, apierrors.CodeTenancyInvalid, apierrors.CodeOf(err))
}

func TestServiceNormalizePagination(t *testing.T) {
	// Defaults.
	off, lim := normalizePagination(nil)
	assert.Equal(t, 0, off)
	assert.Equal(t, listDefaultLimit, lim)

	// Clamp to max.
	off, lim = normalizePagination(&commonv1.PageRequest{Offset: 5, Limit: 1000})
	assert.Equal(t, 5, off)
	assert.Equal(t, listMaxLimit, lim)

	// Non-positive limit defaults.
	off, lim = normalizePagination(&commonv1.PageRequest{Offset: 0, Limit: 0})
	assert.Equal(t, 0, off)
	assert.Equal(t, listDefaultLimit, lim)
}

// fakeComponents is a minimal server.Components that exposes a GORM
// database handle for the lazy-wiring path.
type fakeComponents struct {
	db *gorm.DB
}

func (f *fakeComponents) DB() server.DBComponent { return &fakeDBComponent{db: f.db} }
func (f *fakeComponents) Redis() server.RedisComponent { return nil }
func (f *fakeComponents) MQ() server.MQComponent        { return nil }

type fakeDBComponent struct{ db *gorm.DB }

func (f *fakeDBComponent) GormDB() any { return f.db }

func TestServiceAssemblyAndLazyWiring(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file::memory:?cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, MigrateSchemaForFVT(db))
	t.Cleanup(func() {
		sqlDB, _ := db.DB()
		_ = sqlDB.Close()
	})

	// New + assembly methods.
	svc := New(&fakeComponents{db: db})
	assert.Equal(t, ServiceName, svc.ServiceName())
	assert.NotNil(t, svc.GetServiceHandlerRegisterFn())
	grpcSrv := grpc.NewServer()
	svc.AttachToServer(grpcSrv)

	// Lazy repository wiring from components.
	repo, err := svc.repository()
	require.NoError(t, err)
	assert.NotNil(t, repo)
	assert.Same(t, repo, svc.repo)

	// gormDB resolves from the wired repo.
	gdb, err := svc.gormDB()
	require.NoError(t, err)
	assert.NotNil(t, gdb)

	// A service with no components and no repo fails gormDB.
	empty := &Service{}
	_, err = empty.gormDB()
	require.Error(t, err)
	assert.EqualValues(t, apierrors.CodeInternal, apierrors.CodeOf(err))
}

func TestServiceMigrate(t *testing.T) {
	// Fresh database: Migrate creates the tables and seeds the default
	// organization from config.
	db, err := gorm.Open(sqlite.Open("file::memory:?cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	t.Cleanup(func() {
		sqlDB, _ := db.DB()
		_ = sqlDB.Close()
	})

	cfg := &config.Configuration{}
	cfg.Tenancy.DefaultOrgID = "org-default"
	cfg.Tenancy.DefaultOrgDisplayName = "Default Organization"
	config.SetConfigForTest(cfg)
	t.Cleanup(func() { config.SetConfigForTest(&config.Configuration{}) })

	svc := NewForFVT(db)
	require.NoError(t, svc.Migrate(context.Background()))
	assert.True(t, db.Migrator().HasTable(&Organization{}))
	assert.True(t, db.Migrator().HasTable(&Project{}))

	// The default org was seeded.
	org, err := svc.repo.FindOrganization(context.Background(), "org-default")
	require.NoError(t, err)
	assert.Equal(t, "Default Organization", org.DisplayName)

	// Re-running Migrate on a non-empty table does not re-seed.
	require.NoError(t, svc.Migrate(context.Background()))
	count, err := svc.repo.CountOrganizations(context.Background())
	require.NoError(t, err)
	assert.EqualValues(t, 1, count)
}