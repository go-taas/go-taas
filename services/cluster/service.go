package cluster

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
	"gorm.io/gorm"

	commonv1 "github.com/go-taas/go-taas/proto/taas/common/v1"
	clusterv1 "github.com/go-taas/go-taas/proto/taas/cluster/v1"

	apierrors "github.com/go-taas/go-taas/pkg/errors"
	"github.com/go-taas/go-taas/pkg/server"
)

// ServiceName is the unique name of this service.
const ServiceName = "cluster"

// organizationMetadataKey is the gRPC metadata key carrying the
// transitional caller organization.
const organizationMetadataKey = "x-organization-id"

// SessionOrgResolver resolves the session's active organization.
type SessionOrgResolver interface {
	SessionActiveOrg(ctx context.Context) (string, error)
}

// SessionUserResolver resolves the authenticated caller's user id.
type SessionUserResolver interface {
	SessionUserID(ctx context.Context) (string, error)
}

// RoleGuard enforces the minimum org role on the admin cluster RPCs.
type RoleGuard interface {
	RequireRole(ctx context.Context, orgID, userID, minRole string) error
}

const roleAdmin = "admin"

// WorkloadProvider lists the inference services placed on a cluster
// (AD7). It is implemented by the infer module and injected at wiring
// time.
type WorkloadProvider interface {
	// ListServicesByCluster returns the services placed on a cluster.
	ListServicesByCluster(ctx context.Context, clusterID string) ([]ClusterWorkload, error)
}

// ClusterWorkload is one service placed on a cluster.
type ClusterWorkload struct { //nolint:revive // cluster.ClusterWorkload is the documented domain name
	ServiceID string
	Name      string
	ModelID   string
	State     string
	CreatedAt int64
}

// Service implements the cluster gRPC service.
type Service struct {
	clusterv1.UnimplementedClusterServiceServer

	components server.Components

	// repo is the injection point used by tests and FVT.
	repo *ClusterRepository

	// cache is the in-memory cluster-health projection cache (AD4).
	cache *ProjectionCache

	// defaultClusterID is the platform default cluster for the deploy
	// form (AD8). Empty means the first active cluster.
	defaultClusterID string

	// sessionOrgResolver resolves the session's active organization.
	sessionOrgResolver SessionOrgResolver
	// sessionUserResolver resolves the caller's user id.
	sessionUserResolver SessionUserResolver
	// roleGuard gates the admin cluster RPCs.
	roleGuard RoleGuard
	// workloadProvider lists services placed on a cluster.
	workloadProvider WorkloadProvider
}

// New constructs the cluster service.
func New(components server.Components) *Service {
	return &Service{
		components: components,
		cache:      NewProjectionCache(),
	}
}

// SetDefaultClusterID overrides the platform default cluster.
func (s *Service) SetDefaultClusterID(v string) { s.defaultClusterID = v }

// SetSessionOrgResolver injects the session-organization resolver.
func (s *Service) SetSessionOrgResolver(r SessionOrgResolver) { s.sessionOrgResolver = r }

// SetSessionUserResolver injects the session-user resolver.
func (s *Service) SetSessionUserResolver(r SessionUserResolver) { s.sessionUserResolver = r }

// SetRoleGuard injects the org role guard.
func (s *Service) SetRoleGuard(g RoleGuard) { s.roleGuard = g }

// SetWorkloadProvider injects the workload provider.
func (s *Service) SetWorkloadProvider(p WorkloadProvider) { s.workloadProvider = p }

// NewForFVT constructs a cluster service bound to a caller-provided GORM
// database.
func NewForFVT(db *gorm.DB) *Service {
	return &Service{
		repo:  NewClusterRepository(db),
		cache: NewProjectionCache(),
	}
}

// MigrateSchemaForFVT applies the cluster schema onto a caller-provided
// database.
func MigrateSchemaForFVT(db *gorm.DB) error {
	return db.AutoMigrate(&Cluster{})
}

// AttachToServer implements server.Service.
func (s *Service) AttachToServer(grpcServer *grpc.Server) {
	clusterv1.RegisterClusterServiceServer(grpcServer, s)
}

// ServiceName implements server.Service.
func (s *Service) ServiceName() string { return ServiceName }

// GetServiceHandlerRegisterFn implements server.ServiceWithGateway.
func (s *Service) GetServiceHandlerRegisterFn() server.ServiceHandlerRegisterFn {
	return clusterv1.RegisterClusterServiceHandler
}

// Migrate implements server.Migrator.
func (s *Service) Migrate(ctx context.Context) error {
	db, err := s.gormDB()
	if err != nil {
		return err
	}
	return db.WithContext(ctx).AutoMigrate(&Cluster{})
}

