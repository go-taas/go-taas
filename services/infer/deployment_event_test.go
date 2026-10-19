package infer

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	inferv1 "github.com/go-taas/go-taas/proto/taas/infer/v1"

	apierrors "github.com/go-taas/go-taas/pkg/errors"
)

// TestSpecDiffOmitUnchanged verifies the field-level diff omits unchanged
// fields (feature #34, AC2).
func TestSpecDiffOmitUnchanged(t *testing.T) {
	before := &InferenceService{
		Replicas:        2,
		ModelVersion:    "v1",
		ImageID:         "img-1",
		Accelerator:     "nvidia",
		AcceleratorType: "A800",
	}
	after := &InferenceService{
		Replicas:        4,
		ModelVersion:    "v1",
		ImageID:         "img-1",
		Accelerator:     "nvidia",
		AcceleratorType: "A800",
	}
	b, a := specDiff(before, after)
	var bm, am map[string]any
	require.NoError(t, json.Unmarshal(b, &bm))
	require.NoError(t, json.Unmarshal(a, &am))
	// Only replicas changed; unchanged fields are omitted.
	assert.Equal(t, float64(2), bm["replicas"])
	assert.Equal(t, float64(4), am["replicas"])
	assert.NotContains(t, bm, "model_version")
	assert.NotContains(t, am, "model_version")
	assert.NotContains(t, bm, "image_id")
	assert.NotContains(t, am, "image_id")
	assert.NotContains(t, bm, "accelerator")
	assert.NotContains(t, am, "accelerator")
}

// TestSpecDiffNilSides verifies a nil side renders as an empty diff
// (the before of a create, the after of a delete).
func TestSpecDiffNilSides(t *testing.T) {
	svc := &InferenceService{
		Replicas:        2,
		ModelVersion:    "v1",
		ImageID:         "img-1",
		Accelerator:     "nvidia",
		AcceleratorType: "A800",
	}
	// Create: before nil, after full.
	b, a := specDiff(nil, svc)
	var bm, am map[string]any
	require.NoError(t, json.Unmarshal(b, &bm))
	require.NoError(t, json.Unmarshal(a, &am))
	assert.Empty(t, bm)
	assert.Equal(t, float64(2), am["replicas"])
	assert.Equal(t, "v1", am["model_version"])

	// Delete: before full, after nil.
	b, a = specDiff(svc, nil)
	var bm2, am2 map[string]any
	require.NoError(t, json.Unmarshal(b, &bm2))
	require.NoError(t, json.Unmarshal(a, &am2))
	assert.Equal(t, float64(2), bm2["replicas"])
	assert.Empty(t, am2)
}

