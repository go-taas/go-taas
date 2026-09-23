package infer

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	"github.com/go-taas/go-taas/pkg/mq"
	"github.com/go-taas/go-taas/services/model"

	apierrors "github.com/go-taas/go-taas/pkg/errors"
)

func newInferTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := fmt.Sprintf("file:%s?mode=memory&cache=shared", t.Name())
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&InferenceService{}))
	require.NoError(t, model.MigrateSchemaForFVT(db))
	t.Cleanup(func() {
		sqlDB, _ := db.DB()
		_ = sqlDB.Close()
	})
	return db
}

// recordingClient records every published message.
type recordingClient struct {
	mq.Client
	published []mq.Message
	failNext  bool
}

func (c *recordingClient) Publish(_ context.Context, _ string, body []byte, headers map[string]string) error {
	if c.failNext {
		return fmt.Errorf("publish failed")
	}
	c.published = append(c.published, mq.Message{Body: body, Headers: headers})
	return nil
}

func (c *recordingClient) Subscribe(context.Context, string, mq.Handler) error { return nil }
func (c *recordingClient) Close() error                                       { return nil }

func newInferTestService(t *testing.T) (*Service, *recordingClient, *gorm.DB) {
	t.Helper()
	db := newInferTestDB(t)
	client := &recordingClient{}
	svc := NewForFVT(db, client)
	return svc, client, db
}

// seedModel registers a model + version and returns the model id.
func seedModel(t *testing.T, db *gorm.DB, name, version string) string {
	t.Helper()
	repo := model.NewRepository(db)
	id, err := repo.RegisterModelOrCreateVersion(context.Background(), name, "", version, name+"/"+version)
	require.NoError(t, err)
	return id
}

func seedService(t *testing.T, repo *InferenceServiceRepository, org, name, state string, updated time.Time) *InferenceService {
	t.Helper()
	svc := &InferenceService{
		ID:             NewServiceID(),
		OrganizationID: org,
		Name:           name,
		ModelID:        "model-1",
		ModelVersion: "v1",
		ImageID:        "img-1",
		Replicas:       1,
		Accelerator:    "nvidia",
		State:          state,
		Endpoints:      []string{},
		CreatedAt:      updated,
		UpdatedAt:      updated,
	}
	require.NoError(t, repo.Create(context.Background(), svc))
	return svc
}

func TestRepositoryCreateAndFindScoped(t *testing.T) {
	db := newInferTestDB(t)
	repo := NewInferenceServiceRepository(db)
	ctx := context.Background()
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	seedService(t, repo, "org-a", "svc-a", StateRunning, base)
	seedService(t, repo, "org-b", "svc-b", StateRunning, base)

	// Same org: found.
	row, err := repo.FindByIDAndOrganization(ctx, "org-a", func() string {
		rows, _, err := repo.ListByOrganization(ctx, "org-a", 0, 10, false)
		require.NoError(t, err)
		return rows[0].ID
	}())
	require.NoError(t, err)
	assert.Equal(t, "svc-a", row.Name)

	// Cross-org: not found (no leak).
	_, err = repo.FindByIDAndOrganization(ctx, "org-b", row.ID)
	require.Error(t, err)
	ae, ok := apierrors.As(err)
	require.True(t, ok)
	assert.Equal(t, apierrors.CodeInferServiceNotFound, ae.Code)
}

func TestRepositoryCreateDuplicate(t *testing.T) {
	db := newInferTestDB(t)
	repo := NewInferenceServiceRepository(db)
	ctx := context.Background()

	seedService(t, repo, "org-a", "dup", StatePending, time.Now())
	err := repo.Create(ctx, &InferenceService{
		ID: NewServiceID(), OrganizationID: "org-a", Name: "dup",
		State: StatePending, Endpoints: []string{},
	})
	require.Error(t, err)
	ae, ok := apierrors.As(err)
	require.True(t, ok)
	assert.Equal(t, apierrors.CodeInferServiceExists, ae.Code)
}