// gormDB resolves the *gorm.DB from the wired repository or the shared
// components.
func (s *Service) gormDB() (*gorm.DB, error) {
	if s.repo != nil {
		return s.repo.db.DB(context.Background()), nil
	}
	if s.components == nil || s.components.DB() == nil {
		return nil, apierrors.Newf(apierrors.CodeInternal, "cluster: database component unavailable")
	}
	raw := s.components.DB().GormDB()
	db, ok := raw.(*gorm.DB)
	if !ok {
		return nil, apierrors.Newf(apierrors.CodeInternal, "cluster: unexpected database handle type %T", raw)
	}
	return db, nil
}

// repository lazily wires and returns the cluster repository.
func (s *Service) repository() (*ClusterRepository, error) {
	if s.repo != nil {
		return s.repo, nil
	}
	db, err := s.gormDB()
	if err != nil {
		return nil, err
	}
	s.repo = NewClusterRepository(db)
	return s.repo, nil
}

// resolveOrg returns the organization context for a read.
func (s *Service) resolveOrg(ctx context.Context) (string, error) {
	if s.sessionOrgResolver != nil {
		if org, err := s.sessionOrgResolver.SessionActiveOrg(ctx); err != nil {
			return "", err
		} else if org != "" {
			return org, nil
		}
	}
	return resolveOrganizationID(ctx)
}

func resolveOrganizationID(ctx context.Context) (string, error) {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return "", apierrors.New(apierrors.CodeUnauthorized)
	}
	values := md.Get(organizationMetadataKey)
	if len(values) == 0 || strings.TrimSpace(values[0]) == "" {
		return "", apierrors.New(apierrors.CodeUnauthorized)
	}
	return strings.TrimSpace(values[0]), nil
}

func (s *Service) resolveSessionActor(ctx context.Context) (string, bool, error) {
	if s.sessionUserResolver == nil {
		return "", false, nil
	}
	userID, err := s.sessionUserResolver.SessionUserID(ctx)
	if err == nil && userID != "" {
		return userID, true, nil
	}
	if err != nil && apierrors.CodeOf(err) != apierrors.CodeSessionInvalid {
		return "", false, err
	}
	return "", false, nil
}

func (s *Service) requireAdminRole(ctx context.Context, orgID string) error {
	if s.roleGuard == nil || orgID == "" {
		return nil
	}
	userID, hasSession, err := s.resolveSessionActor(ctx)
	if err != nil {
		return err
	}
	if !hasSession {
		return nil
	}
	return s.roleGuard.RequireRole(ctx, orgID, userID, roleAdmin)
}

