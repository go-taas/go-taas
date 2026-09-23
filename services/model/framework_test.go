package model

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"

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
	return &modelv1.RegisterModelRequest{Name: name, Version: version, WeightPath: weightPath}
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