func TestRepositoryListExcludesTerminated(t *testing.T) {
	db := newInferTestDB(t)
	repo := NewInferenceServiceRepository(db)
	ctx := context.Background()
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	seedService(t, repo, "org", "live", StateRunning, base)
	seedService(t, repo, "org", "dead", StateTerminated, base.Add(time.Hour))

	// Default: terminated excluded.
	rows, total, err := repo.ListByOrganization(ctx, "org", 0, 10, false)
	require.NoError(t, err)
	assert.Equal(t, int64(1), total)
	require.Len(t, rows, 1)
	assert.Equal(t, "live", rows[0].Name)

	// Include terminated.
	rows, total, err = repo.ListByOrganization(ctx, "org", 0, 10, true)
	require.NoError(t, err)
	assert.Equal(t, int64(2), total)
	require.Len(t, rows, 2)
	// Newest first: dead (terminated later) is first.
	assert.Equal(t, "dead", rows[0].Name)
}

func TestRepositoryUpdateReplicasAndMarkTerminated(t *testing.T) {
	db := newInferTestDB(t)
	repo := NewInferenceServiceRepository(db)
	ctx := context.Background()

	svc := seedService(t, repo, "org", "svc", StateRunning, time.Now())

	require.NoError(t, repo.UpdateReplicas(ctx, "org", svc.ID, 5))
	row, err := repo.FindByIDAndOrganization(ctx, "org", svc.ID)
	require.NoError(t, err)
	assert.Equal(t, 5, row.Replicas)

	// UpdateReplicas on a missing service: 10301.
	err = repo.UpdateReplicas(ctx, "org", "missing", 5)
	require.Error(t, err)
	ae, ok := apierrors.As(err)
	require.True(t, ok)
	assert.Equal(t, apierrors.CodeInferServiceNotFound, ae.Code)

	// MarkTerminated is idempotent.
	require.NoError(t, repo.MarkTerminated(ctx, "org", svc.ID))
	require.NoError(t, repo.MarkTerminated(ctx, "org", svc.ID))
	row, err = repo.FindByIDAndOrganization(ctx, "org", svc.ID)
	require.NoError(t, err)
	assert.Equal(t, StateTerminated, row.State)
}

func TestRepositoryApplyStatus(t *testing.T) {
	db := newInferTestDB(t)
	repo := NewInferenceServiceRepository(db)
	ctx := context.Background()

	svc := seedService(t, repo, "org", "svc", StateDeploying, time.Now())

	// running with endpoints.
	require.NoError(t, repo.ApplyStatus(ctx, svc.ID, StateRunning, []string{"http://e"}, nil))
	row, err := repo.FindByIDAndOrganization(ctx, "org", svc.ID)
	require.NoError(t, err)
	assert.Equal(t, StateRunning, row.State)
	assert.Equal(t, []string{"http://e"}, row.Endpoints)
	require.Nil(t, row.FailureReason)

	// failed with reason.
	reason := "image pull backoff"
	require.NoError(t, repo.ApplyStatus(ctx, svc.ID, StateFailed, nil, &reason))
	row, err = repo.FindByIDAndOrganization(ctx, "org", svc.ID)
	require.NoError(t, err)
	assert.Equal(t, StateFailed, row.State)
	require.NotNil(t, row.FailureReason)
	assert.Equal(t, reason, *row.FailureReason)

	// back to running clears the reason.
	require.NoError(t, repo.ApplyStatus(ctx, svc.ID, StateRunning, []string{"http://e"}, nil))
	row, err = repo.FindByIDAndOrganization(ctx, "org", svc.ID)
	require.NoError(t, err)
	require.Nil(t, row.FailureReason)

	// Unknown service: 10301.
	err = repo.ApplyStatus(ctx, "missing", StateRunning, nil, nil)
	require.Error(t, err)
	ae, ok := apierrors.As(err)
	require.True(t, ok)
	assert.Equal(t, apierrors.CodeInferServiceNotFound, ae.Code)
}