// RegisterCluster registers an inference cluster (AC1).
func (s *Service) RegisterCluster(ctx context.Context, req *clusterv1.RegisterClusterRequest) (*clusterv1.RegisterClusterResponse, error) {
	orgID, err := s.resolveOrg(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.requireAdminRole(ctx, orgID); err != nil {
		return nil, err
	}
	name := strings.TrimSpace(req.GetName())
	region := strings.TrimSpace(req.GetRegion())
	kubeconfigRef := strings.TrimSpace(req.GetKubeconfigRef())
	if name == "" || len(name) > 128 {
		return nil, apierrors.New(apierrors.CodeClusterStateInvalid)
	}
	if !validKubeconfigRef(kubeconfigRef) {
		return nil, apierrors.New(apierrors.CodeClusterKubeconfigInvalid)
	}
	repo, err := s.repository()
	if err != nil {
		return nil, err
	}
	cluster := &Cluster{
		ID:            newUUID(),
		Name:          name,
		Region:        region,
		KubeconfigRef: kubeconfigRef,
		State:         ClusterStateActive,
		CreatedAt:     time.Now().UTC(),
	}
	if err := repo.CreateCluster(ctx, cluster); err != nil {
		return nil, err
	}
	return &clusterv1.RegisterClusterResponse{
		Response:  okResponse(),
		ClusterId: cluster.ID,
		State:     cluster.State,
	}, nil
}

// ListClusters returns the clusters with health and workload counts
// (AC1).
func (s *Service) ListClusters(ctx context.Context, _ *clusterv1.ListClustersRequest) (*clusterv1.ListClustersResponse, error) {
	orgID, err := s.resolveOrg(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.requireAdminRole(ctx, orgID); err != nil {
		return nil, err
	}
	repo, err := s.repository()
	if err != nil {
		return nil, err
	}
	rows, err := repo.ListClusters(ctx)
	if err != nil {
		return nil, err
	}
	clusters := make([]*clusterv1.ClusterSummary, 0, len(rows))
	for _, c := range rows {
		clusters = append(clusters, s.summary(ctx, &c))
	}
	return &clusterv1.ListClustersResponse{
		Response: okResponse(),
		Clusters: clusters,
		PageMeta: &commonv1.PageMeta{Total: int64(len(clusters))},
	}, nil
}

// GetCluster returns a cluster's full detail (AC2).
func (s *Service) GetCluster(ctx context.Context, req *clusterv1.GetClusterRequest) (*clusterv1.GetClusterResponse, error) {
	orgID, err := s.resolveOrg(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.requireAdminRole(ctx, orgID); err != nil {
		return nil, err
	}
	repo, err := s.repository()
	if err != nil {
		return nil, err
	}
	cluster, err := repo.FindClusterByID(ctx, strings.TrimSpace(req.GetClusterId()))
	if err != nil {
		return nil, err
	}
	summary := s.summary(ctx, cluster)
	nodes := []*clusterv1.ClusterNode{}
	if health := s.cache.Get(cluster.ID); health != nil {
		for _, n := range health.Nodes {
			nodes = append(nodes, &clusterv1.ClusterNode{
				NodeId: n.NodeID, Name: n.Name, Status: n.Status,
			})
		}
	}
	return &clusterv1.GetClusterResponse{
		Response: okResponse(),
		Cluster: &clusterv1.Cluster{
			Summary: summary,
			Nodes:   nodes,
		},
	}, nil
}

// GetClusterWorkloads returns the services placed on a cluster (AC2).
func (s *Service) GetClusterWorkloads(ctx context.Context, req *clusterv1.GetClusterWorkloadsRequest) (*clusterv1.GetClusterWorkloadsResponse, error) {
	orgID, err := s.resolveOrg(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.requireAdminRole(ctx, orgID); err != nil {
		return nil, err
	}
	repo, err := s.repository()
	if err != nil {
		return nil, err
	}
	clusterID := strings.TrimSpace(req.GetClusterId())
	if _, err := repo.FindClusterByID(ctx, clusterID); err != nil {
		return nil, err
	}
	workloads := []*clusterv1.ClusterWorkload{}
	if s.workloadProvider != nil {
		rows, err := s.workloadProvider.ListServicesByCluster(ctx, clusterID)
		if err != nil {
			return nil, err
		}
		for _, w := range rows {
			workloads = append(workloads, &clusterv1.ClusterWorkload{
				ServiceId: w.ServiceID, Name: w.Name, ModelId: w.ModelID, State: w.State, CreatedAt: w.CreatedAt,
			})
		}
	}
	return &clusterv1.GetClusterWorkloadsResponse{
		Response:  okResponse(),
		Workloads: workloads,
		PageMeta:  &commonv1.PageMeta{Total: int64(len(workloads))},
	}, nil
}

// DisableCluster sets a cluster disabled (AC3).
func (s *Service) DisableCluster(ctx context.Context, req *clusterv1.DisableClusterRequest) (*clusterv1.DisableClusterResponse, error) {
	orgID, err := s.resolveOrg(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.requireAdminRole(ctx, orgID); err != nil {
		return nil, err
	}
	repo, err := s.repository()
	if err != nil {
		return nil, err
	}
	clusterID := strings.TrimSpace(req.GetClusterId())
	if _, err := repo.FindClusterByID(ctx, clusterID); err != nil {
		return nil, err
	}
	if err := repo.UpdateClusterState(ctx, clusterID, ClusterStateDisabled); err != nil {
		return nil, err
	}
	return &clusterv1.DisableClusterResponse{Response: okResponse()}, nil
}

// summary assembles a cluster summary with health and workload counts.
func (s *Service) summary(ctx context.Context, c *Cluster) *clusterv1.ClusterSummary {
	summary := &clusterv1.ClusterSummary{
		ClusterId: c.ID,
		Name:      c.Name,
		Region:    c.Region,
		State:     c.State,
		Health:    "unknown",
	}
	if health := s.cache.Get(c.ID); health != nil {
		summary.Health = health.Health
		summary.NodeCount = health.NodeCount
		summary.LastCheckedAt = health.LastCheckedAt
	}
	if s.workloadProvider != nil {
		if rows, err := s.workloadProvider.ListServicesByCluster(ctx, c.ID); err == nil {
			summary.ServiceCount = int64(len(rows))
		}
	}
	return summary
}

// okResponse returns the standard success envelope.
func okResponse() *commonv1.Response {
	return &commonv1.Response{Code: 0, Message: "OK"}
}

// snapshotFromJSON parses a cluster.health snapshot body.
func snapshotFromJSON(body []byte) (map[string]*ClusterHealth, error) {
	var raw struct {
		Clusters []struct {
			ClusterID     string `json:"cluster_id"`
			Health        string `json:"health"`
			NodeCount     int64  `json:"node_count"`
			LastCheckedAt int64  `json:"last_checked_at"`
			Nodes         []struct {
				NodeID string `json:"node_id"`
				Name   string `json:"name"`
				Status string `json:"status"`
			} `json:"nodes"`
		} `json:"clusters"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, err
	}
	out := make(map[string]*ClusterHealth, len(raw.Clusters))
	for _, c := range raw.Clusters {
		health := &ClusterHealth{
			ClusterID:     c.ClusterID,
			Health:        c.Health,
			NodeCount:     c.NodeCount,
			LastCheckedAt: c.LastCheckedAt,
		}
		for _, n := range c.Nodes {
			health.Nodes = append(health.Nodes, ClusterNodeHealth{
				NodeID: n.NodeID, Name: n.Name, Status: n.Status,
			})
		}
		out[c.ClusterID] = health
	}
	return out, nil
}