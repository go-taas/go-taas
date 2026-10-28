package infer

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/metadata"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	inferv1 "github.com/go-taas/go-taas/proto/taas/infer/v1"

	"github.com/go-taas/go-taas/pkg/mq"
	"github.com/go-taas/go-taas/services/image"
	"github.com/go-taas/go-taas/services/model"

	apierrors "github.com/go-taas/go-taas/pkg/errors"
)

// orgContext builds a context carrying the organization metadata.
func orgContext(org string) context.Context {
	return metadata.NewIncomingContext(context.Background(),
		metadata.Pairs("x-organization-id", org))
}

func seedImageRegistry(t *testing.T) {
	t.Helper()
	// Seed a disposable DB-backed registry and bind the package-level
	// Lookup/List helpers to it.
	db, err := gorm.Open(sqlite.Open(testDSN(t)), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&image.Image{}, &image.WarmupTask{}))
	repo := image.NewRepository(db)
	for _, s := range []*image.Summary{
		{ImageID: "img-vllm-nvidia", Name: "ghcr.io/go-taas/vllm", Tag: "v0.6.3", Accelerator: "nvidia", Engine: "vllm"},
		{ImageID: "img-vllm-iluvatar", Name: "ghcr.io/go-taas/vllm-iluvatar", Tag: "v0.6.3", Accelerator: "iluvatar", Engine: "vllm"},
	} {
		require.NoError(t, repo.CreateImage(context.Background(), &image.Image{
			ID: s.ImageID, Name: s.Name, Tag: s.Tag, Accelerator: s.Accelerator, Engine: s.Engine,
		}))
	}
	image.WireRegistryForTest(db)
	t.Cleanup(func() {
		sqlDB, _ := db.DB()
		_ = sqlDB.Close()
	})
}