func TestRepositoryCountAndBlocking(t *testing.T) {
	db := newInferTestDB(t)
	repo := NewInferenceServiceRepository(db)
	ctx := context.Background()

	live := seedService(t, repo, "org", "live", StateRunning, time.Now())
	seedService(t, repo, "org", "dead", StateTerminated, time.Now())

	count, err := repo.CountByModelID(ctx, live.ModelID, true)
	require.NoError(t, err)
	assert.Equal(t, int64(1), count)

	blocking, err := repo.FindBlockingServiceByModel(ctx, live.ModelID)
	require.NoError(t, err)
	require.NotNil(t, blocking)
	assert.Equal(t, "live", blocking.Name)

	// No blocking service for an unreferenced model.
	blocking, err = repo.FindBlockingServiceByModel(ctx, "unreferenced")
	require.NoError(t, err)
	assert.Nil(t, blocking)
}

func TestDeleteModelGuard(t *testing.T) {
	db := newInferTestDB(t)
	repo := NewInferenceServiceRepository(db)
	ctx := context.Background()

	live := seedService(t, repo, "org", "live", StateRunning, time.Now())
	seedService(t, repo, "org", "dead", StateTerminated, time.Now())

	guard := NewDeleteModelGuard(db)

	// Live reference blocks.
	err := guard(ctx, live.ModelID)
	require.Error(t, err)
	ae, ok := apierrors.As(err)
	require.True(t, ok)
	assert.Equal(t, apierrors.CodeModelNotFound, ae.Code)
	assert.Contains(t, ae.Error(), "live")

	// Terminated-only references do not block.
	err = guard(ctx, "unreferenced")
	require.NoError(t, err)
}

func TestChangeEventJSONShape(t *testing.T) {
	svc := &InferenceService{
		ID: "svc-1", OrganizationID: "org-1", Name: "demo",
		ModelID: "model-1", ModelVersion: "v1", ImageID: "img-1",
		Replicas: 2, Accelerator: "nvidia", AcceleratorType: "A100",
	}
	evt := buildChangeEvent(EventTypeUpsert, svc, "qwen/v1", "ghcr.io/go-taas/vllm:v0.6.3", "vllm")

	body, err := json.Marshal(evt)
	require.NoError(t, err)

	var raw map[string]any
	require.NoError(t, json.Unmarshal(body, &raw))
	assert.Equal(t, "upsert", raw["event_type"])
	assert.Equal(t, "svc-1", raw["service_id"])
	assert.Equal(t, "org-1", raw["organization_id"])
	assert.Equal(t, "demo", raw["name"])
	modelRaw, ok := raw["model"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "model-1", modelRaw["model_id"])
	assert.Equal(t, "qwen/v1", modelRaw["weight_path"])
	imageRaw, ok := raw["image"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "ghcr.io/go-taas/vllm:v0.6.3", imageRaw["reference"])
	assert.Equal(t, "vllm", imageRaw["engine"])
	assert.Equal(t, float64(2), raw["replicas"])
	assert.Equal(t, "nvidia", raw["accelerator"])
}

func TestPublishChangeHeaders(t *testing.T) {
	client := &recordingClient{}
	svc := &InferenceService{ID: "svc-1", OrganizationID: "org-1", Name: "demo"}
	evt := buildChangeEvent(EventTypeDelete, svc, "", "", "")

	require.NoError(t, publishChange(context.Background(), client, evt))
	require.Len(t, client.published, 1)
	msg := client.published[0]
	assert.Equal(t, EventTypeDelete, msg.Headers["event_type"])
	assert.Equal(t, "svc-1", msg.Headers["service_id"])
}

func TestPublishChangeWithCompensationRollsBack(t *testing.T) {
	db := newInferTestDB(t)
	repo := NewInferenceServiceRepository(db)
	ctx := context.Background()

	svc := seedService(t, repo, "org", "svc", StatePending, time.Now())
	client := &recordingClient{failNext: true}
	evt := buildChangeEvent(EventTypeUpsert, svc, "p", "ref", "vllm")

	err := publishChangeWithCompensation(ctx, client, repo, evt, svc.ID)
	require.Error(t, err)

	// The compensating delete terminated the row.
	row, err := repo.FindByIDAndOrganization(ctx, "org", svc.ID)
	require.NoError(t, err)
	assert.Equal(t, StateTerminated, row.State)
}
