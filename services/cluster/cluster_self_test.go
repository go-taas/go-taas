package cluster

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	clusterv1 "github.com/go-taas/go-taas/proto/taas/cluster/v1"
)

// fakeSelfCluster returns a fixed self-cluster description.
type fakeSelfCluster struct {
	info *SelfClusterInfo
	err  error
}

func (f fakeSelfCluster) SelfCluster(context.Context) (*SelfClusterInfo, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.info, nil
}

func TestDiscoverClusterRegistersLocalCluster(t *testing.T) {
	svc := newService(t, newTestDB(t), true)
	svc.SetSelfClusterProvider(fakeSelfCluster{info: &SelfClusterInfo{
		Name:              "cluster-abc123",
		Region:            "cn-north",
		NodeCount:         3,
		AcceleratorLabels: []string{"nvidia.com/gpu.product"},
	}})
	ctx := ctxWithOrg(context.Background(), "org-1")

	resp, err := svc.DiscoverCluster(ctx, &clusterv1.DiscoverClusterRequest{})
	require.NoError(t, err)
	assert.True(t, resp.GetCluster().GetLocal(), "the discovered cluster is marked local")
	assert.Equal(t, "cluster-abc123", resp.GetCluster().GetName())
	assert.Equal(t, "cn-north", resp.GetCluster().GetRegion())
	assert.EqualValues(t, 3, resp.GetCluster().GetNodeCount())
	assert.Equal(t, []string{"nvidia.com/gpu.product"}, resp.GetCluster().GetNodeSelectorLabels())

	// Idempotent: a second discovery updates the same row.
	first := resp.GetCluster().GetClusterId()
	again, err := svc.DiscoverCluster(ctx, &clusterv1.DiscoverClusterRequest{})
	require.NoError(t, err)
	assert.Equal(t, first, again.GetCluster().GetClusterId(), "discovery upserts one local row")

	list, err := svc.ListClusters(ctx, &clusterv1.ListClustersRequest{})
	require.NoError(t, err)
	assert.Len(t, list.GetClusters(), 1, "no duplicate cluster rows")
}

func TestDiscoverClusterRequiresAdmin(t *testing.T) {
	svc := newService(t, newTestDB(t), false)
	svc.SetSelfClusterProvider(fakeSelfCluster{info: &SelfClusterInfo{Name: "c"}})
	_, err := svc.DiscoverCluster(ctxWithOrg(context.Background(), "org-1"), &clusterv1.DiscoverClusterRequest{})
	require.Error(t, err)
}

func TestDiscoverClusterWithoutProviderFailsClosed(t *testing.T) {
	svc := newService(t, newTestDB(t), true)
	_, err := svc.DiscoverCluster(ctxWithOrg(context.Background(), "org-1"), &clusterv1.DiscoverClusterRequest{})
	require.Error(t, err)
}

func TestDiscoverClusterProviderErrorIsReported(t *testing.T) {
	svc := newService(t, newTestDB(t), true)
	svc.SetSelfClusterProvider(fakeSelfCluster{err: errors.New("apiserver down")})
	_, err := svc.DiscoverCluster(ctxWithOrg(context.Background(), "org-1"), &clusterv1.DiscoverClusterRequest{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "apiserver down")
}

func TestUpsertLocalClusterRejectsNameCollision(t *testing.T) {
	db := newTestDB(t)
	repo := NewClusterRepository(db)
	ctx := context.Background()

	require.NoError(t, repo.CreateCluster(ctx, &Cluster{
		ID: "11111111-1111-1111-1111-111111111111", Name: "taken",
		KubeconfigRef: "kube/x", State: ClusterStateActive,
	}))
	_, err := repo.UpsertLocalCluster(ctx, &Cluster{Name: "taken", KubeconfigRef: localKubeconfigRef, State: ClusterStateActive, Local: true})
	require.Error(t, err, "a local cluster must not adopt a registered cluster's name")
}
