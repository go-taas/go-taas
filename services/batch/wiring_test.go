package batch

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"

	batchv1 "github.com/go-taas/go-taas/proto/taas/batch/v1"
)

// TestWiring covers the service registration, Migrate, and runner Run
// methods that the FVT stack exercises end-to-end.
func TestWiring(t *testing.T) {
	db := newTestDB(t)
	store := NewMemFileStore()
	svc := NewForFVT(db, store)

	// MigrateSchemaForFVT.
	require.NoError(t, MigrateSchemaForFVT(db))

	// ServiceName.
	assert.Equal(t, "batch", svc.ServiceName())

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

// TestBatchWorkerRun covers the runner Run loop (cancelled immediately).
func TestBatchWorkerRun(t *testing.T) {
	db := newTestDB(t)
	repo := NewRepository(db)
	store := NewMemFileStore()
	worker := NewBatchWorker(repo, store, &fakeInferenceClient{}, &fakeMeterer{}, 2, time.Millisecond)
	worker.SetFileTTL(24 * time.Hour)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	require.NoError(t, worker.Run(ctx))
	assert.Equal(t, "batch-worker", worker.RunnerName())
}

// TestBatchRetentionRunnerRun covers the retention runner Run loop.
func TestBatchRetentionRunnerRun(t *testing.T) {
	db := newTestDB(t)
	repo := NewRepository(db)
	store := NewMemFileStore()
	runner := NewBatchRetentionRunner(repo, store, 24*time.Hour, time.Millisecond)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	require.NoError(t, runner.Run(ctx))
	assert.Equal(t, "batch-retention-runner", runner.RunnerName())
}

// TestHTTPInferenceClient covers the HTTP inference client.
func TestHTTPInferenceClient(t *testing.T) {
	// Use a port that is not listening; the client should fail fast with
	// a connection error.
	client := NewHTTPInferenceClient("http://127.0.0.1:1", "secret")
	assert.NotNil(t, client)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, _, _, err := client.Complete(ctx, "gpt-4o", "/v1/chat/completions", []byte(`{}`))
	require.Error(t, err)
}

// TestFormatErrorLine covers the error-line formatter.
func TestFormatErrorLine(t *testing.T) {
	line := formatErrorLine("custom-1", "boom")
	assert.Contains(t, string(line), "custom-1")
	assert.Contains(t, string(line), "boom")
}

// TestNewBatchWorkerFromConfig covers the config-based constructor.
func TestNewBatchWorkerFromConfig(t *testing.T) {
	db := newTestDB(t)
	repo := NewRepository(db)
	store := NewMemFileStore()
	worker := NewBatchWorkerFromConfig(nil, repo, store, &fakeInferenceClient{}, &fakeMeterer{})
	assert.NotNil(t, worker)
}

// TestNewBatchRetentionRunnerFromConfig covers the config-based
// constructor.
func TestNewBatchRetentionRunnerFromConfig(t *testing.T) {
	db := newTestDB(t)
	repo := NewRepository(db)
	store := NewMemFileStore()
	runner := NewBatchRetentionRunnerFromConfig(nil, repo, store)
	assert.NotNil(t, runner)
}

// TestServiceNew covers the production constructor.
func TestServiceNew(t *testing.T) {
	svc := New(nil)
	assert.NotNil(t, svc)
	assert.Equal(t, "batch", svc.ServiceName())
}

// TestProtoRegistration ensures the gRPC service is registered.
func TestProtoRegistration(t *testing.T) {
	grpcSrv := grpc.NewServer()
	svc := NewForFVT(newTestDB(t), NewMemFileStore())
	svc.AttachToServer(grpcSrv)
	// The service info should include the batch service.
	info := grpcSrv.GetServiceInfo()
	_, ok := info[batchv1.BatchService_ServiceDesc.ServiceName]
	assert.True(t, ok)
}