// TestDeploymentEventRepositoryRecordAndList verifies RecordEvent and
// ListEvents with filters and pagination (feature #34, AC1).
func TestDeploymentEventRepositoryRecordAndList(t *testing.T) {
	db := newInferTestDB(t)
	repo := NewDeploymentEventRepository(db)
	ctx := context.Background()
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	svc := &InferenceService{ID: "svc-1", Name: "demo", Replicas: 2, ModelVersion: "v1"}
	require.NoError(t, repo.RecordEvent(ctx, &DeploymentEvent{
		ServiceID: svc.ID, ServiceName: svc.Name, EventType: DeploymentEventCreate,
		Actor: "org-1", Before: []byte("{}"), After: []byte(`{"replicas":2}`), CreatedAt: base,
	}))
	require.NoError(t, repo.RecordEvent(ctx, &DeploymentEvent{
		ServiceID: svc.ID, ServiceName: svc.Name, EventType: DeploymentEventScale,
		Actor: "org-1", Before: []byte(`{"replicas":2}`), After: []byte(`{"replicas":4}`), CreatedAt: base.Add(time.Hour),
	}))
	require.NoError(t, repo.RecordEvent(ctx, &DeploymentEvent{
		ServiceID: "svc-2", ServiceName: "other", EventType: DeploymentEventCreate,
		Actor: "org-2", Before: []byte("{}"), After: []byte(`{"replicas":1}`), CreatedAt: base.Add(2 * time.Hour),
	}))

	// Newest first across all services.
	rows, total, err := repo.ListEvents(ctx, DeploymentEventFilter{}, 0, 20)
	require.NoError(t, err)
	assert.Equal(t, int64(3), total)
	require.Len(t, rows, 3)
	assert.Equal(t, "svc-2", rows[0].ServiceID, "newest first")

	// Filter by service.
	rows, total, err = repo.ListEvents(ctx, DeploymentEventFilter{ServiceID: "svc-1"}, 0, 20)
	require.NoError(t, err)
	assert.Equal(t, int64(2), total)
	require.Len(t, rows, 2)
	assert.Equal(t, DeploymentEventScale, rows[0].EventType)

	// Filter by event type.
	rows, total, err = repo.ListEvents(ctx, DeploymentEventFilter{EventType: DeploymentEventCreate}, 0, 20)
	require.NoError(t, err)
	assert.Equal(t, int64(2), total)
	require.Len(t, rows, 2)
	for _, r := range rows {
		assert.Equal(t, DeploymentEventCreate, r.EventType)
	}

	// Filter by actor.
	rows, total, err = repo.ListEvents(ctx, DeploymentEventFilter{Actor: "org-2"}, 0, 20)
	require.NoError(t, err)
	assert.Equal(t, int64(1), total)
	require.Len(t, rows, 1)
	assert.Equal(t, "org-2", rows[0].Actor)

	// Filter by time range.
	rows, total, err = repo.ListEvents(ctx, DeploymentEventFilter{
		Since: base.Add(30 * time.Minute).Unix(), Until: base.Add(90 * time.Minute).Unix(),
	}, 0, 20)
	require.NoError(t, err)
	assert.Equal(t, int64(1), total)
	assert.Equal(t, DeploymentEventScale, rows[0].EventType)

	// Pagination.
	rows, total, err = repo.ListEvents(ctx, DeploymentEventFilter{}, 0, 2)
	require.NoError(t, err)
	assert.Equal(t, int64(3), total)
	require.Len(t, rows, 2)
}

// TestDeploymentEventRepositoryGetEvent verifies GetEvent returns the
// event and maps a miss to CodeInferEndpointNotFound (feature #34, AC3).
func TestDeploymentEventRepositoryGetEvent(t *testing.T) {
	db := newInferTestDB(t)
	repo := NewDeploymentEventRepository(db)
	ctx := context.Background()

	ev := &DeploymentEvent{
		ServiceID: "svc-1", ServiceName: "demo", EventType: DeploymentEventCreate,
		Actor: "org-1", Before: []byte("{}"), After: []byte(`{"replicas":2}`),
	}
	require.NoError(t, repo.RecordEvent(ctx, ev))

	got, err := repo.GetEvent(ctx, ev.ID)
	require.NoError(t, err)
	assert.Equal(t, ev.ID, got.ID)
	assert.Equal(t, "svc-1", got.ServiceID)

	_, err = repo.GetEvent(ctx, "does-not-exist")
	require.Error(t, err)
	ae, ok := apierrors.As(err)
	require.True(t, ok)
	assert.Equal(t, apierrors.CodeInferEndpointNotFound, ae.Code)
}

