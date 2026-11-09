package infer

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	inferv1 "github.com/go-taas/go-taas/proto/taas/infer/v1"
	"github.com/go-taas/go-taas/services/model"
)

// TestServingConvergerPublishesRoutesForRunningServices covers the heartbeat
// that restores a model route deleted out of band: every running service gets
// a route-reconciliation event, and nothing else does.
func TestServingConvergerPublishesRoutesForRunningServices(t *testing.T) {
	db := newInferTestDB(t)
	repo := NewInferenceServiceRepository(db)
	modelID := seedModel(t, db, "qwen-3b", "v1")

	running := seedService(t, repo, "org-1", "live", StateRunning, time.Now())
	running.ModelID = modelID
	require.NoError(t, db.Model(&InferenceService{}).Where("id = ?", running.ID).
		Update("model_id", modelID).Error)
	stopped := seedService(t, repo, "org-1", "parked", StateStopped, time.Now())
	require.NoError(t, db.Model(&InferenceService{}).Where("id = ?", stopped.ID).
		Update("model_id", modelID).Error)

	client := &recordingClient{}
	converger := NewServingConverger(repo, model.NewRepository(db), client, time.Minute)
	converger.runOnce(context.Background())

	require.Len(t, client.published, 1, "only the running service needs its route re-applied")
	var evt changeEvent
	require.NoError(t, json.Unmarshal(client.published[0].Body, &evt))
	assert.Equal(t, EventTypeReconcileRoute, evt.EventType)
	assert.Equal(t, running.ID, evt.ServiceID)
	// The route is named after the model, so the event must carry the catalog
	// name rather than the service name.
	assert.Equal(t, "qwen-3b", evt.Model.Name)
}

// TestServingConvergerWithoutComponentsIsANoOp covers a server started without
// a database or broker: the runner must not panic, and must not publish.
func TestServingConvergerWithoutComponentsIsANoOp(t *testing.T) {
	client := &recordingClient{}
	NewServingConverger(nil, nil, client, time.Minute).runOnce(context.Background())
	NewServingConverger(&InferenceServiceRepository{}, nil, nil, time.Minute).runOnce(context.Background())
	assert.Empty(t, client.published)
}

// TestNewServingConvergerFromConfigRequiresComponents keeps the wiring honest:
// the runner is only built when it has a database and a broker to work with.
func TestNewServingConvergerFromConfigRequiresComponents(t *testing.T) {
	assert.Nil(t, NewServingConvergerFromConfig(nil))
	assert.Nil(t, NewServingConvergerFromConfig(&fakeComponents{}))
}

// TestStopRepublishesSiblingModelRoute is the regression test for the outage
// this fixes: two services serve one model and therefore share one
// model-scoped HTTPRoute. Stopping one of them must re-point the route at the
// survivor *before* the teardown runs, or the survivor answers 503
// (`httproutes ... not found`) until an operator touches it.
func TestStopRepublishesSiblingModelRoute(t *testing.T) {
	seedImageRegistry(t)
	svc, client, db := newInferTestService(t)
	modelID := seedModel(t, db, "qwen-3b", "v1")

	create := func(name string) string {
		created, err := svc.CreateInferenceService(orgContext("org-1"), &inferv1.CreateInferenceServiceRequest{
			Name: name, ModelId: modelID, ModelVersion: "v1",
			ImageId: "img-vllm-nvidia", Replicas: 1, Accelerator: "nvidia",
		})
		require.NoError(t, err)
		// Both are serving: the platform can run two services of one model
		// (a canary next to the primary, for instance).
		require.NoError(t, db.Model(&InferenceService{}).Where("id = ?", created.GetServiceId()).
			Update("state", StateRunning).Error)
		return created.GetServiceId()
	}
	going := create("qwen-primary")
	survivor := create("qwen-canary")

	client.published = nil
	_, err := svc.StopInferenceService(orgContext("org-1"), &inferv1.StopInferenceServiceRequest{ServiceId: going})
	require.NoError(t, err)

	require.Len(t, client.published, 2, "the survivor's route is re-pointed before the teardown")
	var first, second changeEvent
	require.NoError(t, json.Unmarshal(client.published[0].Body, &first))
	require.NoError(t, json.Unmarshal(client.published[1].Body, &second))
	assert.Equal(t, EventTypeReconcileRoute, first.EventType)
	assert.Equal(t, survivor, first.ServiceID)
	assert.Equal(t, "qwen-3b", first.Model.Name)
	assert.Equal(t, EventTypeStop, second.EventType)
	assert.Equal(t, going, second.ServiceID)
}

