package cluster

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
	"gorm.io/gorm"

	clusterv1 "github.com/go-taas/go-taas/proto/taas/cluster/v1"
	apierrors "github.com/go-taas/go-taas/pkg/errors"
	"github.com/go-taas/go-taas/pkg/server"
)

type fakeSessionOrgResolver struct{ org string }

func (f fakeSessionOrgResolver) SessionActiveOrg(context.Context) (string, error) {
	return f.org, nil
}

type fakeSessionUserResolver struct{ user string }

func (f fakeSessionUserResolver) SessionUserID(context.Context) (string, error) {
	return f.user, nil
}

type fakeRoleGuard struct{ allowed bool }

func (f fakeRoleGuard) RequireRole(_ context.Context, _, _, _ string) error {
	if !f.allowed {
		return apierrors.New(apierrors.CodeForbidden)
	}
	return nil
}

type fakeWorkloadProvider struct{ workloads []ClusterWorkload }

func (f fakeWorkloadProvider) ListServicesByCluster(context.Context, string) ([]ClusterWorkload, error) {
	return f.workloads, nil
}

func ctxWithOrg(ctx context.Context, org string) context.Context {
	return metadata.NewIncomingContext(ctx, metadata.Pairs("x-organization-id", org))
}

func newService(t *testing.T, db *gorm.DB, roleAllowed bool) *Service {
	t.Helper()
	svc := NewForFVT(db)
	svc.SetSessionOrgResolver(fakeSessionOrgResolver{org: "org-1"})
	svc.SetSessionUserResolver(fakeSessionUserResolver{user: "user-1"})
	svc.SetRoleGuard(fakeRoleGuard{allowed: roleAllowed})
	svc.SetWorkloadProvider(fakeWorkloadProvider{workloads: []ClusterWorkload{
		{ServiceID: "svc-1", Name: "demo", ModelID: "m1", State: "running", CreatedAt: 100},
	}})
	return svc
}

func TestRegisterAndListClusters(t *testing.T) {
	db := newTestDB(t)
	svc := newService(t, db, true)

	// AC1: register a cluster.
	resp, err := svc.RegisterCluster(ctxWithOrg(context.Background(), "org-1"), &clusterv1.RegisterClusterRequest{
		Name: "cluster-a", Region: "cn-north", KubeconfigRef: "kube/cluster-a",
	})
	require.NoError(t, err)
	assert.NotEmpty(t, resp.ClusterId)
	assert.Equal(t, ClusterStateActive, resp.State)

	// AC1: invalid kubeconfig ref -> 12404.
	_, err = svc.RegisterCluster(ctxWithOrg(context.Background(), "org-1"), &clusterv1.RegisterClusterRequest{
		Name: "cluster-b", KubeconfigRef: "/kube/../x",
	})
	require.Error(t, err)
	assert.Equal(t, apierrors.CodeClusterKubeconfigInvalid, apierrors.CodeOf(err))

	// AC1: list returns the cluster.
	list, err := svc.ListClusters(ctxWithOrg(context.Background(), "org-1"), &clusterv1.ListClustersRequest{})
	require.NoError(t, err)
	require.Len(t, list.Clusters, 1)
	assert.Equal(t, "cluster-a", list.Clusters[0].Name)
	assert.Equal(t, "unknown", list.Clusters[0].Health)
}

func TestGetClusterAndWorkloads(t *testing.T) {
	db := newTestDB(t)
	svc := newService(t, db, true)

	// Register a cluster.
	resp, err := svc.RegisterCluster(ctxWithOrg(context.Background(), "org-1"), &clusterv1.RegisterClusterRequest{
		Name: "cluster-a", Region: "cn-north", KubeconfigRef: "kube/cluster-a",
	})
	require.NoError(t, err)

	// Seed the projection cache.
	svc.cache.Replace(map[string]*ClusterHealth{
		resp.ClusterId: {ClusterID: resp.ClusterId, Health: "healthy", NodeCount: 2, LastCheckedAt: 100, Nodes: []ClusterNodeHealth{{NodeID: "n1", Name: "node-1", Status: "ready"}}},
	})

	// AC2: get the cluster detail.
	got, err := svc.GetCluster(ctxWithOrg(context.Background(), "org-1"), &clusterv1.GetClusterRequest{ClusterId: resp.ClusterId})
	require.NoError(t, err)
	assert.Equal(t, "healthy", got.Cluster.Summary.Health)
	assert.Equal(t, int64(2), got.Cluster.Summary.NodeCount)
	require.Len(t, got.Cluster.Nodes, 1)
	assert.Equal(t, "node-1", got.Cluster.Nodes[0].Name)

	// AC2: get the workloads.
	workloads, err := svc.GetClusterWorkloads(ctxWithOrg(context.Background(), "org-1"), &clusterv1.GetClusterWorkloadsRequest{ClusterId: resp.ClusterId})
	require.NoError(t, err)
	require.Len(t, workloads.Workloads, 1)
	assert.Equal(t, "demo", workloads.Workloads[0].Name)

	// AC2: unknown cluster -> 12401.
	_, err = svc.GetCluster(ctxWithOrg(context.Background(), "org-1"), &clusterv1.GetClusterRequest{ClusterId: "missing"})
	require.Error(t, err)
	assert.Equal(t, apierrors.CodeClusterNotFound, apierrors.CodeOf(err))
}