// TestListDeploymentEvents verifies the RPC returns the trail newest
// first with diffs, and validates service_id and range (feature #34,
// AC1).
func TestListDeploymentEvents(t *testing.T) {
	seedImageRegistry(t)
	svc, _, db := newInferTestService(t)
	modelID := seedModel(t, db, "qwen-3b", "v1")

	// Create a service to seed a create event.
	resp, err := svc.CreateInferenceService(orgContext("org-1"), &inferv1.CreateInferenceServiceRequest{
		Name: "demo-svc", ModelId: modelID, ModelVersion: "v1",
		ImageId: "img-vllm-nvidia", Replicas: 2, Accelerator: "nvidia",
	})
	require.NoError(t, err)
	serviceID := resp.GetServiceId()

	// List the trail for the service.
	listResp, err := svc.ListDeploymentEvents(orgContext("org-1"), &inferv1.ListDeploymentEventsRequest{
		ServiceId: serviceID,
	})
	require.NoError(t, err)
	require.Len(t, listResp.GetEvents(), 1)
	ev := listResp.GetEvents()[0]
	assert.Equal(t, DeploymentEventCreate, ev.GetEventType())
	assert.Equal(t, serviceID, ev.GetServiceId())
	assert.Equal(t, "demo-svc", ev.GetServiceName())
	assert.Equal(t, "org-1", ev.GetActor())
	assert.Equal(t, "{}", ev.GetBefore())
	assert.Contains(t, ev.GetAfter(), "replicas")

	// Unknown service_id → 10301.
	_, err = svc.ListDeploymentEvents(orgContext("org-1"), &inferv1.ListDeploymentEventsRequest{
		ServiceId: "does-not-exist",
	})
	require.Error(t, err)
	ae, ok := apierrors.As(err)
	require.True(t, ok)
	assert.Equal(t, apierrors.CodeInferServiceNotFound, ae.Code)

	// Invalid range → 10404.
	_, err = svc.ListDeploymentEvents(orgContext("org-1"), &inferv1.ListDeploymentEventsRequest{
		Since: 200, Until: 100,
	})
	require.Error(t, err)
	ae, ok = apierrors.As(err)
	require.True(t, ok)
	assert.Equal(t, apierrors.CodeMeteringRangeInvalid, ae.Code)
}

// TestRollbackDeployment verifies rollback reverts to the target event's
// before, keeps service_id, transitions to deploying, and records a new
// rollback event (feature #34, AC3).
func TestRollbackDeployment(t *testing.T) {
	seedImageRegistry(t)
	svc, client, db := newInferTestService(t)
	modelID := seedModel(t, db, "qwen-3b", "v1")
	seedModel(t, db, "qwen-3b", "v2")

	// Create a service at v1 with 2 replicas.
	resp, err := svc.CreateInferenceService(orgContext("org-1"), &inferv1.CreateInferenceServiceRequest{
		Name: "demo-svc", ModelId: modelID, ModelVersion: "v1",
		ImageId: "img-vllm-nvidia", Replicas: 2, Accelerator: "nvidia",
	})
	require.NoError(t, err)
	serviceID := resp.GetServiceId()

	// Drive to running.
	repo, err := svc.repository()
	require.NoError(t, err)
	require.NoError(t, repo.ApplyStatus(context.Background(), serviceID, StateRunning, []string{"https://infer.example.com/demo/v1"}, nil, nil, nil))

	// Scale to 4 replicas → records a scale event.
	_, err = svc.ScaleInferenceService(orgContext("org-1"), &inferv1.ScaleInferenceServiceRequest{
		ServiceId: serviceID, Replicas: 4,
	})
	require.NoError(t, err)

	// List events: create + scale.
	listResp, err := svc.ListDeploymentEvents(orgContext("org-1"), &inferv1.ListDeploymentEventsRequest{
		ServiceId: serviceID,
	})
	require.NoError(t, err)
	require.Len(t, listResp.GetEvents(), 2)
	scaleEvent := listResp.GetEvents()[0]
	assert.Equal(t, DeploymentEventScale, scaleEvent.GetEventType())
	assert.Contains(t, scaleEvent.GetBefore(), "2")
	assert.Contains(t, scaleEvent.GetAfter(), "4")

	// Roll back to the scale event's before (replicas 2).
	client.published = nil
	rollbackResp, err := svc.RollbackDeployment(orgContext("org-1"), &inferv1.RollbackDeploymentRequest{
		ServiceId: serviceID, EventId: scaleEvent.GetEventId(),
	})
	require.NoError(t, err)
	assert.Equal(t, serviceID, rollbackResp.GetServiceId())
	assert.Equal(t, StateDeploying, rollbackResp.GetState())

	// A rollback change event was published.
	require.Len(t, client.published, 1)
	var evt changeEvent
	require.NoError(t, json.Unmarshal(client.published[0].Body, &evt))
	assert.Equal(t, EventTypeRollback, evt.EventType)
	assert.Equal(t, serviceID, evt.ServiceID)
	assert.Equal(t, 2, evt.Replicas, "rolled back to before replicas")

	// A new rollback event appears in the trail.
	listResp, err = svc.ListDeploymentEvents(orgContext("org-1"), &inferv1.ListDeploymentEventsRequest{
		ServiceId: serviceID,
	})
	require.NoError(t, err)
	require.Len(t, listResp.GetEvents(), 3)
	assert.Equal(t, DeploymentEventRollback, listResp.GetEvents()[0].GetEventType())
}

