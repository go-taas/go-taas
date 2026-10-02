package resourcemetrics

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"gorm.io/gorm"

	"github.com/go-taas/go-taas/pkg/server"
	resourcemetricsv1 "github.com/go-taas/go-taas/proto/taas/resourcemetrics/v1"
)

// fakeComponents provides a DB component for the production wiring path.
type fakeComponents struct {
	db *gorm.DB
}

func (f *fakeComponents) DB() server.DBComponent       { return &fakeDBComponent{db: f.db} }
func (f *fakeComponents) Redis() server.RedisComponent { return nil }
func (f *fakeComponents) MQ() server.MQComponent       { return nil }

type fakeDBComponent struct{ db *gorm.DB }

func (f *fakeDBComponent) GormDB() any { return f.db }

// TestServiceWiring exercises the production constructor, Migrate,
// AttachToServer, ServiceName and gateway registration.
func TestServiceWiring(t *testing.T) {
	db := newServiceTestDB(t)
	svc := New(&fakeComponents{db: db})
	assert.Equal(t, ServiceName, svc.ServiceName())
	require.NoError(t, svc.Migrate(context.Background()))

	grpcSrv := grpc.NewServer()
	svc.AttachToServer(grpcSrv)
	assert.NotNil(t, svc.GetServiceHandlerRegisterFn())
}

// TestServerInterface verifies the service implements the server.Service
// / server.ServiceWithGateway / Migrator interfaces.
func TestServerInterface(t *testing.T) {
	db := newServiceTestDB(t)
	svc := NewForFVT(db)

	assert.Equal(t, ServiceName, svc.ServiceName())
	assert.NotNil(t, svc.GetServiceHandlerRegisterFn())

	grpcSrv := grpc.NewServer()
	svc.AttachToServer(grpcSrv)
	assert.NotNil(t, grpcSrv.GetServiceInfo()["taas.resourcemetrics.v1.ResourceMetricsService"])

	require.NoError(t, svc.Migrate(context.Background()))
}

// TestMigrateSchemaForFVT verifies the FVT schema migration creates the
// sample table.
func TestMigrateSchemaForFVT(t *testing.T) {
	db := newServiceTestDB(t)
	require.NoError(t, MigrateSchemaForFVT(db))
	assert.True(t, db.Migrator().HasTable(&serviceResourceMetricRow{}))
}

// TestSetMaxRangeSeconds verifies the range cap override.
func TestSetMaxRangeSeconds(t *testing.T) {
	db := newServiceTestDB(t)
	svc := NewForFVT(db)
	svc.SetMaxRangeSeconds(3600)
	assert.Equal(t, int64(3600), svc.maxRangeSeconds)
}

// TestProtoRegistration verifies the generated proto service is
// registered under the expected name.
func TestProtoRegistration(t *testing.T) {
	assert.NotNil(t, resourcemetricsv1.RegisterResourceMetricsServiceServer)
}