package accelerator

import (
	"context"
	"encoding/json"

	"google.golang.org/grpc"

	acceleratorv1 "github.com/go-taas/go-taas/proto/taas/accelerator/v1"
	commonv1 "github.com/go-taas/go-taas/proto/taas/common/v1"

	"github.com/go-taas/go-taas/pkg/errors"
	"github.com/go-taas/go-taas/pkg/server"
	"github.com/go-taas/go-taas/services/image"
)

// ServiceName is the unique name of this service.
const ServiceName = "accelerator"

// Service implements the read-only accelerator inventory gRPC service
// (feature #18). It reads the in-memory projection cache; it is
// platform-scoped (no organization header required or honored, AD10).
type Service struct {
	acceleratorv1.UnimplementedAcceleratorServiceServer

	cache *ProjectionCache

	// warmupProvider is the injected narrow interface for warmup-task
	// context (Section 5.3). Nil until wired: no warmup context is
	// returned.
	warmupProvider WarmupTasksForNodeProvider
}

// New constructs the accelerator service over a fresh projection cache.
func New() *Service {
	return &Service{cache: NewProjectionCache()}
}

// NewWithCache constructs the accelerator service over a caller-provided
// cache. It is the injection point used by tests and FVT.
func NewWithCache(cache *ProjectionCache) *Service {
	return &Service{cache: cache}
}

// SetWarmupProvider installs the warmup-task context provider. It must
// be called before serving.
func (s *Service) SetWarmupProvider(p WarmupTasksForNodeProvider) {
	s.warmupProvider = p
}

// AttachToServer implements server.Service.
func (s *Service) AttachToServer(grpcServer *grpc.Server) {
	acceleratorv1.RegisterAcceleratorServiceServer(grpcServer, s)
}

// ServiceName implements server.Service.
func (s *Service) ServiceName() string { return ServiceName }

// GetServiceHandlerRegisterFn implements server.ServiceWithGateway.
func (s *Service) GetServiceHandlerRegisterFn() server.ServiceHandlerRegisterFn {
	return acceleratorv1.RegisterAcceleratorServiceHandler
}

// Migrate implements server.Migrator: no-op. The inventory is an
// in-memory projection (AD11); there is no schema.
func (s *Service) Migrate(_ context.Context) error { return nil }

// ListAcceleratorNodes returns the fleet list with vendor/health
// filters, node-name search, and pagination (AC1).
func (s *Service) ListAcceleratorNodes(_ context.Context, req *acceleratorv1.ListAcceleratorNodesRequest) (*acceleratorv1.ListAcceleratorNodesResponse, error) {
	offset, limit := normalizePage(req.GetPage())
	nodes, total, err := s.cache.List(req.GetVendor(), req.GetHealth(), req.GetSearch(), offset, limit)
	if err != nil {
		return nil, err
	}

	summaries := make([]*acceleratorv1.AcceleratorNodeSummary, 0, len(nodes))
	for _, n := range nodes {
		summaries = append(summaries, nodeToSummary(n))
	}
	return &acceleratorv1.ListAcceleratorNodesResponse{
		Response: okResponse(),
		Nodes:    summaries,
		//nolint:gosec // G115: limit is capped at 100 by normalizePage.
		PageMeta: &commonv1.PageMeta{Total: total, Offset: int64(offset), Limit: int32(limit)},
	}, nil
}

// GetAcceleratorNode returns one node's detail: per-GPU breakdown,
// labels, taints, resources, and warmup-task context (AC3). A missing
// node id returns 10208 CodeAcceleratorNodeNotFound (AD6).
func (s *Service) GetAcceleratorNode(ctx context.Context, req *acceleratorv1.GetAcceleratorNodeRequest) (*acceleratorv1.GetAcceleratorNodeResponse, error) {
	node, ok := s.cache.Get(req.GetNodeId())
	if !ok {
		return nil, errors.New(errors.CodeAcceleratorNodeNotFound)
	}

	resp := &acceleratorv1.GetAcceleratorNodeResponse{
		Response: okResponse(),
		Node:     nodeToDetail(node),
	}

	if s.warmupProvider != nil {
		// Warmup node_results mention the node by name (the Kubernetes
		// node name), so the narrow read searches by node name.
		tasks, err := s.warmupProvider.ListWarmupTasksForNode(ctx, node.Name, 20)
		if err != nil {
			return nil, err
		}
		for _, t := range tasks {
			resp.Node.WarmupTasks = append(resp.Node.WarmupTasks, &acceleratorv1.WarmupTaskRef{
				TaskId:      t.ID,
				State:       t.State,
				NodeOutcome: nodeOutcomeFor(t, node.Name),
			})
		}
	}
	return resp, nil
}

