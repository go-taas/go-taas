package infer

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	apierrors "github.com/go-taas/go-taas/pkg/errors"
	inferv1 "github.com/go-taas/go-taas/proto/taas/infer/v1"
)

// createTopologyService creates one service with the given topology and
// returns its id.
func createTopologyService(t *testing.T, svc *Service, name string, req *inferv1.CreateInferenceServiceRequest) string {
	t.Helper()
	req.Name = name
	resp, err := svc.CreateInferenceService(orgContext("org-1"), req)
	require.NoError(t, err)
	return resp.GetServiceId()
}

func TestCreateInferenceServiceRecordsDistributedTopology(t *testing.T) {
	seedImageRegistry(t)
	svc, client, db := newInferTestService(t)
	modelID := seedModel(t, db, "qwen-3b", "v1")

	id := createTopologyService(t, svc, "dist-svc", &inferv1.CreateInferenceServiceRequest{
		ModelId: modelID, ModelVersion: "v1", ImageId: "img-vllm-nvidia",
		Replicas: 2, Accelerator: "nvidia",
		Topology:             TopologyDistributed,
		TensorParallelSize:   4,
		PipelineParallelSize: 2,
	})

	repo, err := svc.repository()
	require.NoError(t, err)
	stored, err := repo.FindByIDAndOrganization(t.Context(), "org-1", id)
	require.NoError(t, err)
	assert.Equal(t, TopologyDistributed, stored.Topology)
	assert.Equal(t, 4, stored.TensorParallelSize)
	assert.Equal(t, 2, stored.PipelineParallelSize)

	// The change event carries the layout, so the controller builds the right
	// storm roles without a database read.
	require.Len(t, client.published, 1)
	var evt changeEvent
	require.NoError(t, json.Unmarshal(client.published[0].Body, &evt))
	assert.Equal(t, TopologyDistributed, evt.Topology)
	assert.Equal(t, 4, evt.TensorParallelSize)
	assert.Equal(t, 2, evt.PipelineParallelSize)
	assert.Zero(t, evt.PrefillReplicas)
}

func TestCreateInferenceServiceRecordsPDTopology(t *testing.T) {
	seedImageRegistry(t)
	svc, client, db := newInferTestService(t)
	modelID := seedModel(t, db, "qwen-3b", "v1")

	id := createTopologyService(t, svc, "pd-svc", &inferv1.CreateInferenceServiceRequest{
		ModelId: modelID, ModelVersion: "v1", ImageId: "img-vllm-nvidia",
		Replicas: 1, Accelerator: "nvidia",
		Topology:        TopologyPD,
		PrefillReplicas: 2,
		DecodeReplicas:  3,
	})

	repo, err := svc.repository()
	require.NoError(t, err)
	stored, err := repo.FindByIDAndOrganization(t.Context(), "org-1", id)
	require.NoError(t, err)
	assert.Equal(t, TopologyPD, stored.Topology)
	assert.Equal(t, 2, stored.PrefillReplicas)
	assert.Equal(t, 3, stored.DecodeReplicas)

	var evt changeEvent
	require.NoError(t, json.Unmarshal(client.published[0].Body, &evt))
	assert.Equal(t, TopologyPD, evt.Topology)
	assert.Equal(t, 2, evt.PrefillReplicas)
	assert.Equal(t, 3, evt.DecodeReplicas)
}

func TestCreateInferenceServiceRejectsInvalidTopology(t *testing.T) {
	seedImageRegistry(t)
	svc, client, db := newInferTestService(t)
	modelID := seedModel(t, db, "qwen-3b", "v1")

	// Distributed with a single pod is not distributed.
	_, err := svc.CreateInferenceService(orgContext("org-1"), &inferv1.CreateInferenceServiceRequest{
		Name: "bad-dist", ModelId: modelID, ModelVersion: "v1", ImageId: "img-vllm-nvidia",
		Replicas: 1, Accelerator: "nvidia",
		Topology:           TopologyDistributed,
		TensorParallelSize: 1,
	})
	require.Error(t, err)
	assert.Equal(t, apierrors.CodeInferTopologyInvalid, apierrors.CodeOf(err))
	assert.Empty(t, client.published, "an invalid topology publishes nothing")
}

