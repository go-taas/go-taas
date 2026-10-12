package runtime

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	"github.com/go-taas/go-taas/pkg/server"
	runtimev1 "github.com/go-taas/go-taas/proto/taas/runtime/v1"
)

// fakeDBComponent adapts a *gorm.DB to the server.DBComponent surface.
type fakeDBComponent struct{ db *gorm.DB }

func (f fakeDBComponent) GormDB() any { return f.db }

// fakeComponents implements server.Components with only the database
// wired; the runtime service needs no other component.
type fakeComponents struct{ db server.DBComponent }

func (f fakeComponents) DB() server.DBComponent       { return f.db }
func (f fakeComponents) Redis() server.RedisComponent { return nil }
func (f fakeComponents) MQ() server.MQComponent       { return nil }

// TestNewWithCacheMigratesRuntimeSettings is the regression test for the
// startup bug: the constructor used by apps/taas-server/main.go must
// carry the shared components so Migrate() can resolve the database.
// Before the fix NewWithCache took the cache only, so Migrate returned
// "[500] runtime: database component unavailable" and the server aborted
// with FATAL "service migration failed {"service": "runtime"}".
func TestNewWithCacheMigratesRuntimeSettings(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:runtime_wiring?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	t.Cleanup(func() {
		sqlDB, _ := db.DB()
		_ = sqlDB.Close()
	})

	// Construct exactly the way main.go does.
	svc := NewWithCache(fakeComponents{db: fakeDBComponent{db: db}}, NewProjectionCache())

	require.NoError(t, svc.Migrate(context.Background()), "Migrate must succeed with components wired")
	require.True(t, db.Migrator().HasTable(&RuntimeSetting{}), "runtime_settings table must be created")

	// The table is queryable through the service (the scheme defaults to
	// plain on an empty table).
	resp, err := svc.GetServingScheme(context.Background(), &runtimev1.GetServingSchemeRequest{})
	require.NoError(t, err)
	assert.Equal(t, SchemePlain, resp.Scheme.GetScheme())

	// Migrate is idempotent (second call is a no-op).
	require.NoError(t, svc.Migrate(context.Background()))

	// Guard the exact failure mode of the bug: a cache-only service
	// (components not wired) must fail migration with the shipped error,
	// not silently pass. This is what aborted the server with FATAL.
	bare := &Service{cache: NewProjectionCache()}
	err = bare.Migrate(context.Background())
	require.Error(t, err, "a service without components must fail migration")
	assert.Contains(t, err.Error(), "database component unavailable")
}
