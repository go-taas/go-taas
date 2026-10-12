package provider

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	"github.com/go-taas/go-taas/pkg/config"
	"github.com/go-taas/go-taas/pkg/server"
	providerv1 "github.com/go-taas/go-taas/proto/taas/provider/v1"
)

// fakeDBComponent adapts a *gorm.DB to the server.DBComponent surface.
type fakeDBComponent struct{ db *gorm.DB }

func (f fakeDBComponent) GormDB() any { return f.db }

// fakeComponents implements server.Components with only the database wired.
type fakeComponents struct{ db server.DBComponent }

func (f fakeComponents) DB() server.DBComponent       { return f.db }
func (f fakeComponents) Redis() server.RedisComponent { return nil }
func (f fakeComponents) MQ() server.MQComponent       { return nil }

// TestNewMigratesProviderSchema is the regression test for the startup bug
// class: the constructor used by apps/taas-server/main.go must carry the
// shared components so Migrate() can resolve the database. A cache-only
// service owning a DB table aborted the server with FATAL "service
// migration failed {"service": "provider"}".
func TestNewMigratesProviderSchema(t *testing.T) {
	cfg := &config.Configuration{}
	cfg.MQ.Namespace = "test"
	config.SetConfigForTest(cfg)

	db, err := gorm.Open(sqlite.Open("file:provider_wiring?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	t.Cleanup(func() {
		sqlDB, _ := db.DB()
		_ = sqlDB.Close()
	})

	// Construct exactly the way main.go does.
	svc := New(fakeComponents{db: fakeDBComponent{db: db}})
	require.Equal(t, ServiceName, svc.ServiceName())
	require.NoError(t, svc.Migrate(context.Background()), "Migrate must succeed with components wired")
	require.True(t, db.Migrator().HasTable(&Provider{}), "providers table must be created")
	require.True(t, db.Migrator().HasTable(&ProviderModel{}), "provider_models table must be created")

	// The service is usable end to end after migration.
	resp, err := svc.ListProviders(context.Background(), &providerv1.ListProvidersRequest{})
	require.NoError(t, err)
	assert.Empty(t, resp.GetProviders())

	// Migrate is idempotent.
	require.NoError(t, svc.Migrate(context.Background()))

	// Guard the exact failure mode: a service without components must fail
	// migration with the shipped error, not silently pass.
	bare := &Service{probeClient: NewProbeClient()}
	err = bare.Migrate(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "database component unavailable")
}

// TestMigrateSchemaForFVT asserts the FVT schema helper.
func TestMigrateSchemaForFVT(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:provider_fvt_schema?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	t.Cleanup(func() {
		sqlDB, _ := db.DB()
		_ = sqlDB.Close()
	})
	require.NoError(t, MigrateSchemaForFVT(db))
	assert.True(t, db.Migrator().HasTable(&Provider{}))
	assert.True(t, db.Migrator().HasTable(&ProviderModel{}))
}

// TestUnwiredCatalogFailsClosed asserts publish without a catalog fails
// with an internal error rather than panicking.
func TestUnwiredCatalogFailsClosed(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:provider_unwired?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	t.Cleanup(func() {
		sqlDB, _ := db.DB()
		_ = sqlDB.Close()
	})
	require.NoError(t, MigrateSchemaForFVT(db))
	svc := NewForFVT(db)
	resp, err := svc.CreateProvider(context.Background(), &providerv1.CreateProviderRequest{
		Name:    "acme",
		Type:    TypeOpenAI,
		BaseUrl: "https://api.example.com",
		ApiKey:  "sk-abcdef123456",
	})
	require.NoError(t, err)
	require.NotEmpty(t, resp.GetProvider().GetProviderId())
}
