package infer

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	inferv1 "github.com/go-taas/go-taas/proto/taas/infer/v1"

	apierrors "github.com/go-taas/go-taas/pkg/errors"
)

func TestStopAndStartInferenceService(t *testing.T) {
	seedImageRegistry(t)
	svc, client, db := newInferTestService(t)
	modelID := seedModel(t, db, "qwen-3b", "v1")

	created, err := svc.CreateInferenceService(orgContext("org-1"), &inferv1.CreateInferenceServiceRequest{
		Name: "demo", ModelId: modelID, ModelVersion: "v1",
		ImageId: "img-vllm-nvidia", Replicas: 1, Accelerator: "nvidia",
	})
	require.NoError(t, err)
	client.published = nil

	// Stop: one stop event, state becomes stopped.
	resp, err := svc.StopInferenceService(orgContext("org-1"), &inferv1.StopInferenceServiceRequest{ServiceId: created.GetServiceId()})
	require.NoError(t, err)
	assert.Equal(t, StateStopped, resp.GetState())
	require.Len(t, client.published, 1)
	var evt changeEvent
	require.NoError(t, json.Unmarshal(client.published[0].Body, &evt))
	assert.Equal(t, EventTypeStop, evt.EventType)

	get, err := svc.GetInferenceService(orgContext("org-1"), &inferv1.GetInferenceServiceRequest{ServiceId: created.GetServiceId()})
	require.NoError(t, err)
	assert.Equal(t, StateStopped, get.GetService().GetState())

	// Stopping again is idempotent and re-publishes the teardown
	// (best-effort) so a lost event still converges the k8s resources.
	client.published = nil
	_, err = svc.StopInferenceService(orgContext("org-1"), &inferv1.StopInferenceServiceRequest{ServiceId: created.GetServiceId()})
	require.NoError(t, err)
	require.Len(t, client.published, 1)
	require.NoError(t, json.Unmarshal(client.published[0].Body, &evt))
	assert.Equal(t, EventTypeStop, evt.EventType)

	// Start: one start event, state becomes pending.
	client.published = nil
	resp2, err := svc.StartInferenceService(orgContext("org-1"), &inferv1.StartInferenceServiceRequest{ServiceId: created.GetServiceId()})
	require.NoError(t, err)
	assert.Equal(t, StatePending, resp2.GetState())
	require.Len(t, client.published, 1)
	require.NoError(t, json.Unmarshal(client.published[0].Body, &evt))
	assert.Equal(t, EventTypeStart, evt.EventType)

	get, err = svc.GetInferenceService(orgContext("org-1"), &inferv1.GetInferenceServiceRequest{ServiceId: created.GetServiceId()})
	require.NoError(t, err)
	assert.Equal(t, StatePending, get.GetService().GetState())

	// Starting a running service is idempotent.
	client.published = nil
	_, err = svc.StartInferenceService(orgContext("org-1"), &inferv1.StartInferenceServiceRequest{ServiceId: created.GetServiceId()})
	require.NoError(t, err)
	assert.Empty(t, client.published)
}

func TestStopInferenceServiceTerminatedRejected(t *testing.T) {
	seedImageRegistry(t)
	svc, _, db := newInferTestService(t)
	modelID := seedModel(t, db, "qwen-3b", "v1")

	created, err := svc.CreateInferenceService(orgContext("org-1"), &inferv1.CreateInferenceServiceRequest{
		Name: "demo", ModelId: modelID, ModelVersion: "v1",
		ImageId: "img-vllm-nvidia", Replicas: 1, Accelerator: "nvidia",
	})
	require.NoError(t, err)

	_, err = svc.DeleteInferenceService(orgContext("org-1"), &inferv1.DeleteInferenceServiceRequest{ServiceId: created.GetServiceId()})
	require.NoError(t, err)

	// Stopping a terminated service is rejected.
	_, err = svc.StopInferenceService(orgContext("org-1"), &inferv1.StopInferenceServiceRequest{ServiceId: created.GetServiceId()})
	require.Error(t, err)
	ae, ok := apierrors.As(err)
	require.True(t, ok)
	assert.Equal(t, apierrors.CodeInferServiceStateInvalid, ae.Code)

	// Starting a terminated service is rejected.
	_, err = svc.StartInferenceService(orgContext("org-1"), &inferv1.StartInferenceServiceRequest{ServiceId: created.GetServiceId()})
	require.Error(t, err)
	ae, ok = apierrors.As(err)
	require.True(t, ok)
	assert.Equal(t, apierrors.CodeInferServiceStateInvalid, ae.Code)
}

func TestStopInferenceServiceUnknown(t *testing.T) {
	seedImageRegistry(t)
	svc, _, _ := newInferTestService(t)

	_, err := svc.StopInferenceService(orgContext("org-1"), &inferv1.StopInferenceServiceRequest{ServiceId: "no-such"})
	require.Error(t, err)
	ae, ok := apierrors.As(err)
	require.True(t, ok)
	assert.Equal(t, apierrors.CodeInferServiceNotFound, ae.Code)

	_, err = svc.StartInferenceService(orgContext("org-1"), &inferv1.StartInferenceServiceRequest{ServiceId: "no-such"})
	require.Error(t, err)
	ae, ok = apierrors.As(err)
	require.True(t, ok)
	assert.Equal(t, apierrors.CodeInferServiceNotFound, ae.Code)
}

var _ = context.Background

