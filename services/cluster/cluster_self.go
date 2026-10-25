package cluster

import (
	"context"
	"strings"
	"time"

	apierrors "github.com/go-taas/go-taas/pkg/errors"
	"github.com/go-taas/go-taas/pkg/logger"
	clusterv1 "github.com/go-taas/go-taas/proto/taas/cluster/v1"
)

// SelfClusterInfo describes the cluster the platform itself runs in, as
// read from the in-cluster Kubernetes environment.
type SelfClusterInfo struct {
	// Name is a stable cluster name: the explicit configuration value
	// when set, else a name derived from the cluster identity.
	Name string
	// Region is the topology region/zone of the nodes, empty when the
	// cluster carries no topology labels.
	Region string
	// NodeCount is the number of nodes in the cluster.
	NodeCount int64
	// AcceleratorLabels are the accelerator label keys present on the
	// cluster's nodes (e.g. "nvidia.com/gpu.product"), used by the
	// console to offer valid image-warmup node selectors.
	AcceleratorLabels []string
}

// SelfClusterProvider reads the running cluster's identity from the
// Kubernetes API. It is implemented in the server wiring layer (which
// owns the cluster's client) and injected here, keeping the cluster
// module free of a Kubernetes dependency. Nil until wired: discovery
// fails closed with 10402.
type SelfClusterProvider interface {
	// SelfCluster returns the running cluster's identity.
	SelfCluster(ctx context.Context) (*SelfClusterInfo, error)
}

// SetSelfClusterProvider injects the in-cluster identity provider used
// by DiscoverCluster.
func (s *Service) SetSelfClusterProvider(p SelfClusterProvider) { s.selfCluster = p }

// DiscoverCluster registers or refreshes the cluster the platform itself
// runs in. It is idempotent: the local row is updated in place, so
// restarting the server (or clicking "discover" again) converges on one
// row. The row is marked Local so the console can distinguish it from
// manually registered remote clusters.
func (s *Service) DiscoverCluster(ctx context.Context, _ *clusterv1.DiscoverClusterRequest) (*clusterv1.DiscoverClusterResponse, error) {
	orgID, err := s.resolveOrg(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.requireAdminRole(ctx, orgID); err != nil {
		return nil, err
	}
	return s.discoverLocalCluster(ctx)
}

// discoverLocalCluster is the discovery core shared by the RPC and the
// startup hook. It does not check the caller's role (the startup hook
// has no session).
func (s *Service) discoverLocalCluster(ctx context.Context) (*clusterv1.DiscoverClusterResponse, error) {
	if s.selfCluster == nil {
		return nil, apierrors.New(apierrors.CodeClusterNotFound)
	}
	info, err := s.selfCluster.SelfCluster(ctx)
	if err != nil {
		return nil, apierrors.Newf(apierrors.CodeInternal, "cluster: discover the running cluster failed: %v", err)
	}
	repo, err := s.repository()
	if err != nil {
		return nil, err
	}
	row, err := repo.UpsertLocalCluster(ctx, &Cluster{
		Name:          info.Name,
		Region:        info.Region,
		KubeconfigRef: localKubeconfigRef,
		State:         ClusterStateActive,
		Local:         true,
		NodeLabels:    strings.Join(info.AcceleratorLabels, ","),
		CreatedAt:     time.Now().UTC(),
	})
	if err != nil {
		return nil, err
	}
	// A freshly discovered cluster has no health snapshot yet; seed the
	// node count from the live API so the page is informative immediately.
	s.cache.Set(row.ID, &ClusterHealth{
		ClusterID:     row.ID,
		Health:        "healthy",
		NodeCount:     info.NodeCount,
		LastCheckedAt: time.Now().UTC().Unix(),
	})
	summary := s.summary(ctx, row)
	return &clusterv1.DiscoverClusterResponse{
		Response: okResponse(),
		Cluster:  summary,
	}, nil
}

// localKubeconfigRef is the stored kubeconfig reference of the local
// cluster: it is served by the platform's own service account, not a
// file. It satisfies the reference syntax (non-empty, relative, no "..").
const localKubeconfigRef = "in-cluster"

// SelfDiscoveryRefresh is how often the local cluster row is refreshed
// from the live cluster (node count and accelerator labels change as
// nodes join or are labelled).
const SelfDiscoveryRefresh = 10 * time.Minute

// NewSelfDiscoveryRunner returns the startup runner that discovers the
// running cluster once and then refreshes it periodically. Failures are
// logged and retried, never fatal: a cluster API hiccup must not stop the
// control plane.
func NewSelfDiscoveryRunner(svc *Service) *SelfDiscoveryRunner {
	return &SelfDiscoveryRunner{svc: svc}
}

// SelfDiscoveryRunner is the startup local-cluster discovery runner.
type SelfDiscoveryRunner struct {
	svc *Service
}

// Run implements server.Runner.
func (r *SelfDiscoveryRunner) Run(ctx context.Context) error {
	if r.svc == nil || r.svc.selfCluster == nil {
		return nil
	}
	r.discover(ctx)
	ticker := time.NewTicker(SelfDiscoveryRefresh)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			r.discover(ctx)
		}
	}
}

// discover runs one discovery pass, logging instead of failing.
func (r *SelfDiscoveryRunner) discover(ctx context.Context) {
	resp, err := r.svc.discoverLocalCluster(ctx)
	if err != nil {
		logger.S().Warnw("cluster: local cluster discovery failed", "err", err)
		return
	}
	logger.S().Infow("cluster: local cluster discovered",
		"cluster_id", resp.GetCluster().GetClusterId(),
		"name", resp.GetCluster().GetName(),
		"nodes", resp.GetCluster().GetNodeCount())
}