func TestUpdateInferenceServiceTopology(t *testing.T) {
	seedImageRegistry(t)
	svc, client, db := newInferTestService(t)
	modelID := seedModel(t, db, "qwen-3b", "v1")

	id := createTopologyService(t, svc, "reshape-svc", &inferv1.CreateInferenceServiceRequest{
		ModelId: modelID, ModelVersion: "v1", ImageId: "img-vllm-nvidia",
		Replicas: 1, Accelerator: "nvidia",
	})
	client.published = nil

	resp, err := svc.UpdateInferenceServiceTopology(orgContext("org-1"), &inferv1.UpdateInferenceServiceTopologyRequest{
		ServiceId:            id,
		Topology:             TopologyDistributed,
		TensorParallelSize:   2,
		PipelineParallelSize: 2,
		Replicas:             3,
	})
	require.NoError(t, err)
	assert.Equal(t, id, resp.GetServiceId())
	assert.Equal(t, StatePending, resp.GetState())

	repo, err := svc.repository()
	require.NoError(t, err)
	stored, err := repo.FindByIDAndOrganization(t.Context(), "org-1", id)
	require.NoError(t, err)
	assert.Equal(t, TopologyDistributed, stored.Topology)
	assert.Equal(t, 2, stored.TensorParallelSize)
	assert.Equal(t, 2, stored.PipelineParallelSize)
	assert.Equal(t, 3, stored.Replicas)
	assert.Equal(t, StatePending, stored.State)

	// A spec change republishes the desired state; the controller rolls the
	// storm out with the new roles.
	require.Len(t, client.published, 1)
	var evt changeEvent
	require.NoError(t, json.Unmarshal(client.published[0].Body, &evt))
	assert.Equal(t, EventTypeUpsert, evt.EventType)
	assert.Equal(t, TopologyDistributed, evt.Topology)
	assert.Equal(t, 2, evt.TensorParallelSize)
	assert.Equal(t, 3, evt.Replicas)
	require.NotNil(t, evt.Autoscaling, "the effective autoscaling policy travels with the change")
}

func TestUpdateInferenceServiceTopologyKeepsReplicasWhenZero(t *testing.T) {
	seedImageRegistry(t)
	svc, _, db := newInferTestService(t)
	modelID := seedModel(t, db, "qwen-3b", "v1")

	id := createTopologyService(t, svc, "keep-replicas", &inferv1.CreateInferenceServiceRequest{
		ModelId: modelID, ModelVersion: "v1", ImageId: "img-vllm-nvidia",
		Replicas: 4, Accelerator: "nvidia",
	})

	_, err := svc.UpdateInferenceServiceTopology(orgContext("org-1"), &inferv1.UpdateInferenceServiceTopologyRequest{
		ServiceId: id,
		Topology:  TopologyPD,
	})
	require.NoError(t, err)

	repo, err := svc.repository()
	require.NoError(t, err)
	stored, err := repo.FindByIDAndOrganization(t.Context(), "org-1", id)
	require.NoError(t, err)
	assert.Equal(t, 4, stored.Replicas, "replicas=0 keeps the current count")
	assert.Equal(t, TopologyPD, stored.Topology)
	assert.Equal(t, 1, stored.PrefillReplicas)
	assert.Equal(t, 1, stored.DecodeReplicas)
}

func TestUpdateInferenceServiceTopologyValidation(t *testing.T) {
	seedImageRegistry(t)
	svc, client, db := newInferTestService(t)
	modelID := seedModel(t, db, "qwen-3b", "v1")

	id := createTopologyService(t, svc, "validate-svc", &inferv1.CreateInferenceServiceRequest{
		ModelId: modelID, ModelVersion: "v1", ImageId: "img-vllm-nvidia",
		Replicas: 1, Accelerator: "nvidia",
	})
	client.published = nil

	// Unknown service.
	_, err := svc.UpdateInferenceServiceTopology(orgContext("org-1"), &inferv1.UpdateInferenceServiceTopologyRequest{
		ServiceId: "does-not-exist",
		Topology:  TopologySingle,
	})
	require.Error(t, err)
	assert.Equal(t, apierrors.CodeInferServiceNotFound, apierrors.CodeOf(err))

	// Invalid layout.
	_, err = svc.UpdateInferenceServiceTopology(orgContext("org-1"), &inferv1.UpdateInferenceServiceTopologyRequest{
		ServiceId:          id,
		Topology:           TopologyDistributed,
		TensorParallelSize: 1,
		PipelineParallelSize: 1,
	})
	require.Error(t, err)
	assert.Equal(t, apierrors.CodeInferTopologyInvalid, apierrors.CodeOf(err))

	// Replica bounds are enforced for the optional field.
	_, err = svc.UpdateInferenceServiceTopology(orgContext("org-1"), &inferv1.UpdateInferenceServiceTopologyRequest{
		ServiceId: id,
		Topology:  TopologySingle,
		Replicas:  maxReplicas + 1,
	})
	require.Error(t, err)
	assert.Equal(t, apierrors.CodeInferReplicasInvalid, apierrors.CodeOf(err))
	assert.Empty(t, client.published, "a rejected update publishes nothing")
}

func TestUpdateInferenceServiceTopologyRejectsTerminated(t *testing.T) {
	seedImageRegistry(t)
	svc, _, db := newInferTestService(t)
	modelID := seedModel(t, db, "qwen-3b", "v1")

	id := createTopologyService(t, svc, "terminated-svc", &inferv1.CreateInferenceServiceRequest{
		ModelId: modelID, ModelVersion: "v1", ImageId: "img-vllm-nvidia",
		Replicas: 1, Accelerator: "nvidia",
	})
	repo, err := svc.repository()
	require.NoError(t, err)
	require.NoError(t, repo.MarkTerminated(t.Context(), "org-1", id))

	_, err = svc.UpdateInferenceServiceTopology(orgContext("org-1"), &inferv1.UpdateInferenceServiceTopologyRequest{
		ServiceId: id,
		Topology:  TopologySingle,
	})
	require.Error(t, err)
	assert.Equal(t, apierrors.CodeInferServiceStateInvalid, apierrors.CodeOf(err))
}
