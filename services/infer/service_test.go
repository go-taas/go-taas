package infer

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/metadata"

	inferv1 "github.com/go-taas/go-taas/proto/taas/infer/v1"

	"github.com/go-taas/go-taas/pkg/mq"
	"github.com/go-taas/go-taas/services/image"

	apierrors "github.com/go-taas/go-taas/pkg/errors"
)

// orgContext builds a context carrying the organization metadata.
func orgContext(org string) context.Context {
	return metadata.NewIncomingContext(context.Background(),
		metadata.Pairs("x-organization-id", org))
}

func seedImageRegistry(t *testing.T) {
	t.Helper()
	restore := image.ResetForTest([]*image.Summary{
		{ImageID: "img-vllm-nvidia", Name: "ghcr.io/go-taas/vllm", Tag: "v0.6.3", Accelerator: "nvidia", Engine: "vllm"},
		{ImageID: "img-vllm-iluvatar", Name: "ghcr.io/go-taas/vllm-iluvatar", Tag: "v0.6.3", Accelerator: "iluvatar", Engine: "vllm"},
	})
	t.Cleanup(restore)
}

func TestCreateInferenceServiceHappyPath(t *testing.T) {
	seedImageRegistry(t)
	svc, client, db := newInferTestService(t)
	modelID := seedModel(t, db, "qwen-3b", "v1")

	resp, err := svc.CreateInferenceService(orgContext("org-1"), &inferv1.CreateInferenceServiceRequest{
		Name:          "demo-svc",
		ModelId:       modelID,
		ModelVersion: "v1",
		ImageId:       "img-vllm-nvidia",
		Replicas:      2,
		Accelerator:   "nvidia",
	})
	require.NoError(t, err)
	assert.NotEmpty(t, resp.GetServiceId())

	// One upsert event published with the resolved spec.
	require.Len(t, client.published, 1)
	var evt changeEvent
	require.NoError(t, json.Unmarshal(client.published[0].Body, &evt))
	assert.Equal(t, EventTypeUpsert, evt.EventType)
	assert.Equal(t, resp.GetServiceId(), evt.ServiceID)
	assert.Equal(t, "demo-svc", evt.Name)
	assert.Equal(t, "qwen-3b/v1", evt.Model.WeightPath)
	assert.Equal(t, "ghcr.io/go-taas/vllm:v0.6.3", evt.Image.Reference)
	assert.Equal(t, 2, evt.Replicas)

	// The row is pending with empty endpoints.
	row, err := svc.repository()
	require.NoError(t, err)
	stored, err := row.FindByIDAndOrganization(context.Background(), "org-1", resp.GetServiceId())
	require.NoError(t, err)
	assert.Equal(t, StatePending, stored.State)
}

func TestCreateInferenceServiceValidationMatrix(t *testing.T) {
	seedImageRegistry(t)
	svc, client, db := newInferTestService(t)
	modelID := seedModel(t, db, "qwen-3b", "v1")

	cases := []struct {
		name string
		req  *inferv1.CreateInferenceServiceRequest
		code apierrors.Code
	}{
		{
			"invalid name",
			&inferv1.CreateInferenceServiceRequest{Name: "Bad_Name", ModelId: modelID, ModelVersion: "v1", ImageId: "img-vllm-nvidia", Replicas: 1, Accelerator: "nvidia"},
			apierrors.CodeInferServiceStateInvalid,
		},
		{
			"name too long",
			&inferv1.CreateInferenceServiceRequest{Name: "a-very-long-service-name-exceeding-the-sixty-three-character-limit-for-dns", ModelId: modelID, ModelVersion: "v1", ImageId: "img-vllm-nvidia", Replicas: 1, Accelerator: "nvidia"},
			apierrors.CodeInferServiceStateInvalid,
		},
		{
			"replicas zero",
			&inferv1.CreateInferenceServiceRequest{Name: "demo", ModelId: modelID, ModelVersion: "v1", ImageId: "img-vllm-nvidia", Replicas: 0, Accelerator: "nvidia"},
			apierrors.CodeInferReplicasInvalid,
		},
		{
			"replicas too many",
			&inferv1.CreateInferenceServiceRequest{Name: "demo", ModelId: modelID, ModelVersion: "v1", ImageId: "img-vllm-nvidia", Replicas: 101, Accelerator: "nvidia"},
			apierrors.CodeInferReplicasInvalid,
		},
		{
			"unsupported accelerator",
			&inferv1.CreateInferenceServiceRequest{Name: "demo", ModelId: modelID, ModelVersion: "v1", ImageId: "img-vllm-nvidia", Replicas: 1, Accelerator: "tpu"},
			apierrors.CodeInferEngineUnsupported,
		},
		{
			"unknown model",
			&inferv1.CreateInferenceServiceRequest{Name: "demo", ModelId: "missing", ModelVersion: "v1", ImageId: "img-vllm-nvidia", Replicas: 1, Accelerator: "nvidia"},
			apierrors.CodeModelNotFound,
		},
		{
			"unknown model version",
			&inferv1.CreateInferenceServiceRequest{Name: "demo", ModelId: modelID, ModelVersion: "v9", ImageId: "img-vllm-nvidia", Replicas: 1, Accelerator: "nvidia"},
			apierrors.CodeModelVersionNotFound,
		},
		{
			"unknown image",
			&inferv1.CreateInferenceServiceRequest{Name: "demo", ModelId: modelID, ModelVersion: "v1", ImageId: "img-missing", Replicas: 1, Accelerator: "nvidia"},
			apierrors.CodeImageNotFound,
		},
		{
			"accelerator mismatch",
			&inferv1.CreateInferenceServiceRequest{Name: "demo", ModelId: modelID, ModelVersion: "v1", ImageId: "img-vllm-nvidia", Replicas: 1, Accelerator: "iluvatar"},
			apierrors.CodeImageIncompatible,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := svc.CreateInferenceService(orgContext("org-1"), tc.req)
			require.Error(t, err)
			ae, ok := apierrors.As(err)
			require.True(t, ok, "expected API error, got %v", err)
			assert.Equal(t, tc.code, ae.Code)
		})
	}

	// Nothing was published for any failed validation (AC5).
	assert.Empty(t, client.published)
}

