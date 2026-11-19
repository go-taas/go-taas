package model

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	modelv1 "github.com/go-taas/go-taas/proto/taas/model/v1"

	"github.com/go-taas/go-taas/pkg/server"
)

// fakeComponents is a minimal server.Components backed by a test DB.
type fakeComponents struct {
	db any
}

func (f *fakeComponents) DB() server.DBComponent {
	if f.db == nil {
		return nil
	}
	return &fakeDBComponent{db: f.db}
}
func (f *fakeComponents) Redis() server.RedisComponent { return nil }
func (f *fakeComponents) MQ() server.MQComponent       { return nil }

type fakeDBComponent struct{ db any }

func (d *fakeDBComponent) GormDB() any { return d.db }

func registerModelReq(name, version, weightPath string) *modelv1.RegisterModelRequest {
	return &modelv1.RegisterModelRequest{
		Name:       name,
		Version:    version,
		HubModelId: name + "/hub",
		WeightPath: weightPath,
	}
}

func TestServiceFrameworkIntegration(t *testing.T) {
	db := newModelTestDB(t)
	svc := New(&fakeComponents{db: db})
	ctx := context.Background()

	// Framework metadata.
	assert.Equal(t, ServiceName, svc.ServiceName())
	assert.NotNil(t, svc.GetServiceHandlerRegisterFn())

	// AttachToServer registers without panicking.
	grpcSrv := grpc.NewServer()
	svc.AttachToServer(grpcSrv)
	grpcSrv.Stop()

	// Migrate through the components path.
	require.NoError(t, svc.Migrate(ctx))

	// The lazy repository wiring resolves through components.
	repo, err := svc.repository()
	require.NoError(t, err)
	require.NotNil(t, repo)

	// End-to-end through the components-wired service.
	_, err = svc.RegisterModel(ctx, registerModelReq("qwen-3b", "v1", "qwen/v1"))
	require.NoError(t, err)
}

func TestServiceNewWithRepository(t *testing.T) {
	db := newModelTestDB(t)
	repo := NewRepository(db)
	svc := NewWithRepository(repo)

	_, err := svc.RegisterModel(context.Background(), registerModelReq("qwen-3b", "v1", "qwen/v1"))
	require.NoError(t, err)
}

func TestServiceMigrateSchemaForFVT(t *testing.T) {
	db := newModelTestDB(t)
	require.NoError(t, MigrateSchemaForFVT(db))
}

// TestMigrateActiveVersionIndex verifies the migration drops the stale
// GLOBAL (is_active) WHERE is_active index and AutoMigrate recreates it
// as the per-model (model_id) WHERE is_active index (feature #32, AD2).
func TestMigrateActiveVersionIndex(t *testing.T) {
	// A fresh DB without AutoMigrate so we can create the stale global
	// index first.
	dsn := fmt.Sprintf("file:%s?mode=memory&cache=shared", t.Name())
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.Exec(`CREATE TABLE model_versions (
		id TEXT PRIMARY KEY,
		model_id TEXT NOT NULL,
		version TEXT NOT NULL,
		weight_path TEXT NOT NULL,
		is_active BOOLEAN NOT NULL DEFAULT false,
		created_at DATETIME
	)`).Error)
	// Simulate the stale schema: create the global index first, then run
	// the migration.
	require.NoError(t, db.Exec(`CREATE UNIQUE INDEX idx_model_versions_active ON model_versions (is_active) WHERE is_active`).Error)
	require.NoError(t, migrateActiveVersionIndex(db))
	require.NoError(t, db.AutoMigrate(&Model{}, &Version{}, &Authorization{}))

	// The recreated index must be per-model (model_id, is_active).
	indexes, err := db.Migrator().GetIndexes(&Version{})
	require.NoError(t, err)
	found := false
	for _, idx := range indexes {
		if idx.Name() == "idx_model_versions_active" {
			found = true
			assert.Equal(t, []string{"model_id", "is_active"}, idx.Columns())
		}
	}
	assert.True(t, found, "idx_model_versions_active must exist after migration")
}

// TestMigrateActiveVersionIndexIdempotent verifies the migration is a
// no-op when the index is already per-model.
func TestMigrateActiveVersionIndexIdempotent(t *testing.T) {
	db := newModelTestDB(t)
	require.NoError(t, db.AutoMigrate(&Model{}, &Version{}, &Authorization{}))

	// The per-model index already exists; the migration must not drop it.
	require.NoError(t, migrateActiveVersionIndex(db))
	indexes, err := db.Migrator().GetIndexes(&Version{})
	require.NoError(t, err)
	for _, idx := range indexes {
		if idx.Name() == "idx_model_versions_active" {
			assert.Equal(t, []string{"model_id", "is_active"}, idx.Columns())
		}
	}
}

func TestServiceWithoutComponents(t *testing.T) {
	svc := New(nil)
	_, err := svc.RegisterModel(context.Background(), registerModelReq("m", "v1", "p"))
	require.Error(t, err)
}

func TestServiceGormDBUnexpectedHandle(t *testing.T) {
	svc := New(&fakeComponents{db: "not-a-gorm-db"})
	err := svc.Migrate(context.Background())
	require.Error(t, err)
}