// ListCardTypeSummary returns the (vendor, card_type) capacity summary
// (AC2).
func (s *Service) ListCardTypeSummary(_ context.Context, _ *acceleratorv1.ListCardTypeSummaryRequest) (*acceleratorv1.ListCardTypeSummaryResponse, error) {
	rows := s.cache.CardTypeSummary()
	out := make([]*acceleratorv1.CardTypeSummary, 0, len(rows))
	for _, r := range rows {
		out = append(out, &acceleratorv1.CardTypeSummary{
			Vendor:    r.Vendor,
			CardType:  r.CardType,
			Total:     r.Total,
			Allocated: r.Allocated,
			Free:      r.Free,
			NodeCount: r.NodeCount,
		})
	}
	return &acceleratorv1.ListCardTypeSummaryResponse{
		Response:  okResponse(),
		CardTypes: out,
	}, nil
}

// nodeToSummary maps a cache node to its wire summary.
func nodeToSummary(n *AcceleratorNode) *acceleratorv1.AcceleratorNodeSummary {
	return &acceleratorv1.AcceleratorNodeSummary{
		NodeId:            n.NodeID,
		Name:              n.Name,
		Vendor:            n.Vendor,
		CardTypes:         n.CardTypes,
		GpusAllocated:     n.GPUsAllocated,
		GpusFree:          n.GPUsFree,
		DriverVersion:     n.DriverVersion,
		DevicePluginState: n.DevicePluginState,
		Readiness:         n.Readiness,
		Health:            n.Health,
		LastUpdatedAt:     n.LastUpdatedAt.Unix(),
	}
}

// nodeToDetail maps a cache node to its wire detail.
func nodeToDetail(n *AcceleratorNode) *acceleratorv1.AcceleratorNode {
	detail := &acceleratorv1.AcceleratorNode{
		Summary:     nodeToSummary(n),
		Labels:      n.Labels,
		Taints:      n.Taints,
		WarmupTasks: []*acceleratorv1.WarmupTaskRef{},
	}
	for _, g := range n.GPUs {
		detail.Gpus = append(detail.Gpus, &acceleratorv1.AcceleratorGPU{
			Index: g.Index, Model: g.Model, Allocated: g.Allocated, Free: g.Free, Note: g.Note,
		})
	}
	for _, r := range n.Resources {
		detail.Resources = append(detail.Resources, &acceleratorv1.AcceleratorResource{
			CardType: r.CardType, Allocatable: r.Allocatable, Allocated: r.Allocated,
		})
	}
	return detail
}

// normalizePage applies the pagination defaults: limit 20, cap 100,
// negative offset clamped to 0.
func normalizePage(page *commonv1.PageRequest) (offset, limit int) {
	offset = int(page.GetOffset())
	limit = int(page.GetLimit())
	if offset < 0 {
		offset = 0
	}
	if limit <= 0 {
		limit = 20
	}
	if limit > 100 {
		limit = 100
	}
	return offset, limit
}

// okResponse builds the success envelope.
func okResponse() *commonv1.Response {
	return &commonv1.Response{Code: 0, Message: "OK"}
}

// nodeOutcomeFor returns the per-node outcome of a warmup task for the
// given node name, or "" when the task has no result for the node.
func nodeOutcomeFor(t *image.WarmupTask, nodeName string) string {
	results := []image.NodeResult{}
	if len(t.NodeResults) > 0 {
		_ = json.Unmarshal(t.NodeResults, &results)
	}
	for _, r := range results {
		if r.Node == nodeName {
			return r.State
		}
	}
	return ""
}