func TestCreateInferenceServiceDuplicate(t *testing.T) {
	seedImageRegistry(t)
	svc, _, db := newInferTestService(t)
	modelID := seedModel(t, db, "qwen-3b", "v1")

	req := &inferv1.CreateInferenceServiceRequest{
		Name: "demo", ModelId: modelID, ModelVersion: "v1",
		ImageId: "img-vllm-nvidia", Replicas: 1, Accelerator: "nvidia",
	}
	_, err := svc.CreateInferenceService(orgContext("org-1"), req)
	require.NoError(t, err)

	_, err = svc.CreateInferenceService(orgContext("org-1"), req)
	require.Error(t, err)
	ae, ok := apierrors.As(err)
	require.True(t, ok)
	assert.Equal(t, apierrors.CodeInferServiceExists, ae.Code)

	// The same name in another organization is fine.
	_, err = svc.CreateInferenceService(orgContext("org-2"), req)
	require.NoError(t, err)
}

func TestCreateInferenceServiceRequiresOrganization(t *testing.T) {
	seedImageRegistry(t)
	svc, _, _ := newInferTestService(t)

	_, err := svc.CreateInferenceService(context.Background(), &inferv1.CreateInferenceServiceRequest{})
	require.Error(t, err)
	ae, ok := apierrors.As(err)
	require.True(t, ok)
	assert.Equal(t, apierrors.CodeUnauthorized, ae.Code)
}

func TestListAndGetInferenceServices(t *testing.T) {
	seedImageRegistry(t)
	svc, _, db := newInferTestService(t)
	modelID := seedModel(t, db, "qwen-3b", "v1")

	create := func(org, name string) string {
		resp, err := svc.CreateInferenceService(orgContext(org), &inferv1.CreateInferenceServiceRequest{
			Name: name, ModelId: modelID, ModelVersion: "v1",
			ImageId: "img-vllm-nvidia", Replicas: 1, Accelerator: "nvidia",
		})
		require.NoError(t, err)
		return resp.GetServiceId()
	}
	id1 := create("org-1", "svc-one")
	create("org-1", "svc-two")
	create("org-2", "other-org-svc")

	// Org-scoped list.
	resp, err := svc.ListInferenceServices(orgContext("org-1"), &inferv1.ListInferenceServicesRequest{})
	require.NoError(t, err)
	assert.Len(t, resp.GetServices(), 2)
	assert.Equal(t, int64(2), resp.GetPageMeta().GetTotal())

	// Get: endpoints hidden while pending.
	get, err := svc.GetInferenceService(orgContext("org-1"), &inferv1.GetInferenceServiceRequest{ServiceId: id1})
	require.NoError(t, err)
	assert.Equal(t, StatePending, get.GetService().GetState())
	assert.Empty(t, get.GetEndpoints())

	// Drive to running via the status consumer path: endpoints appear.
	repo, err := svc.repository()
	require.NoError(t, err)
	require.NoError(t, repo.ApplyStatus(context.Background(), id1, StateRunning, []string{"http://demo/v1"}, nil))
	get, err = svc.GetInferenceService(orgContext("org-1"), &inferv1.GetInferenceServiceRequest{ServiceId: id1})
	require.NoError(t, err)
	assert.Equal(t, StateRunning, get.GetService().GetState())
	assert.Equal(t, []string{"http://demo/v1"}, get.GetEndpoints())

	// Cross-org get: not found.
	_, err = svc.GetInferenceService(orgContext("org-2"), &inferv1.GetInferenceServiceRequest{ServiceId: id1})
	require.Error(t, err)
	ae, ok := apierrors.As(err)
	require.True(t, ok)
	assert.Equal(t, apierrors.CodeInferServiceNotFound, ae.Code)
}

