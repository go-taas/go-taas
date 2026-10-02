package infer

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/go-taas/go-taas/services/cluster"
)

// TestClusterWorkloadProviderListServicesByCluster verifies
// ListServicesByCluster returns the services placed on a cluster
// (feature #40, AC2). It is a regression test for the missing
// cluster_id column: the query must run against a schema that has the
// column.
func TestClusterWorkloadProviderListServicesByCluster(t *testing.T) {
	db := newInferTestDB(t)
	p := NewClusterWorkloadProvider(db)

	// No services placed on the cluster yet -> empty list.
	rows, err := p.ListServicesByCluster(context.Background(), "cluster-a")
	require.NoError(t, err)
	assert.Empty(t, rows)

	// Place two services on cluster-a and one on cluster-b.
	svc := &InferenceService{
		ID:             NewServiceID(),
		OrganizationID: "org-1",
		Name:           "svc-a1",
		ModelID:        "11111111-1111-1111-1111-111111111111",
		ModelVersion:   "v1",
		ImageID:        "img-1",
		Replicas:       1,
		Accelerator:    "nvidia",
		State:          StateRunning,
		ClusterID:      "cluster-a",
	}
	require.NoError(t, db.Create(svc).Error)
	svc2 := &InferenceService{
		ID:             NewServiceID(),
		OrganizationID: "org-1",
		Name:           "svc-a2",
		ModelID:        "11111111-1111-1111-1111-111111111111",
		ModelVersion:   "v1",
		ImageID:        "img-1",
		Replicas:       1,
		Accelerator:    "nvidia",
		State:          StateRunning,
		ClusterID:      "cluster-a",
	}
	require.NoError(t, db.Create(svc2).Error)
	svcB := &InferenceService{
		ID:             NewServiceID(),
		OrganizationID: "org-1",
		Name:           "svc-b1",
		ModelID:        "11111111-1111-1111-1111-111111111111",
		ModelVersion:   "v1",
		ImageID:        "img-1",
		Replicas:       1,
		Accelerator:    "nvidia",
		State:          StateRunning,
		ClusterID:      "cluster-b",
	}
	require.NoError(t, db.Create(svcB).Error)

	rows, err = p.ListServicesByCluster(context.Background(), "cluster-a")
	require.NoError(t, err)
	require.Len(t, rows, 2)
	got := map[string]cluster.ClusterWorkload{}
	for _, r := range rows {
		got[r.ServiceID] = r
	}
	assert.Equal(t, "svc-a1", got[svc.ID].Name)
	assert.Equal(t, "svc-a2", got[svc2.ID].Name)
	assert.Equal(t, StateRunning, got[svc.ID].State)

	// cluster-b has one service.
	rows, err = p.ListServicesByCluster(context.Background(), "cluster-b")
	require.NoError(t, err)
	require.Len(t, rows, 1)
	assert.Equal(t, "svc-b1", rows[0].Name)
}