// TestDeleteRepublishesSiblingModelRoute is the same guarantee on the delete
// path, including the idempotent re-publish for an already terminated row.
func TestDeleteRepublishesSiblingModelRoute(t *testing.T) {
	seedImageRegistry(t)
	svc, client, db := newInferTestService(t)
	modelID := seedModel(t, db, "qwen-3b", "v1")

	create := func(name string) string {
		created, err := svc.CreateInferenceService(orgContext("org-1"), &inferv1.CreateInferenceServiceRequest{
			Name: name, ModelId: modelID, ModelVersion: "v1",
			ImageId: "img-vllm-nvidia", Replicas: 1, Accelerator: "nvidia",
		})
		require.NoError(t, err)
		return created.GetServiceId()
	}
	going := create("qwen-primary")
	survivor := create("qwen-canary")
	require.NoError(t, db.Model(&InferenceService{}).Where("id = ?", survivor).
		Update("state", StateRunning).Error)

	client.published = nil
	_, err := svc.DeleteInferenceService(orgContext("org-1"), &inferv1.DeleteInferenceServiceRequest{ServiceId: going})
	require.NoError(t, err)

	require.Len(t, client.published, 2)
	var first changeEvent
	require.NoError(t, json.Unmarshal(client.published[0].Body, &first))
	assert.Equal(t, EventTypeReconcileRoute, first.EventType)
	assert.Equal(t, survivor, first.ServiceID)

	// A second delete is idempotent and still re-points the survivor: the
	// teardown converger replays this path, so it must not undo the failover.
	client.published = nil
	_, err = svc.DeleteInferenceService(orgContext("org-1"), &inferv1.DeleteInferenceServiceRequest{ServiceId: going})
	require.NoError(t, err)
	require.Len(t, client.published, 2)
	require.NoError(t, json.Unmarshal(client.published[0].Body, &first))
	assert.Equal(t, EventTypeReconcileRoute, first.EventType)
}

// TestStopWithoutSiblingKeepsOneEvent guards the common case: a single
// service of its model publishes only the teardown.
func TestStopWithoutSiblingKeepsOneEvent(t *testing.T) {
	seedImageRegistry(t)
	svc, client, db := newInferTestService(t)
	modelID := seedModel(t, db, "qwen-3b", "v1")

	created, err := svc.CreateInferenceService(orgContext("org-1"), &inferv1.CreateInferenceServiceRequest{
		Name: "solo", ModelId: modelID, ModelVersion: "v1",
		ImageId: "img-vllm-nvidia", Replicas: 1, Accelerator: "nvidia",
	})
	require.NoError(t, err)

	client.published = nil
	_, err = svc.StopInferenceService(orgContext("org-1"), &inferv1.StopInferenceServiceRequest{ServiceId: created.GetServiceId()})
	require.NoError(t, err)

	require.Len(t, client.published, 1)
	var evt changeEvent
	require.NoError(t, json.Unmarshal(client.published[0].Body, &evt))
	assert.Equal(t, EventTypeStop, evt.EventType)
}

// TestTeardownConvergerCarriesTheModelName covers an older gap the route fix
// exposed: the converger used to publish an empty model name, so the
// controller looked for a route named after the service and never found the
// model's real one. Its replay must now name the model and re-point the route
// at a running sibling before it tears anything down.
func TestTeardownConvergerCarriesTheModelName(t *testing.T) {
	db := newInferTestDB(t)
	repo := NewInferenceServiceRepository(db)
	modelID := seedModel(t, db, "qwen-3b", "v1")

	stopped := seedService(t, repo, "org-1", "parked", StateStopped, time.Now())
	require.NoError(t, db.Model(&InferenceService{}).Where("id = ?", stopped.ID).
		Update("model_id", modelID).Error)
	survivor := seedService(t, repo, "org-1", "live", StateRunning, time.Now())
	require.NoError(t, db.Model(&InferenceService{}).Where("id = ?", survivor.ID).
		Update("model_id", modelID).Error)

	client := &recordingClient{}
	converger := NewTeardownConverger(repo, model.NewRepository(db), client, time.Minute)
	converger.runOnce(context.Background())

	require.Len(t, client.published, 2, "the survivor's route is re-pointed, then the teardown replays")
	var first, second changeEvent
	require.NoError(t, json.Unmarshal(client.published[0].Body, &first))
	require.NoError(t, json.Unmarshal(client.published[1].Body, &second))
	assert.Equal(t, EventTypeReconcileRoute, first.EventType)
	assert.Equal(t, survivor.ID, first.ServiceID)
	assert.Equal(t, EventTypeStop, second.EventType)
	assert.Equal(t, "qwen-3b", second.Model.Name, "the teardown must name the model, not the service")
}