func TestScaleInferenceService(t *testing.T) {
	seedImageRegistry(t)
	svc, client, db := newInferTestService(t)
	modelID := seedModel(t, db, "qwen-3b", "v1")

	created, err := svc.CreateInferenceService(orgContext("org-1"), &inferv1.CreateInferenceServiceRequest{
		Name: "demo", ModelId: modelID, ModelVersion: "v1",
		ImageId: "img-vllm-nvidia", Replicas: 1, Accelerator: "nvidia",
	})
	require.NoError(t, err)
	client.published = nil

	// Scale up.
	_, err = svc.ScaleInferenceService(orgContext("org-1"), &inferv1.ScaleInferenceServiceRequest{
		ServiceId: created.GetServiceId(), Replicas: 3,
	})
	require.NoError(t, err)

	// One upsert event with the new replica count.
	require.Len(t, client.published, 1)
	var evt changeEvent
	require.NoError(t, json.Unmarshal(client.published[0].Body, &evt))
	assert.Equal(t, 3, evt.Replicas)

	// The state is untouched (AC8).
	get, err := svc.GetInferenceService(orgContext("org-1"), &inferv1.GetInferenceServiceRequest{ServiceId: created.GetServiceId()})
	require.NoError(t, err)
	assert.Equal(t, StatePending, get.GetService().GetState())
	assert.Equal(t, int32(3), get.GetService().GetReplicas())

	// Invalid replicas.
	_, err = svc.ScaleInferenceService(orgContext("org-1"), &inferv1.ScaleInferenceServiceRequest{
		ServiceId: created.GetServiceId(), Replicas: 0,
	})
	require.Error(t, err)
	ae, ok := apierrors.As(err)
	require.True(t, ok)
	assert.Equal(t, apierrors.CodeInferReplicasInvalid, ae.Code)
}

func TestDeleteInferenceServiceIdempotent(t *testing.T) {
	seedImageRegistry(t)
	svc, client, db := newInferTestService(t)
	modelID := seedModel(t, db, "qwen-3b", "v1")

	created, err := svc.CreateInferenceService(orgContext("org-1"), &inferv1.CreateInferenceServiceRequest{
		Name: "demo", ModelId: modelID, ModelVersion: "v1",
		ImageId: "img-vllm-nvidia", Replicas: 1, Accelerator: "nvidia",
	})
	require.NoError(t, err)
	client.published = nil

	// First delete: one delete event.
	_, err = svc.DeleteInferenceService(orgContext("org-1"), &inferv1.DeleteInferenceServiceRequest{ServiceId: created.GetServiceId()})
	require.NoError(t, err)
	require.Len(t, client.published, 1)
	var evt changeEvent
	require.NoError(t, json.Unmarshal(client.published[0].Body, &evt))
	assert.Equal(t, EventTypeDelete, evt.EventType)

	// Second delete: success, no new event (AC9).
	_, err = svc.DeleteInferenceService(orgContext("org-1"), &inferv1.DeleteInferenceServiceRequest{ServiceId: created.GetServiceId()})
	require.NoError(t, err)
	assert.Len(t, client.published, 1)

	// Terminated services are hidden from the list.
	list, err := svc.ListInferenceServices(orgContext("org-1"), &inferv1.ListInferenceServicesRequest{})
	require.NoError(t, err)
	assert.Empty(t, list.GetServices())

	// Scaling a terminated service is rejected.
	_, err = svc.ScaleInferenceService(orgContext("org-1"), &inferv1.ScaleInferenceServiceRequest{
		ServiceId: created.GetServiceId(), Replicas: 2,
	})
	require.Error(t, err)
	ae, ok := apierrors.As(err)
	require.True(t, ok)
	assert.Equal(t, apierrors.CodeInferServiceStateInvalid, ae.Code)
}

func TestStatusConsumerHandle(t *testing.T) {
	db := newInferTestDB(t)
	repo := NewInferenceServiceRepository(db)
	ctx := context.Background()

	svc := seedService(t, repo, "org", "svc", StateDeploying, time.Now().UTC())
	consumer := NewStatusConsumer(mq.NewFake(), repo, 1)

	// Valid report applies.
	body, err := json.Marshal(statusReport{
		ServiceID: svc.ID, State: StateRunning, Endpoints: []string{"http://e"},
	})
	require.NoError(t, err)
	require.NoError(t, consumer.handle(ctx, mq.Message{Body: body}))
	row, err := repo.FindByIDAndOrganization(ctx, "org", svc.ID)
	require.NoError(t, err)
	assert.Equal(t, StateRunning, row.State)

	// Malformed JSON: skipped (nil), not an error.
	require.NoError(t, consumer.handle(ctx, mq.Message{Body: []byte("{not json")}))

	// Unknown service: skipped (nil).
	body, _ = json.Marshal(statusReport{ServiceID: "missing", State: StateRunning})
	require.NoError(t, consumer.handle(ctx, mq.Message{Body: body}))

	// Invalid state (pending/terminated/garbage): skipped.
	body, _ = json.Marshal(statusReport{ServiceID: svc.ID, State: "garbage"})
	require.NoError(t, consumer.handle(ctx, mq.Message{Body: body}))
	row, err = repo.FindByIDAndOrganization(ctx, "org", svc.ID)
	require.NoError(t, err)
	assert.Equal(t, StateRunning, row.State)
}