func TestCreateInferenceServiceHappyPath(t *testing.T) {
	seedImageRegistry(t)
	svc, client, db := newInferTestService(t)
	modelID := seedModel(t, db, "qwen-3b", "v1")

	resp, err := svc.CreateInferenceService(orgContext("org-1"), &inferv1.CreateInferenceServiceRequest{
		Name:         "demo-svc",
		ModelId:      modelID,
		ModelVersion: "v1",
		ImageId:      "img-vllm-nvidia",
		Replicas:     2,
		Accelerator:  "nvidia",
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

// fakeClusterResolver resolves a fixed cluster id.
type fakeClusterResolver struct {
	id  string
	err error
}

func (f fakeClusterResolver) ResolveCluster(context.Context, string) (string, error) {
	return f.id, f.err
}

func TestCreateInferenceServiceRecordsCluster(t *testing.T) {
	seedImageRegistry(t)
	svc, _, db := newInferTestService(t)
	svc.SetClusterResolver(fakeClusterResolver{id: "cluster-default"})
	modelID := seedModel(t, db, "qwen-3b", "v1")

	resp, err := svc.CreateInferenceService(orgContext("org-1"), &inferv1.CreateInferenceServiceRequest{
		Name:         "demo-svc",
		ModelId:      modelID,
		ModelVersion: "v1",
		ImageId:      "img-vllm-nvidia",
		Replicas:     1,
		Accelerator:  "nvidia",
	})
	require.NoError(t, err)

	row, err := svc.repository()
	require.NoError(t, err)
	stored, err := row.FindByIDAndOrganization(context.Background(), "org-1", resp.GetServiceId())
	require.NoError(t, err)
	assert.Equal(t, "cluster-default", stored.ClusterID)
}

// fakeSliceValidator validates slice requests.
type fakeSliceValidator struct {
	info SliceTemplateInfo
	err  error
}

func (f fakeSliceValidator) ValidateSlice(_ context.Context, _, _, _ string) (SliceTemplateInfo, error) {
	return f.info, f.err
}

func TestCreateInferenceServiceSliceValidation(t *testing.T) {
	seedImageRegistry(t)
	svc, client, db := newInferTestService(t)
	modelID := seedModel(t, db, "qwen-3b", "v1")

	// A slice request with a nil validator fails closed (13403).
	_, err := svc.CreateInferenceService(orgContext("org-1"), &inferv1.CreateInferenceServiceRequest{
		Name: "slice-svc", ModelId: modelID, ModelVersion: "v1",
		ImageId: "img-vllm-nvidia", Replicas: 1, Accelerator: "nvidia",
		PoolId: "pool-a", SliceTemplateId: "small",
	})
	assert.EqualValues(t, apierrors.CodeVGPUSliceUnavailable, apierrors.CodeOf(err))

	// A valid slice request persists the slice fields and publishes them.
	svc.SetSliceValidator(fakeSliceValidator{info: SliceTemplateInfo{MemoryGiB: 8, ComputePct: 25}})
	_, err = svc.CreateInferenceService(orgContext("org-1"), &inferv1.CreateInferenceServiceRequest{
		Name: "slice-svc", ModelId: modelID, ModelVersion: "v1",
		ImageId: "img-vllm-nvidia", Replicas: 1, Accelerator: "nvidia",
		PoolId: "pool-a", SliceTemplateId: "small",
	})
	require.NoError(t, err)
	require.Len(t, client.published, 1)
	var evt changeEvent
	require.NoError(t, json.Unmarshal(client.published[0].Body, &evt))
	assert.Equal(t, "pool-a", evt.PoolID)
	assert.Equal(t, "small", evt.SliceTemplateID)
	assert.Equal(t, int64(8*1024), evt.SliceMemoryMiB)
	assert.Equal(t, int64(25), evt.SliceComputePct)

	// A validator error propagates.
	svc.SetSliceValidator(fakeSliceValidator{err: apierrors.New(apierrors.CodeVGPUSliceUnavailable)})
	_, err = svc.CreateInferenceService(orgContext("org-1"), &inferv1.CreateInferenceServiceRequest{
		Name: "slice-svc2", ModelId: modelID, ModelVersion: "v1",
		ImageId: "img-vllm-nvidia", Replicas: 1, Accelerator: "nvidia",
		PoolId: "pool-a", SliceTemplateId: "small",
	})
	assert.EqualValues(t, apierrors.CodeVGPUSliceUnavailable, apierrors.CodeOf(err))

	// Only one of pool_id/slice_template_id present -> 13403.
	_, err = svc.CreateInferenceService(orgContext("org-1"), &inferv1.CreateInferenceServiceRequest{
		Name: "slice-svc3", ModelId: modelID, ModelVersion: "v1",
		ImageId: "img-vllm-nvidia", Replicas: 1, Accelerator: "nvidia",
		PoolId: "pool-a",
	})
	assert.EqualValues(t, apierrors.CodeVGPUSliceUnavailable, apierrors.CodeOf(err))
}

func TestGetInferenceServiceAssignedSlice(t *testing.T) {
	db := newInferTestDB(t)
	repo := NewInferenceServiceRepository(db)
	ctx := context.Background()
	svc := seedService(t, repo, "org-a", "svc-slice", StateRunning, time.Now())
	// Set the slice fields on the row.
	require.NoError(t, repo.DB(ctx).Model(&InferenceService{}).Where("id = ?", svc.ID).Updates(map[string]any{
		"pool_id": "pool-a", "slice_template_id": "small",
		"slice_node_id": "node-a", "slice_gpu_index": 0,
		"slice_memory_gib": 8, "slice_compute_pct": 25,
	}).Error)

	svc2, _, _ := newInferTestService(t)
	// Reuse the same db so the row is visible.
	svc2.repo = repo
	resp, err := svc2.GetInferenceService(orgContext("org-a"), &inferv1.GetInferenceServiceRequest{ServiceId: svc.ID})
	require.NoError(t, err)
	require.NotNil(t, resp.GetAssignedSlice())
	assert.Equal(t, "pool-a", resp.GetAssignedSlice().GetPoolId())
	assert.Equal(t, "small", resp.GetAssignedSlice().GetTemplateName())
	assert.Equal(t, "node-a", resp.GetAssignedSlice().GetNodeId())
	assert.Equal(t, int64(0), resp.GetAssignedSlice().GetGpuIndex())
}

// fakeIncidentReader returns a fixed incident or healthy.
type fakeIncidentReader struct {
	incident ServiceIncident
	ok       bool
}

func (f fakeIncidentReader) ActiveIncidentForService(context.Context, string) (ServiceIncident, bool, error) {
	return f.incident, f.ok, nil
}

func TestGetServiceStatus(t *testing.T) {
	db := newInferTestDB(t)
	repo := NewInferenceServiceRepository(db)
	svc := seedService(t, repo, "org-a", "svc-status", StateRunning, time.Now())

	svc2, _, _ := newInferTestService(t)
	svc2.repo = repo

	// Healthy: no incident.
	resp, err := svc2.GetServiceStatus(orgContext("org-a"), &inferv1.GetServiceStatusRequest{ServiceId: svc.ID})
	require.NoError(t, err)
	assert.Equal(t, StateRunning, resp.GetState())
	assert.False(t, resp.GetDegraded())
	assert.Nil(t, resp.GetIncident())

	// Degraded: an active incident is returned.
	svc2.SetIncidentReader(fakeIncidentReader{
		incident: ServiceIncident{IncidentID: "inc-1", Severity: "warning", Status: "recovering", RootCause: "service_crash_loop", Message: "recovering"},
		ok:       true,
	})
	resp, err = svc2.GetServiceStatus(orgContext("org-a"), &inferv1.GetServiceStatusRequest{ServiceId: svc.ID})
	require.NoError(t, err)
	assert.True(t, resp.GetDegraded())
	require.NotNil(t, resp.GetIncident())
	assert.Equal(t, "inc-1", resp.GetIncident().GetIncidentId())
	assert.Equal(t, "service_crash_loop", resp.GetIncident().GetRootCause())
}

func TestGetAdminServiceStatus(t *testing.T) {
	db := newInferTestDB(t)
	repo := NewInferenceServiceRepository(db)
	svc := seedService(t, repo, "org-a", "svc-admin-status", StateRunning, time.Now())

	svc2, _, _ := newInferTestService(t)
	svc2.repo = repo

	// Healthy: no incident.
	resp, err := svc2.GetAdminServiceStatus(orgContext("org-a"), &inferv1.GetAdminServiceStatusRequest{ServiceId: svc.ID})
	require.NoError(t, err)
	assert.Equal(t, StateRunning, resp.GetState())
	assert.False(t, resp.GetDegraded())
	assert.Nil(t, resp.GetIncident())

	// Degraded: an active incident is returned.
	svc2.SetIncidentReader(fakeIncidentReader{
		incident: ServiceIncident{IncidentID: "inc-1", Severity: "warning", Status: "recovering", RootCause: "service_crash_loop", Message: "recovering"},
		ok:       true,
	})
	resp, err = svc2.GetAdminServiceStatus(orgContext("org-a"), &inferv1.GetAdminServiceStatusRequest{ServiceId: svc.ID})
	require.NoError(t, err)
	assert.True(t, resp.GetDegraded())
	require.NotNil(t, resp.GetIncident())
	assert.Equal(t, "inc-1", resp.GetIncident().GetIncidentId())

	// A service is a platform asset: another organization reading it is
	// not a leak, it is the point. Access policy, not the organization
	// field, decides who may *use* a service.
	_, err = svc2.GetAdminServiceStatus(orgContext("org-b"), &inferv1.GetAdminServiceStatusRequest{ServiceId: svc.ID})
	require.NoError(t, err)

	// A malformed (non-UUID) id maps to not-found, not a uuid cast error.
	_, err = svc2.GetAdminServiceStatus(orgContext("org-a"), &inferv1.GetAdminServiceStatusRequest{ServiceId: "not-a-uuid"})
	assert.EqualValues(t, apierrors.CodeInferServiceNotFound, apierrors.CodeOf(err))
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
			// A vendor the fleet could report is accepted (the image is
			// what pins it down); malformed syntax is refused.
			"accelerator with bad syntax",
			&inferv1.CreateInferenceServiceRequest{Name: "demo", ModelId: modelID, ModelVersion: "v1", ImageId: "img-vllm-nvidia", Replicas: 1, Accelerator: "TPU/../x"},
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

	// A name is unique platform-wide, not per organization: a service is a
	// platform asset, and two rows with one name would render into one
	// Kubernetes Deployment.
	_, err = svc.CreateInferenceService(orgContext("org-2"), req)
	assert.EqualValues(t, apierrors.CodeInferServiceExists, apierrors.CodeOf(err))
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

	// The list is platform-wide: a service is a platform asset, so every
	// caller sees the same set no matter which organization created it.
	resp, err := svc.ListInferenceServices(orgContext("org-1"), &inferv1.ListInferenceServicesRequest{})
	require.NoError(t, err)
	assert.Len(t, resp.GetServices(), 3)
	assert.Equal(t, int64(3), resp.GetPageMeta().GetTotal())

	// The provenance filter still narrows by creating organization.
	resp, err = svc.ListInferenceServices(orgContext("org-1"), &inferv1.ListInferenceServicesRequest{OrganizationId: "org-1"})
	require.NoError(t, err)
	assert.Len(t, resp.GetServices(), 2)

	// Get: endpoints hidden while pending.
	get, err := svc.GetInferenceService(orgContext("org-1"), &inferv1.GetInferenceServiceRequest{ServiceId: id1})
	require.NoError(t, err)
	assert.Equal(t, StatePending, get.GetService().GetState())
	assert.Empty(t, get.GetEndpoints())

	// Drive to running via the status consumer path: endpoints appear.
	repo, err := svc.repository()
	require.NoError(t, err)
	require.NoError(t, repo.ApplyStatus(context.Background(), id1, StateRunning, []string{"http://demo/v1"}, nil, nil, nil))
	get, err = svc.GetInferenceService(orgContext("org-1"), &inferv1.GetInferenceServiceRequest{ServiceId: id1})
	require.NoError(t, err)
	assert.Equal(t, StateRunning, get.GetService().GetState())
	// No platform gateway is configured in this test, so the stored
	// in-cluster endpoint is reported as the fallback.
	assert.Equal(t, []string{"http://demo/v1"}, get.GetEndpoints())

	// Any caller resolves the same service: there is no organization
	// boundary to cross.
	crossOrg, err := svc.GetInferenceService(orgContext("org-2"), &inferv1.GetInferenceServiceRequest{ServiceId: id1})
	require.NoError(t, err)
	assert.Equal(t, id1, crossOrg.GetService().GetServiceId())
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

func TestUpdateInferenceServiceVersion(t *testing.T) {
	seedImageRegistry(t)
	svc, client, db := newInferTestService(t)
	modelID := seedModel(t, db, "qwen-3b", "v1")
	// Register a second version.
	repo := model.NewRepository(db)
	_, err := repo.RegisterModelOrCreateVersion(context.Background(), "qwen-3b", "", "", "v2", "qwen-3b/v2")
	require.NoError(t, err)

	created, err := svc.CreateInferenceService(orgContext("org-1"), &inferv1.CreateInferenceServiceRequest{
		Name: "demo", ModelId: modelID, ModelVersion: "v1",
		ImageId: "img-vllm-nvidia", Replicas: 1, Accelerator: "nvidia",
	})
	require.NoError(t, err)
	client.published = nil

	// Update the version in place.
	resp, err := svc.UpdateInferenceServiceVersion(orgContext("org-1"), &inferv1.UpdateInferenceServiceVersionRequest{
		ServiceId: created.GetServiceId(), ModelVersion: "v2",
	})
	require.NoError(t, err)
	assert.Equal(t, created.GetServiceId(), resp.GetServiceId())
	assert.Equal(t, StateDeploying, resp.GetState())

	// One update_version event with the new version and weight path.
	require.Len(t, client.published, 1)
	var evt changeEvent
	require.NoError(t, json.Unmarshal(client.published[0].Body, &evt))
	assert.Equal(t, EventTypeUpdateVersion, evt.EventType)
	assert.Equal(t, "v2", evt.Model.Version)
	assert.Equal(t, "qwen-3b/v2", evt.Model.WeightPath)

	// The row reflects the new version and deploying state.
	get, err := svc.GetInferenceService(orgContext("org-1"), &inferv1.GetInferenceServiceRequest{ServiceId: created.GetServiceId()})
	require.NoError(t, err)
	assert.Equal(t, "v2", get.GetService().GetModelVersion())
	assert.Equal(t, StateDeploying, get.GetService().GetState())

	// Unknown service → 10301.
	_, err = svc.UpdateInferenceServiceVersion(orgContext("org-1"), &inferv1.UpdateInferenceServiceVersionRequest{
		ServiceId: "no-such", ModelVersion: "v2",
	})
	require.Error(t, err)
	ae, ok := apierrors.As(err)
	require.True(t, ok)
	assert.Equal(t, apierrors.CodeInferServiceNotFound, ae.Code)

	// Unknown version → 10103.
	_, err = svc.UpdateInferenceServiceVersion(orgContext("org-1"), &inferv1.UpdateInferenceServiceVersionRequest{
		ServiceId: created.GetServiceId(), ModelVersion: "nope",
	})
	require.Error(t, err)
	ae, ok = apierrors.As(err)
	require.True(t, ok)
	assert.Equal(t, apierrors.CodeModelVersionNotFound, ae.Code)

	// Terminated service → 10303.
	_, err = svc.DeleteInferenceService(orgContext("org-1"), &inferv1.DeleteInferenceServiceRequest{ServiceId: created.GetServiceId()})
	require.NoError(t, err)
	_, err = svc.UpdateInferenceServiceVersion(orgContext("org-1"), &inferv1.UpdateInferenceServiceVersionRequest{
		ServiceId: created.GetServiceId(), ModelVersion: "v2",
	})
	require.Error(t, err)
	ae, ok = apierrors.As(err)
	require.True(t, ok)
	assert.Equal(t, apierrors.CodeInferServiceStateInvalid, ae.Code)
}

// fakeLogFetcher is a deterministic LogFetcher for unit tests.
type fakeLogFetcher struct {
	pods    []LogPod
	windows map[string]LogWindow
}

func (f *fakeLogFetcher) ListServiceLogPods(_ context.Context, _ string) ([]LogPod, error) {
	return f.pods, nil
}

func (f *fakeLogFetcher) GetServiceLogs(_ context.Context, _, pod, container string, _ int, _, _ string) (LogWindow, error) {
	if w, ok := f.windows[pod+"|"+container]; ok {
		return w, nil
	}
	return LogWindow{}, nil
}

func TestServiceLogs(t *testing.T) {
	seedImageRegistry(t)
	svc, _, db := newInferTestService(t)
	modelID := seedModel(t, db, "qwen-3b", "v1")

	created, err := svc.CreateInferenceService(orgContext("org-1"), &inferv1.CreateInferenceServiceRequest{
		Name: "demo", ModelId: modelID, ModelVersion: "v1",
		ImageId: "img-vllm-nvidia", Replicas: 1, Accelerator: "nvidia",
	})
	require.NoError(t, err)

	svc.SetLogFetcher(&fakeLogFetcher{
		pods: []LogPod{
			{ReplicaIndex: "replica-1", Container: "engine", State: "Running"},
		},
		windows: map[string]LogWindow{
			"replica-1|engine": {
				Lines:      []LogLine{{Timestamp: 100, Level: "info", Message: "hi"}},
				NextOffset: "replica-1|engine|100",
				HasMore:    true,
			},
		},
	})

	// ListServiceLogPods.
	resp, err := svc.ListServiceLogPods(orgContext("org-1"), &inferv1.ListServiceLogPodsRequest{ServiceId: created.GetServiceId()})
	require.NoError(t, err)
	require.Len(t, resp.GetPods(), 1)
	assert.Equal(t, "replica-1", resp.GetPods()[0].GetReplicaIndex())
	assert.Equal(t, "engine", resp.GetPods()[0].GetContainer())

	// Unknown service → 10301.
	_, err = svc.ListServiceLogPods(orgContext("org-1"), &inferv1.ListServiceLogPodsRequest{ServiceId: "no-such"})
	require.Error(t, err)
	ae, ok := apierrors.As(err)
	require.True(t, ok)
	assert.Equal(t, apierrors.CodeInferServiceNotFound, ae.Code)

	// GetServiceLogs.
	logs, err := svc.GetServiceLogs(orgContext("org-1"), &inferv1.GetServiceLogsRequest{
		ServiceId: created.GetServiceId(), Pod: "replica-1", Container: "engine", Tail: 500,
	})
	require.NoError(t, err)
	require.Len(t, logs.GetLines(), 1)
	assert.Equal(t, int64(100), logs.GetLines()[0].GetTimestamp())
	assert.Equal(t, "info", logs.GetLines()[0].GetLevel())
	assert.Equal(t, "hi", logs.GetLines()[0].GetMessage())
	assert.Equal(t, "replica-1|engine|100", logs.GetNextOffset())
	assert.Equal(t, true, logs.GetHasMore())

	// Invalid tail → 10404.
	_, err = svc.GetServiceLogs(orgContext("org-1"), &inferv1.GetServiceLogsRequest{
		ServiceId: created.GetServiceId(), Pod: "replica-1", Tail: 99999,
	})
	require.Error(t, err)
	ae, ok = apierrors.As(err)
	require.True(t, ok)
	assert.Equal(t, apierrors.CodeMeteringRangeInvalid, ae.Code)

	// Malformed since → 10404.
	_, err = svc.GetServiceLogs(orgContext("org-1"), &inferv1.GetServiceLogsRequest{
		ServiceId: created.GetServiceId(), Pod: "replica-1", Since: "not-a-time",
	})
	require.Error(t, err)
	ae, ok = apierrors.As(err)
	require.True(t, ok)
	assert.Equal(t, apierrors.CodeMeteringRangeInvalid, ae.Code)

	// Unknown service → 10301.
	_, err = svc.GetServiceLogs(orgContext("org-1"), &inferv1.GetServiceLogsRequest{
		ServiceId: "no-such", Pod: "replica-1",
	})
	require.Error(t, err)
	ae, ok = apierrors.As(err)
	require.True(t, ok)
	assert.Equal(t, apierrors.CodeInferServiceNotFound, ae.Code)
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

// fakePlatformAdmin reports a fixed platform-administrator verdict.
type fakePlatformAdmin struct{ admin bool }

func (f fakePlatformAdmin) IsPlatformAdmin(context.Context) (bool, error) { return f.admin, nil }

// TestListInferenceServicesPlatformWide verifies the list is platform-wide
// for every caller: a service is a platform asset, so there is no
// organization view to fall back to and the console needs no selector.
func TestListInferenceServicesPlatformWide(t *testing.T) {
	svc, _, db := newInferTestService(t)
	svc.SetPlatformAdminResolver(fakePlatformAdmin{admin: true})
	require.NoError(t, db.Create(&InferenceService{
		ID: "svc-a", OrganizationID: "org-a", Name: "a", ModelID: "m",
		ModelVersion: "v1", ImageID: "img-vllm-nvidia", Replicas: 1, State: StateRunning,
	}).Error)
	require.NoError(t, db.Create(&InferenceService{
		ID: "svc-b", OrganizationID: "org-b", Name: "b", ModelID: "m",
		ModelVersion: "v1", ImageID: "img-vllm-nvidia", Replicas: 1, State: StateRunning,
	}).Error)

	list, err := svc.ListInferenceServices(orgContext("org-a"), &inferv1.ListInferenceServicesRequest{})
	require.NoError(t, err)
	assert.Len(t, list.GetServices(), 2, "both organizations' services are listed")

	// The optional filter narrows to one organization.
	filtered, err := svc.ListInferenceServices(orgContext("org-a"), &inferv1.ListInferenceServicesRequest{
		OrganizationId: "org-b",
	})
	require.NoError(t, err)
	require.Len(t, filtered.GetServices(), 1)
	assert.Equal(t, "svc-b", filtered.GetServices()[0].GetServiceId())
	assert.Equal(t, "org-b", filtered.GetServices()[0].GetOrganizationId())
}