// TestRollbackDeploymentValidation verifies the validation order:
// unknown service → 10301, terminated → 10303, unknown event → 10304.
func TestRollbackDeploymentValidation(t *testing.T) {
	seedImageRegistry(t)
	svc, _, db := newInferTestService(t)
	modelID := seedModel(t, db, "qwen-3b", "v1")

	resp, err := svc.CreateInferenceService(orgContext("org-1"), &inferv1.CreateInferenceServiceRequest{
		Name: "demo-svc", ModelId: modelID, ModelVersion: "v1",
		ImageId: "img-vllm-nvidia", Replicas: 2, Accelerator: "nvidia",
	})
	require.NoError(t, err)
	serviceID := resp.GetServiceId()

	// Unknown service → 10301.
	_, err = svc.RollbackDeployment(orgContext("org-1"), &inferv1.RollbackDeploymentRequest{
		ServiceId: "does-not-exist", EventId: "evt-1",
	})
	require.Error(t, err)
	ae, ok := apierrors.As(err)
	require.True(t, ok)
	assert.Equal(t, apierrors.CodeInferServiceNotFound, ae.Code)

	// Unknown event → 10304.
	_, err = svc.RollbackDeployment(orgContext("org-1"), &inferv1.RollbackDeploymentRequest{
		ServiceId: serviceID, EventId: "does-not-exist",
	})
	require.Error(t, err)
	ae, ok = apierrors.As(err)
	require.True(t, ok)
	assert.Equal(t, apierrors.CodeInferEndpointNotFound, ae.Code)

	// Terminated service → 10303.
	repo, err := svc.repository()
	require.NoError(t, err)
	require.NoError(t, repo.MarkTerminated(context.Background(), "org-1", serviceID))
	_, err = svc.RollbackDeployment(orgContext("org-1"), &inferv1.RollbackDeploymentRequest{
		ServiceId: serviceID, EventId: "does-not-exist",
	})
	require.Error(t, err)
	ae, ok = apierrors.As(err)
	require.True(t, ok)
	assert.Equal(t, apierrors.CodeInferServiceStateInvalid, ae.Code)
}

