package infer

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	apierrors "github.com/go-taas/go-taas/pkg/errors"
	inferv1 "github.com/go-taas/go-taas/proto/taas/infer/v1"
)

// TestNormalizeRdma verifies the request is normalized: disabled drops every
// value, an omitted count means one device, and the class is optional.
func TestNormalizeRdma(t *testing.T) {
	off, err := NormalizeRdma(nil)
	require.NoError(t, err)
	assert.Equal(t, RdmaSpec{}, off)

	off, err = NormalizeRdma(&inferv1.RdmaConfig{Enabled: false, Devices: 4, DeviceClass: "taas-rdma"})
	require.NoError(t, err)
	assert.Equal(t, RdmaSpec{}, off, "a disabled request must not keep devices or a class")

	defaults, err := NormalizeRdma(&inferv1.RdmaConfig{Enabled: true})
	require.NoError(t, err)
	assert.Equal(t, RdmaSpec{Enabled: true, Devices: 1}, defaults)

	explicit, err := NormalizeRdma(&inferv1.RdmaConfig{Enabled: true, Devices: 8, DeviceClass: " dranet-rdma-hpc "})
	require.NoError(t, err)
	assert.Equal(t, RdmaSpec{Enabled: true, Devices: 8, DeviceClass: "dranet-rdma-hpc"}, explicit)
}

// TestNormalizeRdmaRejectsBadValues verifies the device count and the class
// name are bounded before they reach a claim template.
func TestNormalizeRdmaRejectsBadValues(t *testing.T) {
	for _, devices := range []int32{-1, maxRdmaDevices + 1} {
		_, err := NormalizeRdma(&inferv1.RdmaConfig{Enabled: true, Devices: devices})
		require.Error(t, err, "devices=%d must be rejected", devices)
		assert.Equal(t, apierrors.CodeInferRdmaInvalid, apierrors.CodeOf(err))
	}
	for _, class := range []string{"Taas-RDMA", "taas_rdma", "taas rdma", "-taas", "taas-", "a..b"} {
		_, err := NormalizeRdma(&inferv1.RdmaConfig{Enabled: true, DeviceClass: class})
		require.Error(t, err, "class %q must be rejected", class)
		assert.Equal(t, apierrors.CodeInferRdmaInvalid, apierrors.CodeOf(err))
	}
}

// TestCreateInferenceServiceStoresRdma verifies the create RPC persists the
// request, the detail RPC returns it, and the change event carries it.
func TestCreateInferenceServiceStoresRdma(t *testing.T) {
	seedImageRegistry(t)
	svc, client, db := newInferTestService(t)
	modelID := seedModel(t, db, "qwen-3b", "v1")

	resp, err := svc.CreateInferenceService(orgContext("org-1"), &inferv1.CreateInferenceServiceRequest{
		Name: "rdma-demo", ModelId: modelID, ModelVersion: "v1",
		ImageId: "img-vllm-nvidia", Replicas: 1, Accelerator: "nvidia",
		Rdma: &inferv1.RdmaConfig{Enabled: true, Devices: 2, DeviceClass: "taas-rdma"},
	})
	require.NoError(t, err)

	repo := NewInferenceServiceRepository(db)
	row, err := repo.FindByID(t.Context(), resp.GetServiceId())
	require.NoError(t, err)
	assert.True(t, row.RdmaEnabled)
	assert.Equal(t, 2, row.RdmaDevices)
	assert.Equal(t, "taas-rdma", row.RdmaDeviceClass)

	detail, err := svc.GetInferenceService(orgContext("org-1"), &inferv1.GetInferenceServiceRequest{ServiceId: resp.GetServiceId()})
	require.NoError(t, err)
	assert.True(t, detail.GetRdma().GetEnabled())
	assert.EqualValues(t, 2, detail.GetRdma().GetDevices())
	assert.Equal(t, "taas-rdma", detail.GetRdma().GetDeviceClass())

	require.Len(t, client.published, 1)
	var evt changeEvent
	require.NoError(t, json.Unmarshal(client.published[0].Body, &evt))
	require.NotNil(t, evt.Rdma)
	assert.True(t, evt.Rdma.Enabled)
	assert.Equal(t, 2, evt.Rdma.Devices)
	assert.Equal(t, "taas-rdma", evt.Rdma.DeviceClass)

	// A service without RDMA carries no event field at all, so the controller
	// creates no claim.
	client.published = nil
	plain, err := svc.CreateInferenceService(orgContext("org-1"), &inferv1.CreateInferenceServiceRequest{
		Name: "no-rdma", ModelId: modelID, ModelVersion: "v1",
		ImageId: "img-vllm-nvidia", Replicas: 1, Accelerator: "nvidia",
	})
	require.NoError(t, err)
	require.Len(t, client.published, 1)
	evt = changeEvent{}
	require.NoError(t, json.Unmarshal(client.published[0].Body, &evt))
	assert.Nil(t, evt.Rdma)

	list, err := svc.ListInferenceServices(orgContext("org-1"), &inferv1.ListInferenceServicesRequest{})
	require.NoError(t, err)
	byID := map[string]*inferv1.InferenceServiceSummary{}
	for _, row := range list.GetServices() {
		byID[row.GetServiceId()] = row
	}
	assert.True(t, byID[resp.GetServiceId()].GetRdma().GetEnabled(), "the list summary reports the request")
	assert.False(t, byID[plain.GetServiceId()].GetRdma().GetEnabled())
}

