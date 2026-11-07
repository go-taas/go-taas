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

	// Stopping again is idempotent: no new event.
	client.published = nil
	_, err = svc.StopInferenceService(orgContext("org-1"), &inferv1.StopInferenceServiceRequest{ServiceId: created.GetServiceId()})
	require.NoError(t, err)
	assert.Empty(t, client.published)

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