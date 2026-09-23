package infer

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"

	inferv1 "github.com/go-taas/go-taas/proto/taas/infer/v1"

	"github.com/go-taas/go-taas/pkg/config"
	"github.com/go-taas/go-taas/pkg/mq"
	"github.com/go-taas/go-taas/pkg/server"
	"github.com/go-taas/go-taas/services/model"
)

// fakeComponents is a minimal server.Components backed by a test DB and
// MQ client.
type fakeComponents struct {
	db     any
	mqType string // "client" | "wrong" | ""
}

func (f *fakeComponents) DB() server.DBComponent {
	if f.db == nil {
		return nil
	}
	return &fakeDBComponent{db: f.db}
}
func (f *fakeComponents) Redis() server.RedisComponent { return nil }
func (f *fakeComponents) MQ() server.MQComponent {
	switch f.mqType {
	case "client":
		return &fakeMQComponent{client: mq.NewFake()}
	case "wrong":
		return &fakeMQComponent{client: "not-a-client"}
	default:
		return nil
	}
}

type fakeDBComponent struct{ db any }

func (d *fakeDBComponent) GormDB() any { return d.db }

type fakeMQComponent struct{ client any }

func (m *fakeMQComponent) Publish(ctx context.Context, subject string, body []byte, headers map[string]string) error {
	client, ok := m.client.(mq.Client)
	if !ok {
		return assert.AnError
	}
	return client.Publish(ctx, subject, body, headers)
}
func (m *fakeMQComponent) Client() any { return m.client }

func TestServiceFrameworkIntegration(t *testing.T) {
	db := newInferTestDB(t)
	svc := New(&fakeComponents{db: db, mqType: "client"})
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

	// The lazy wiring resolves through components.
	repo, err := svc.repository()
	require.NoError(t, err)
	require.NotNil(t, repo)
	modelRepo, err := svc.modelRepository()
	require.NoError(t, err)
	require.NotNil(t, modelRepo)
	client, err := svc.mqClientFor()
	require.NoError(t, err)
	require.NotNil(t, client)
}

func TestServiceNewWithDependencies(t *testing.T) {
	db := newInferTestDB(t)
	svc := NewWithDependencies(
		NewInferenceServiceRepository(db),
		model.NewRepository(db),
		mq.NewFake(),
	)
	require.NotNil(t, svc)
	require.NoError(t, MigrateSchemaForFVT(db))
}

func TestServiceWithoutComponents(t *testing.T) {
	svc := New(nil)
	_, err := svc.ListInferenceServices(context.Background(), &inferv1.ListInferenceServicesRequest{})
	require.Error(t, err)
}

func TestServiceUnexpectedHandles(t *testing.T) {
	// Wrong DB handle type.
	svc := New(&fakeComponents{db: "not-a-db", mqType: "client"})
	err := svc.Migrate(context.Background())
	require.Error(t, err)

	// MQ component unavailable.
	svc = New(&fakeComponents{db: newInferTestDB(t), mqType: ""})
	_, err = svc.CreateInferenceService(orgContext("org"), &inferv1.CreateInferenceServiceRequest{
		Name: "demo", Replicas: 1, Accelerator: "nvidia",
	})
	require.Error(t, err)

	// Wrong MQ handle type.
	svc = New(&fakeComponents{db: newInferTestDB(t), mqType: "wrong"})
	_, err = svc.CreateInferenceService(orgContext("org"), &inferv1.CreateInferenceServiceRequest{
		Name: "demo", Replicas: 1, Accelerator: "nvidia",
	})
	require.Error(t, err)
}

func TestStatusConsumerRunSubscribes(t *testing.T) {
	db := newInferTestDB(t)
	repo := NewInferenceServiceRepository(db)
	bus := mq.NewFake()
	consumer := NewStatusConsumer(bus, repo, 0) // workers <= 0 → 1

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- consumer.Run(ctx) }()

	// A published report reaches the repository through the
	// subscription.
	svc := seedService(t, repo, "org", "svc", StateDeploying, time.Now().UTC())
	body, err := json.Marshal(statusReport{ServiceID: svc.ID, State: StateRunning, Endpoints: []string{"http://e"}})
	require.NoError(t, err)
	require.NoError(t, bus.Publish(ctx, mq.DefaultSubjects().InferServiceStatus, body, nil))

	require.Eventually(t, func() bool {
		row, err := repo.FindByIDAndOrganization(ctx, "org", svc.ID)
		return err == nil && row.State == StateRunning
	}, 2*time.Second, 10*time.Millisecond)

	cancel()
	select {
	case err := <-done:
		assert.NoError(t, err)
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return after cancel")
	}
}

func TestNewStatusConsumerRunner(t *testing.T) {
	// Load a configuration with the status consumer enabled.
	dir := t.TempDir()
	path := filepath.Join(dir, "test.yaml")
	content := `
db:
  master:
    host: localhost
    port: 5432
    dbName: taas
    user: taas
    password: secret
    maxOpenConns: 10
    maxIdleConns: 5
redis:
  host: localhost
  port: 6379
mq:
  driver: nats
  url: nats://localhost:4222
infer:
  endpointBaseURL: ""
  statusConsumer:
    enabled: true
    workers: 2
`
	require.NoError(t, os.WriteFile(path, []byte(content), 0o600))
	config.ParseConfigs(path)

	// Disabled via configuration.
	require.Nil(t, NewStatusConsumerRunner(nil))

	// Missing components.
	require.Nil(t, NewStatusConsumerRunner(&fakeComponents{}))

	// Full wiring.
	db := newInferTestDB(t)
	consumer := NewStatusConsumerRunner(&fakeComponents{db: db, mqType: "client"})
	require.NotNil(t, consumer)
	assert.Equal(t, 2, consumer.workers)
}
