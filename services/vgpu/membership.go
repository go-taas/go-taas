package vgpu

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"

	"github.com/go-taas/go-taas/pkg/config"
	"github.com/go-taas/go-taas/pkg/logger"
	"github.com/go-taas/go-taas/pkg/mq"
	"github.com/go-taas/go-taas/pkg/server"
)

// acceleratorNode is one node's projection in the membership cache. It
// mirrors the accelerator-inventory snapshot's node shape (feature #18,
// AD12).
type acceleratorNode struct {
	NodeID string            `json:"node_id"`
	Name   string            `json:"name"`
	Labels map[string]string `json:"labels"`
	GPUs   []acceleratorGPU  `json:"gpus"`
	Health string            `json:"health"`
}

// acceleratorGPU is one physical GPU's slice allocation (feature #52,
// FR3.1). The slice allocation is carried in the inventory snapshot by
// the Controller from the HAMI-reported device state.
type acceleratorGPU struct {
	Index   int64             `json:"index"`
	Model   string            `json:"model"`
	Slices  []sliceAllocation `json:"slices"`
	Allocated bool            `json:"allocated"`
	Free    bool              `json:"free"`
}

// sliceAllocation is one allocated slice on a physical GPU (feature
// #52, FR3.1).
type sliceAllocation struct {
	TemplateName string `json:"template_name"`
	ServiceID    string `json:"service_id"`
	MemoryGiB    int64  `json:"memory_gib"`
	ComputePct   int64  `json:"compute_pct"`
}

// inventorySnapshot is the full fleet snapshot published by the
// Controller on accelerator.inventory (feature #18, AD1). The vgpu
// module consumes the same subject to maintain its membership/capacity
// projection (AD12).
type inventorySnapshot struct {
	Nodes []acceleratorNode `json:"nodes"`
}

// MembershipProjection is the pool membership/capacity projection
// (feature #52, AD12). It holds a sync.RWMutex-guarded map of nodes
// keyed by node id, replaced wholesale on each inventory snapshot.
type MembershipProjection struct {
	mu    sync.RWMutex
	nodes map[string]*acceleratorNode
}

// NewMembershipProjection constructs an empty projection.
func NewMembershipProjection() *MembershipProjection {
	return &MembershipProjection{nodes: make(map[string]*acceleratorNode)}
}

// Replace swaps the whole projection under a write lock (AD12).
func (p *MembershipProjection) Replace(nodes map[string]*acceleratorNode) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.nodes = nodes
}

// NodeCount returns the number of nodes whose labels contain the pool's
// selector key/value (feature #52, FR1.3).
func (p *MembershipProjection) NodeCount(_ string, labelKey, labelValue string) int64 {
	p.mu.RLock()
	defer p.mu.RUnlock()
	var count int64
	for _, n := range p.nodes {
		if n.Labels[labelKey] == labelValue {
			count++
		}
	}
	return count
}

// Nodes returns the member nodes of a pool with their GPUs and per-GPU
// slice allocation (feature #52, FR3.1).
func (p *MembershipProjection) Nodes(_ string, labelKey, labelValue string) []*acceleratorNode { //nolint:revive // the node type is package-internal
	p.mu.RLock()
	defer p.mu.RUnlock()
	var out []*acceleratorNode
	for _, n := range p.nodes {
		if n.Labels[labelKey] == labelValue {
			out = append(out, n)
		}
	}
	return out
}

// TemplateAllocation aggregates allocated/free per template across the
// pool's GPUs (feature #52, FR3.2).
func (p *MembershipProjection) TemplateAllocation(_ string, labelKey, labelValue string) map[string]TemplateAllocation {
	p.mu.RLock()
	defer p.mu.RUnlock()
	agg := make(map[string]TemplateAllocation)
	for _, n := range p.nodes {
		if n.Labels[labelKey] != labelValue {
			continue
		}
		for _, g := range n.GPUs {
			for _, s := range g.Slices {
				row, ok := agg[s.TemplateName]
				if !ok {
					row = TemplateAllocation{TemplateName: s.TemplateName}
				}
				row.Allocated++
				agg[s.TemplateName] = row
			}
		}
	}
	return agg
}

// Utilization returns the allocated memory / total pool memory as a
// percentage (feature #52, FR3.2). It returns 0 when the pool has no
// GPUs.
func (p *MembershipProjection) Utilization(_ string, labelKey, labelValue string) int64 {
	p.mu.RLock()
	defer p.mu.RUnlock()
	var totalMem, allocatedMem int64
	for _, n := range p.nodes {
		if n.Labels[labelKey] != labelValue {
			continue
		}
		for _, g := range n.GPUs {
			for _, s := range g.Slices {
				allocatedMem += s.MemoryGiB
			}
		}
	}
	if totalMem <= 0 {
		return 0
	}
	return allocatedMem * 100 / totalMem
}

// MembershipConsumer subscribes to the accelerator.inventory subject and
// applies each full snapshot to the membership projection. It implements
// server.Runner so the framework starts it with the server and cancels
// it on shutdown.
type MembershipConsumer struct {
	client mq.Client
	proj   *MembershipProjection
}

// NewMembershipConsumer builds a MembershipConsumer.
func NewMembershipConsumer(client mq.Client, proj *MembershipProjection) *MembershipConsumer {
	return &MembershipConsumer{client: client, proj: proj}
}

// NewMembershipConsumerRunner builds the MembershipConsumer from the
// shared server components over the caller-provided projection. It
// returns nil when the required components are unavailable, so callers
// can pass the result straight to AddRunner.
func NewMembershipConsumerRunner(components server.Components, proj *MembershipProjection) *MembershipConsumer {
	if components == nil {
		return nil
	}
	cfg := config.GetConfig()
	if !cfg.Accelerator.SnapshotConsumer.Enabled {
		return nil
	}
	if components.MQ() == nil {
		logger.S().Warn("vgpu: membership consumer disabled, mq component unavailable")
		return nil
	}
	client, ok := components.MQ().Client().(mq.Client)
	if !ok {
		logger.S().Fatalw("vgpu: unexpected mq handle type",
			"type", fmt.Sprintf("%T", components.MQ().Client()))
		return nil
	}
	return NewMembershipConsumer(client, proj)
}

// Run implements server.Runner: it subscribes to the accelerator
// inventory subject and blocks until ctx is cancelled or the
// subscription fails.
func (c *MembershipConsumer) Run(ctx context.Context) error {
	return c.client.Subscribe(ctx, mq.DefaultSubjects().AcceleratorInventory, func(msg mq.Message) error {
		return c.handle(ctx, msg)
	})
}

// handle applies one full snapshot. A malformed snapshot is logged and
// skipped (never retried).
func (c *MembershipConsumer) handle(_ context.Context, msg mq.Message) error {
	var snap inventorySnapshot
	if err := json.Unmarshal(msg.Body, &snap); err != nil {
		logger.S().Warnw("vgpu: malformed inventory snapshot, skipping",
			"subject", msg.Subject, "err", err)
		return nil
	}
	nodes := make(map[string]*acceleratorNode, len(snap.Nodes))
	for _, n := range snap.Nodes {
		if n.NodeID == "" {
			continue
		}
		node := n
		nodes[node.NodeID] = &node
	}
	c.proj.Replace(nodes)
	logger.S().Infow("vgpu: membership projection applied",
		"nodes", len(nodes))
	return nil
}