// TestRollbackDeploymentEmptyTarget verifies rolling back to a create
// event (whose before is empty) is rejected (feature #34, AC7).
func TestRollbackDeploymentEmptyTarget(t *testing.T) {
	seedImageRegistry(t)
	svc, _, db := newInferTestService(t)
	modelID := seedModel(t, db, "qwen-3b", "v1")

	resp, err := svc.CreateInferenceService(orgContext("org-1"), &inferv1.CreateInferenceServiceRequest{
		Name: "demo-svc", ModelId: modelID, ModelVersion: "v1",
		ImageId: "img-vllm-nvidia", Replicas: 2, Accelerator: "nvidia",
	})
	require.NoError(t, err)
	serviceID := resp.GetServiceId()

	// The create event's before is empty → not a valid rollback target.
	listResp, err := svc.ListDeploymentEvents(orgContext("org-1"), &inferv1.ListDeploymentEventsRequest{
		ServiceId: serviceID,
	})
	require.NoError(t, err)
	require.Len(t, listResp.GetEvents(), 1)
	createEvent := listResp.GetEvents()[0]
	assert.Equal(t, DeploymentEventCreate, createEvent.GetEventType())

	_, err = svc.RollbackDeployment(orgContext("org-1"), &inferv1.RollbackDeploymentRequest{
		ServiceId: serviceID, EventId: createEvent.GetEventId(),
	})
	require.Error(t, err)
	ae, ok := apierrors.As(err)
	require.True(t, ok)
	assert.Equal(t, apierrors.CodeInferServiceStateInvalid, ae.Code)
}

// TestLifecycleEventRecording verifies create/scale/delete events carry
// the correct before/after diffs (feature #34, AC2).
func TestLifecycleEventRecording(t *testing.T) {
	seedImageRegistry(t)
	svc, _, db := newInferTestService(t)
	modelID := seedModel(t, db, "qwen-3b", "v1")

	// Create → before empty, after full spec.
	resp, err := svc.CreateInferenceService(orgContext("org-1"), &inferv1.CreateInferenceServiceRequest{
		Name: "demo-svc", ModelId: modelID, ModelVersion: "v1",
		ImageId: "img-vllm-nvidia", Replicas: 2, Accelerator: "nvidia",
	})
	require.NoError(t, err)
	serviceID := resp.GetServiceId()

	listResp, err := svc.ListDeploymentEvents(orgContext("org-1"), &inferv1.ListDeploymentEventsRequest{
		ServiceId: serviceID,
	})
	require.NoError(t, err)
	require.Len(t, listResp.GetEvents(), 1)
	createEvt := listResp.GetEvents()[0]
	assert.Equal(t, DeploymentEventCreate, createEvt.GetEventType())
	assert.Equal(t, "{}", createEvt.GetBefore())
	assert.Contains(t, createEvt.GetAfter(), "replicas")

	// Scale → before has old replicas, after has new replicas.
	repo, err := svc.repository()
	require.NoError(t, err)
	require.NoError(t, repo.ApplyStatus(context.Background(), serviceID, StateRunning, []string{"https://infer.example.com/demo/v1"}, nil, nil, nil))
	_, err = svc.ScaleInferenceService(orgContext("org-1"), &inferv1.ScaleInferenceServiceRequest{
		ServiceId: serviceID, Replicas: 4,
	})
	require.NoError(t, err)

	listResp, err = svc.ListDeploymentEvents(orgContext("org-1"), &inferv1.ListDeploymentEventsRequest{
		ServiceId: serviceID,
	})
	require.NoError(t, err)
	require.Len(t, listResp.GetEvents(), 2)
	scaleEvt := listResp.GetEvents()[0]
	assert.Equal(t, DeploymentEventScale, scaleEvt.GetEventType())
	assert.Contains(t, scaleEvt.GetBefore(), "2")
	assert.Contains(t, scaleEvt.GetAfter(), "4")

	// Delete → before full, after empty.
	_, err = svc.DeleteInferenceService(orgContext("org-1"), &inferv1.DeleteInferenceServiceRequest{
		ServiceId: serviceID,
	})
	require.NoError(t, err)

	listResp, err = svc.ListDeploymentEvents(orgContext("org-1"), &inferv1.ListDeploymentEventsRequest{
		ServiceId: serviceID,
	})
	require.NoError(t, err)
	require.Len(t, listResp.GetEvents(), 3)
	deleteEvt := listResp.GetEvents()[0]
	assert.Equal(t, DeploymentEventDelete, deleteEvt.GetEventType())
	assert.Contains(t, deleteEvt.GetBefore(), "replicas")
	assert.Equal(t, "{}", deleteEvt.GetAfter())
}
