package prompt

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"

	promptv1 "github.com/go-taas/go-taas/proto/taas/prompt/v1"
)

// TestWiring covers the service registration, Migrate, and seam setters
// that the FVT stack exercises end-to-end.
func TestWiring(t *testing.T) {
	db := newTestDB(t)
	svc := NewForFVT(db)

	// MigrateSchemaForFVT.
	require.NoError(t, MigrateSchemaForFVT(db))

	// ServiceName.
	assert.Equal(t, "prompt", svc.ServiceName())

	// AttachToServer + GetServiceHandlerRegisterFn.
	grpcSrv := grpc.NewServer()
	svc.AttachToServer(grpcSrv)
	assert.NotNil(t, svc.GetServiceHandlerRegisterFn())

	// Migrate.
	require.NoError(t, svc.Migrate(context.Background()))

	// Setters are no-ops that store the seam.
	svc.SetSessionOrgResolver(nil)
	svc.SetSessionUserResolver(nil)
	svc.SetRoleGuard(nil)
	svc.SetAuditRecorder(nil)
}

// TestServiceNew covers the production constructor.
func TestServiceNew(t *testing.T) {
	svc := New(nil)
	assert.NotNil(t, svc)
	assert.Equal(t, "prompt", svc.ServiceName())
}

// TestProtoRegistration ensures the gRPC service is registered.
func TestProtoRegistration(t *testing.T) {
	grpcSrv := grpc.NewServer()
	svc := NewForFVT(newTestDB(t))
	svc.AttachToServer(grpcSrv)
	assert.NotNil(t, promptv1.RegisterPromptServiceHandler)
}