func TestUpdateEngineArgsOnStoppedService(t *testing.T) {
	seedImageRegistry(t)
	svc, client, db := newInferTestService(t)
	modelID := seedModel(t, db, "qwen-3b", "v1")

	created, err := svc.CreateInferenceService(orgContext("org-1"), &inferv1.CreateInferenceServiceRequest{
		Name: "demo", ModelId: modelID, ModelVersion: "v1",
		ImageId: "img-vllm-nvidia", Replicas: 1, Accelerator: "nvidia",
		EngineArgs: map[string]string{"max-model-len": "4096"},
	})
	require.NoError(t, err)

	// Stop the service.
	_, err = svc.StopInferenceService(orgContext("org-1"), &inferv1.StopInferenceServiceRequest{ServiceId: created.GetServiceId()})
	require.NoError(t, err)
	client.published = nil

	// Update engine args while stopped: the state stays stopped.
	resp, err := svc.UpdateInferenceServiceEngineArgs(orgContext("org-1"), &inferv1.UpdateInferenceServiceEngineArgsRequest{
		ServiceId:  created.GetServiceId(),
		EngineArgs: map[string]string{"max-model-len": "8192", "enable-prefix-caching": "flag:true"},
	})
	require.NoError(t, err)
	assert.Equal(t, StateStopped, resp.GetState())

	// The stored args are updated.
	get, err := svc.GetInferenceService(orgContext("org-1"), &inferv1.GetInferenceServiceRequest{ServiceId: created.GetServiceId()})
	require.NoError(t, err)
	assert.Equal(t, "8192", get.GetEngineArgs()["max-model-len"])
	assert.Equal(t, "flag:true", get.GetEngineArgs()["enable-prefix-caching"])
	assert.Equal(t, StateStopped, get.GetService().GetState())

	// An update_engine_args event was published.
	require.Len(t, client.published, 1)
	var evt changeEvent
	require.NoError(t, json.Unmarshal(client.published[0].Body, &evt))
	assert.Equal(t, EventTypeUpdateEngineArgs, evt.EventType)
	assert.Equal(t, "8192", evt.EngineArgs["max-model-len"])
}

func TestUpdateEngineArgsHotUpdate(t *testing.T) {
	seedImageRegistry(t)
	svc, client, db := newInferTestService(t)
	modelID := seedModel(t, db, "qwen-3b", "v1")

	created, err := svc.CreateInferenceService(orgContext("org-1"), &inferv1.CreateInferenceServiceRequest{
		Name: "demo", ModelId: modelID, ModelVersion: "v1",
		ImageId: "img-vllm-nvidia", Replicas: 1, Accelerator: "nvidia",
	})
	require.NoError(t, err)

	// Simulate the service reaching running.
	repo := NewInferenceServiceRepository(db)
	require.NoError(t, repo.ApplyStatus(context.Background(), created.GetServiceId(), StateRunning, []string{"http://x"}, nil, nil, nil))
	client.published = nil

	// Hot update: the state transitions to pending so the controller
	// recreates the pods.
	resp, err := svc.UpdateInferenceServiceEngineArgs(orgContext("org-1"), &inferv1.UpdateInferenceServiceEngineArgsRequest{
		ServiceId:  created.GetServiceId(),
		EngineArgs: map[string]string{"max-model-len": "16384"},
	})
	require.NoError(t, err)
	assert.Equal(t, StatePending, resp.GetState())

	get, err := svc.GetInferenceService(orgContext("org-1"), &inferv1.GetInferenceServiceRequest{ServiceId: created.GetServiceId()})
	require.NoError(t, err)
	assert.Equal(t, "16384", get.GetEngineArgs()["max-model-len"])
	assert.Equal(t, StatePending, get.GetService().GetState())

	require.Len(t, client.published, 1)
	var evt changeEvent
	require.NoError(t, json.Unmarshal(client.published[0].Body, &evt))
	assert.Equal(t, EventTypeUpdateEngineArgs, evt.EventType)
}

func TestUpdateEngineArgsValidation(t *testing.T) {
	seedImageRegistry(t)
	svc, _, db := newInferTestService(t)
	modelID := seedModel(t, db, "qwen-3b", "v1")

	created, err := svc.CreateInferenceService(orgContext("org-1"), &inferv1.CreateInferenceServiceRequest{
		Name: "demo", ModelId: modelID, ModelVersion: "v1",
		ImageId: "img-vllm-nvidia", Replicas: 1, Accelerator: "nvidia",
	})
	require.NoError(t, err)

	// Invalid engine arg name.
	_, err = svc.UpdateInferenceServiceEngineArgs(orgContext("org-1"), &inferv1.UpdateInferenceServiceEngineArgsRequest{
		ServiceId:  created.GetServiceId(),
		EngineArgs: map[string]string{"Bad Arg": "x"},
	})
	require.Error(t, err)
	ae, ok := apierrors.As(err)
	require.True(t, ok)
	assert.Equal(t, apierrors.CodeInferEngineArgsInvalid, ae.Code)

	// Unknown service.
	_, err = svc.UpdateInferenceServiceEngineArgs(orgContext("org-1"), &inferv1.UpdateInferenceServiceEngineArgsRequest{
		ServiceId: "no-such",
	})
	require.Error(t, err)
	ae, ok = apierrors.As(err)
	require.True(t, ok)
	assert.Equal(t, apierrors.CodeInferServiceNotFound, ae.Code)
}