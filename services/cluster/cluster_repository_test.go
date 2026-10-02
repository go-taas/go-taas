package cluster

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	apierrors "github.com/go-taas/go-taas/pkg/errors"
)

func newTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&Cluster{}))
	t.Cleanup(func() {
		sqlDB, _ := db.DB()
		_ = sqlDB.Close()
	})
	return db
}

func TestClusterRepository(t *testing.T) {
	db := newTestDB(t)
	repo := NewClusterRepository(db)
	clusterID := "11111111-1111-1111-1111-111111111111"

	// AC1: CreateCluster + ListClusters.
	c := &Cluster{ID: clusterID, Name: "cluster-a", Region: "cn-north", KubeconfigRef: "kube/cluster-a", State: ClusterStateActive}
	require.NoError(t, repo.CreateCluster(context.Background(), c))
	rows, err := repo.ListClusters(context.Background())
	require.NoError(t, err)
	require.Len(t, rows, 1)
	assert.Equal(t, "cluster-a", rows[0].Name)

	// AC1: duplicate name -> 12402.
	dup := &Cluster{ID: "22222222-2222-2222-2222-222222222222", Name: "cluster-a", KubeconfigRef: "kube/x", State: ClusterStateActive}
	err = repo.CreateCluster(context.Background(), dup)
	require.Error(t, err)
	assert.Equal(t, apierrors.CodeClusterExists, apierrors.CodeOf(err))

	// AC2: FindClusterByID.
	found, err := repo.FindClusterByID(context.Background(), clusterID)
	require.NoError(t, err)
	assert.Equal(t, "cluster-a", found.Name)

	// AC2: unknown cluster -> 12401.
	_, err = repo.FindClusterByID(context.Background(), "missing")
	require.Error(t, err)
	assert.Equal(t, apierrors.CodeClusterNotFound, apierrors.CodeOf(err))

	// AC3: UpdateClusterState.
	require.NoError(t, repo.UpdateClusterState(context.Background(), clusterID, ClusterStateDisabled))
	found, err = repo.FindClusterByID(context.Background(), clusterID)
	require.NoError(t, err)
	assert.Equal(t, ClusterStateDisabled, found.State)
}

func TestValidKubeconfigRef(t *testing.T) {
	assert.True(t, validKubeconfigRef("kube/cluster-a"))
	assert.False(t, validKubeconfigRef(""))
	assert.False(t, validKubeconfigRef("/kube/cluster-a"))
	assert.False(t, validKubeconfigRef("kube/../cluster-a"))
	assert.False(t, validKubeconfigRef("kube\\cluster-a"))
}

func TestProjectionCache(t *testing.T) {
	cache := NewProjectionCache()

	// AC2: empty cache returns nil.
	assert.Nil(t, cache.Get("c1"))

	// AC2: Replace wholesale.
	snapshot := map[string]*ClusterHealth{
		"c1": {ClusterID: "c1", Health: "healthy", NodeCount: 3, LastCheckedAt: 100},
	}
	cache.Replace(snapshot)
	health := cache.Get("c1")
	require.NotNil(t, health)
	assert.Equal(t, "healthy", health.Health)
	assert.Equal(t, int64(3), health.NodeCount)

	// AC2: a removed cluster disappears.
	cache.Replace(map[string]*ClusterHealth{})
	assert.Nil(t, cache.Get("c1"))
}

func TestSnapshotFromJSON(t *testing.T) {
	body := []byte(`{"clusters":[{"cluster_id":"c1","health":"healthy","node_count":2,"last_checked_at":100,"nodes":[{"node_id":"n1","name":"node-1","status":"ready"}]}]}`)
	snapshot, err := snapshotFromJSON(body)
	require.NoError(t, err)
	require.Len(t, snapshot, 1)
	health := snapshot["c1"]
	require.NotNil(t, health)
	assert.Equal(t, "healthy", health.Health)
	require.Len(t, health.Nodes, 1)
	assert.Equal(t, "node-1", health.Nodes[0].Name)
}