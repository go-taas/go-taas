package accelerator

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/go-taas/go-taas/pkg/config"
	"github.com/go-taas/go-taas/pkg/logger"
	"github.com/go-taas/go-taas/pkg/mq"
	"github.com/go-taas/go-taas/pkg/server"
)

// inventorySnapshot is the full fleet snapshot published by the
// Controller on accelerator.inventory (AD1). It carries the raw signals;
// the composite health is computed here at ingestion (AD2).
type inventorySnapshot struct {
	Nodes      []snapshotNode `json:"nodes"`
	ReportedAt time.Time      `json:"reported_at"`
}

// snapshotNode is one node's raw signals in a snapshot.
type snapshotNode struct {
	NodeID            string             `json:"node_id"`
	Name              string             `json:"name"`
	Vendor            string             `json:"vendor"`
	CardTypes         []string           `json:"card_types"`
	GPUsAllocated     int64              `json:"gpus_allocated"`
	GPUsFree          int64              `json:"gpus_free"`
	DriverVersion     string             `json:"driver_version"`
	DevicePluginState string             `json:"device_plugin_state"`
	Readiness         string             `json:"readiness"`
	GPUs              []snapshotGPU      `json:"gpus"`
	Labels            map[string]string  `json:"labels"`
	Taints            []string           `json:"taints"`
	Resources         []snapshotResource `json:"resources"`
}

// snapshotGPU is one GPU's raw signal.
type snapshotGPU struct {
	Index     int64  `json:"index"`
	Model     string `json:"model"`
	Allocated bool   `json:"allocated"`
	Free      bool   `json:"free"`
	Note      string `json:"note"`
}

// snapshotResource is one card type's allocatable vs allocated.
type snapshotResource struct {
	CardType    string `json:"card_type"`
	Allocatable int64  `json:"allocatable"`
	Allocated   int64  `json:"allocated"`
}

// SnapshotConsumer subscribes to the accelerator.inventory subject and
// applies each full snapshot to the projection cache. It implements
// server.Runner so the framework starts it with the server and cancels
// it on shutdown.
type SnapshotConsumer struct {
	client  mq.Client
	cache   *ProjectionCache
	workers int
}

// NewSnapshotConsumer builds a SnapshotConsumer. workers <= 0 falls back
// to 1.
func NewSnapshotConsumer(client mq.Client, cache *ProjectionCache, workers int) *SnapshotConsumer {
	if workers <= 0 {
		workers = 1
	}
	return &SnapshotConsumer{client: client, cache: cache, workers: workers}
}

// NewSnapshotConsumerRunner builds the SnapshotConsumer from the shared
// server components over the caller-provided projection cache. The cache
// must be the same instance the accelerator service reads from, so the
// consumer's Replace() populates the cache the RPCs serve (BUG-ACCEL-001).
// It returns nil when the consumer is disabled or the required components
// are unavailable, so callers can pass the result straight to AddRunner.
func NewSnapshotConsumerRunner(components server.Components, cache *ProjectionCache) *SnapshotConsumer {
	if components == nil {
		return nil
	}
	cfg := config.GetConfig()
	if !cfg.Accelerator.SnapshotConsumer.Enabled {
		return nil
	}
	if components.MQ() == nil {
		logger.S().Warn("accelerator: snapshot consumer disabled, mq component unavailable")
		return nil
	}
	client, ok := components.MQ().Client().(mq.Client)
	if !ok {
		logger.S().Fatalw("accelerator: unexpected mq handle type",
			"type", fmt.Sprintf("%T", components.MQ().Client()))
		return nil
	}
	return NewSnapshotConsumer(client, cache, cfg.Accelerator.SnapshotConsumer.Workers)
}

// Run implements server.Runner: it subscribes to the accelerator
// inventory subject and blocks until ctx is cancelled or the
// subscription fails.
func (c *SnapshotConsumer) Run(ctx context.Context) error {
	return c.client.Subscribe(ctx, mq.DefaultSubjects().AcceleratorInventory, func(msg mq.Message) error {
		return c.handle(ctx, msg)
	})
}

// handle applies one full snapshot. A malformed snapshot is logged and
// skipped (never retried — a malformed snapshot never becomes
// well-formed). The composite health is computed per node and the cache
// is replaced wholesale.
func (c *SnapshotConsumer) handle(_ context.Context, msg mq.Message) error {
	var snap inventorySnapshot
	if err := json.Unmarshal(msg.Body, &snap); err != nil {
		logger.S().Warnw("accelerator: malformed inventory snapshot, skipping",
			"subject", msg.Subject, "err", err)
		return nil
	}

	nodes := make(map[string]*AcceleratorNode, len(snap.Nodes))
	for _, sn := range snap.Nodes {
		if sn.NodeID == "" {
			continue
		}
		node := &AcceleratorNode{
			NodeID:            sn.NodeID,
			Name:              sn.Name,
			Vendor:            sn.Vendor,
			CardTypes:         sn.CardTypes,
			GPUsAllocated:     sn.GPUsAllocated,
			GPUsFree:          sn.GPUsFree,
			DriverVersion:     sn.DriverVersion,
			DevicePluginState: sn.DevicePluginState,
			Readiness:         sn.Readiness,
			Labels:            sn.Labels,
			Taints:            sn.Taints,
			LastUpdatedAt:     snap.ReportedAt,
		}
		for _, g := range sn.GPUs {
			node.GPUs = append(node.GPUs, AcceleratorGPU(g))
		}
		for _, r := range sn.Resources {
			node.Resources = append(node.Resources, AcceleratorResource(r))
		}
		node.Health = computeHealth(node)
		nodes[node.NodeID] = node
	}

	c.cache.Replace(nodes)
	logger.S().Infow("accelerator: inventory snapshot applied",
		"nodes", len(nodes), "reported_at", snap.ReportedAt)
	return nil
}

// computeHealth derives the composite health from the three raw signals
// (AD2, Section 4.3). A node with vendor "unspecified" has no
// device-plugin or driver signals; its health is based on node readiness
// alone (design FR4.2).
func computeHealth(n *AcceleratorNode) string {
	if n.Vendor == VendorUnspecified {
		switch n.Readiness {
		case ReadinessReady:
			return HealthHealthy
		case ReadinessNotReady:
			return HealthDegraded
		default:
			return HealthUnknown
		}
	}
	// Any unknown signal -> unknown.
	if n.Readiness == ReadinessUnknown || n.DevicePluginState == DevicePluginUnknown {
		return HealthUnknown
	}
	// Healthy only when all three hold.
	if n.Readiness == ReadinessReady &&
		n.DevicePluginState == DevicePluginHealthy &&
		n.DriverVersion != "" {
		return HealthHealthy
	}
	return HealthDegraded
}