func TestDisableCluster(t *testing.T) {
	db := newTestDB(t)
	svc := newService(t, db, true)

	// Register a cluster.
	resp, err := svc.RegisterCluster(ctxWithOrg(context.Background(), "org-1"), &clusterv1.RegisterClusterRequest{
		Name: "cluster-a", KubeconfigRef: "kube/cluster-a",
	})
	require.NoError(t, err)

	// AC3: disable the cluster.
	_, err = svc.DisableCluster(ctxWithOrg(context.Background(), "org-1"), &clusterv1.DisableClusterRequest{ClusterId: resp.ClusterId})
	require.NoError(t, err)

	// AC3: idempotent.
	_, err = svc.DisableCluster(ctxWithOrg(context.Background(), "org-1"), &clusterv1.DisableClusterRequest{ClusterId: resp.ClusterId})
	require.NoError(t, err)

	// Verify the state.
	got, err := svc.GetCluster(ctxWithOrg(context.Background(), "org-1"), &clusterv1.GetClusterRequest{ClusterId: resp.ClusterId})
	require.NoError(t, err)
	assert.Equal(t, ClusterStateDisabled, got.Cluster.Summary.State)
}

func TestRoleDenied(t *testing.T) {
	db := newTestDB(t)
	svc := newService(t, db, false)
	// AC9: role denied -> 10036.
	_, err := svc.ListClusters(ctxWithOrg(context.Background(), "org-1"), &clusterv1.ListClustersRequest{})
	require.Error(t, err)
	assert.Equal(t, apierrors.CodeForbidden, apierrors.CodeOf(err))
}

func TestServiceWiring(t *testing.T) {
	db := newTestDB(t)
	svc := NewForFVT(db)
	assert.Equal(t, ServiceName, svc.ServiceName())
	grpcSrv := grpc.NewServer()
	svc.AttachToServer(grpcSrv)
	assert.NotNil(t, svc.GetServiceHandlerRegisterFn())
	assert.NoError(t, svc.Migrate(context.Background()))
	assert.NotNil(t, grpcSrv.GetServiceInfo()["taas.cluster.v1.ClusterService"])
}

func TestMigrateSchemaForFVT(t *testing.T) {
	db := newTestDB(t)
	require.NoError(t, MigrateSchemaForFVT(db))
	assert.True(t, db.Migrator().HasTable(&Cluster{}))
}

func TestTransitionalPath(t *testing.T) {
	db := newTestDB(t)
	svc := NewForFVT(db)
	resp, err := svc.ListClusters(ctxWithOrg(context.Background(), "org-1"), &clusterv1.ListClustersRequest{})
	require.NoError(t, err)
	assert.Empty(t, resp.Clusters)
}

func TestNoOrg(t *testing.T) {
	db := newTestDB(t)
	svc := NewForFVT(db)
	_, err := svc.ListClusters(context.Background(), &clusterv1.ListClustersRequest{})
	require.Error(t, err)
	assert.Equal(t, apierrors.CodeUnauthorized, apierrors.CodeOf(err))
}

// fakeComponents provides a DB component for the production wiring path.
type fakeComponents struct {
	db *gorm.DB
	mq server.MQComponent
}

func (f *fakeComponents) DB() server.DBComponent       { return &fakeDBComponent{db: f.db} }
func (f *fakeComponents) Redis() server.RedisComponent { return nil }
func (f *fakeComponents) MQ() server.MQComponent       { return f.mq }

type fakeDBComponent struct{ db *gorm.DB }

func (f *fakeDBComponent) GormDB() any { return f.db }

// TestProductionConstructor exercises the production New constructor and
// Migrate through the shared components.
func TestProductionConstructor(t *testing.T) {
	db := newTestDB(t)
	svc := New(&fakeComponents{db: db})
	assert.Equal(t, ServiceName, svc.ServiceName())
	require.NoError(t, svc.Migrate(context.Background()))
	svc.SetDefaultClusterID("cluster-1")
	assert.Equal(t, "cluster-1", svc.defaultClusterID)

	// Exercise the production repository path (via components).
	svc.repo = nil
	_, err := svc.repository()
	require.NoError(t, err)
}

// TestDisableClusterUnknown verifies disabling an unknown cluster
// returns 12401.
func TestDisableClusterUnknown(t *testing.T) {
	db := newTestDB(t)
	svc := newService(t, db, true)
	_, err := svc.DisableCluster(ctxWithOrg(context.Background(), "org-1"), &clusterv1.DisableClusterRequest{ClusterId: "missing"})
	require.Error(t, err)
	assert.Equal(t, apierrors.CodeClusterNotFound, apierrors.CodeOf(err))
}

// TestRegisterClusterEmptyName verifies the empty-name branch.
func TestRegisterClusterEmptyName(t *testing.T) {
	db := newTestDB(t)
	svc := newService(t, db, true)
	_, err := svc.RegisterCluster(ctxWithOrg(context.Background(), "org-1"), &clusterv1.RegisterClusterRequest{
		Name: "", KubeconfigRef: "kube/x",
	})
	require.Error(t, err)
	assert.Equal(t, apierrors.CodeClusterStateInvalid, apierrors.CodeOf(err))
}