// TestCreateInferenceServiceRejectsBadRdma verifies a bad request fails before
// any row or event exists.
func TestCreateInferenceServiceRejectsBadRdma(t *testing.T) {
	seedImageRegistry(t)
	svc, client, db := newInferTestService(t)
	modelID := seedModel(t, db, "qwen-3b", "v1")

	_, err := svc.CreateInferenceService(orgContext("org-1"), &inferv1.CreateInferenceServiceRequest{
		Name: "bad-rdma", ModelId: modelID, ModelVersion: "v1",
		ImageId: "img-vllm-nvidia", Replicas: 1, Accelerator: "nvidia",
		Rdma: &inferv1.RdmaConfig{Enabled: true, Devices: 99},
	})
	require.Error(t, err)
	assert.Equal(t, apierrors.CodeInferRdmaInvalid, apierrors.CodeOf(err))
	assert.Empty(t, client.published)
}

// TestUpdateTopologyReplacesRdma verifies the RDMA request rides with the pod
// shape: an explicit request replaces it, an omitted one keeps it.
func TestUpdateTopologyReplacesRdma(t *testing.T) {
	seedImageRegistry(t)
	svc, client, db := newInferTestService(t)
	modelID := seedModel(t, db, "qwen-3b", "v1")

	created, err := svc.CreateInferenceService(orgContext("org-1"), &inferv1.CreateInferenceServiceRequest{
		Name: "rdma-topology", ModelId: modelID, ModelVersion: "v1",
		ImageId: "img-vllm-nvidia", Replicas: 1, Accelerator: "nvidia",
		Rdma: &inferv1.RdmaConfig{Enabled: true, Devices: 1},
	})
	require.NoError(t, err)
	repo := NewInferenceServiceRepository(db)

	// An omitted request keeps the stored one.
	_, err = svc.UpdateInferenceServiceTopology(orgContext("org-1"), &inferv1.UpdateInferenceServiceTopologyRequest{
		ServiceId: created.GetServiceId(), Topology: "single",
	})
	require.NoError(t, err)
	row, err := repo.FindByID(t.Context(), created.GetServiceId())
	require.NoError(t, err)
	assert.True(t, row.RdmaEnabled, "a layout change must not detach the devices")
	assert.Equal(t, 1, row.RdmaDevices)

	// An explicit request replaces it.
	client.published = nil
	_, err = svc.UpdateInferenceServiceTopology(orgContext("org-1"), &inferv1.UpdateInferenceServiceTopologyRequest{
		ServiceId: created.GetServiceId(), Topology: "multinode",
		TensorParallelSize: 2, PipelineParallelSize: 1,
		Rdma: &inferv1.RdmaConfig{Enabled: true, Devices: 4, DeviceClass: "dranet-rdma-hpc"},
	})
	require.NoError(t, err)
	row, err = repo.FindByID(t.Context(), created.GetServiceId())
	require.NoError(t, err)
	assert.Equal(t, 4, row.RdmaDevices)
	assert.Equal(t, "dranet-rdma-hpc", row.RdmaDeviceClass)

	require.Len(t, client.published, 1)
	var evt changeEvent
	require.NoError(t, json.Unmarshal(client.published[0].Body, &evt))
	require.NotNil(t, evt.Rdma)
	assert.Equal(t, 4, evt.Rdma.Devices)

	// Disabling removes the request from the row and the event.
	client.published = nil
	_, err = svc.UpdateInferenceServiceTopology(orgContext("org-1"), &inferv1.UpdateInferenceServiceTopologyRequest{
		ServiceId: created.GetServiceId(), Topology: "single",
		Rdma: &inferv1.RdmaConfig{Enabled: false},
	})
	require.NoError(t, err)
	row, err = repo.FindByID(t.Context(), created.GetServiceId())
	require.NoError(t, err)
	assert.False(t, row.RdmaEnabled)
	require.Len(t, client.published, 1)
	evt = changeEvent{}
	require.NoError(t, json.Unmarshal(client.published[0].Body, &evt))
	assert.Nil(t, evt.Rdma)
}
