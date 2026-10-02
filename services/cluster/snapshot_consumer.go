package cluster

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/go-taas/go-taas/pkg/logger"
	"github.com/go-taas/go-taas/pkg/mq"
	"github.com/go-taas/go-taas/pkg/server"
)

// clusterSnapshot is the full cluster-health snapshot published by the
// Controller on cluster.health (AD4).
type clusterSnapshot struct {
	Clusters []snapshotCluster `json:"clusters"`
}

// snapshotCluster is one cluster's health in a snapshot.
type snapshotCluster struct {
	ClusterID     string           `json:"cluster_id"`
	Health        string           `json:"health"`
	NodeCount     int64            `json:"node_count"`
	LastCheckedAt int64            `json:"last_checked_at"`
	Nodes         []snapshotNode   `json:"nodes"`
}

// snapshotNode is one node's health in a cluster.
type snapshotNode struct {
	NodeID string `json:"node_id"`
	Name   string `json:"name"`
	Status string `json:"status"`
}

// SnapshotConsumer subscribes to the cluster.health subject and applies
// each full snapshot to the projection cache. It implements
// server.Runner so the framework starts it with the server and cancels
// it on shutdown.
type SnapshotConsumer struct {
	client mq.Client
	cache  *ProjectionCache
}

// NewSnapshotConsumer builds a SnapshotConsumer.
func NewSnapshotConsumer(client mq.Client, cache *ProjectionCache) *SnapshotConsumer {
	return &SnapshotConsumer{client: client, cache: cache}
}

// NewSnapshotConsumerRunner builds the SnapshotConsumer from the shared
// server components over the caller-provided projection cache. It
// returns nil when the required components are unavailable.
func NewSnapshotConsumerRunner(components server.Components, cache *ProjectionCache) *SnapshotConsumer {
	if components == nil {
		return nil
	}
	if components.MQ() == nil {
		logger.S().Warn("cluster: snapshot consumer disabled, mq component unavailable")
		return nil
	}
	client, ok := components.MQ().Client().(mq.Client)
	if !ok {
		logger.S().Fatalw("cluster: unexpected mq handle type",
			"type", fmt.Sprintf("%T", components.MQ().Client()))
		return nil
	}
	return NewSnapshotConsumer(client, cache)
}

// Run implements server.Runner: it subscribes to the cluster.health
// subject and blocks until ctx is cancelled or the subscription fails.
func (c *SnapshotConsumer) Run(ctx context.Context) error {
	return c.client.Subscribe(ctx, mq.DefaultSubjects().ClusterHealth, func(msg mq.Message) error {
		return c.handle(ctx, msg)
	})
}

// handle applies one full snapshot. A malformed snapshot is logged and
// skipped. The cache is replaced wholesale.
func (c *SnapshotConsumer) handle(_ context.Context, msg mq.Message) error {
	var snap clusterSnapshot
	if err := json.Unmarshal(msg.Body, &snap); err != nil {
		logger.S().Warnw("cluster: malformed health snapshot, skipping",
			"subject", msg.Subject, "err", err)
		return nil
	}
	byID := make(map[string]*ClusterHealth, len(snap.Clusters))
	for _, sc := range snap.Clusters {
		if sc.ClusterID == "" {
			continue
		}
		health := &ClusterHealth{
			ClusterID:     sc.ClusterID,
			Health:        sc.Health,
			NodeCount:     sc.NodeCount,
			LastCheckedAt: sc.LastCheckedAt,
		}
		for _, n := range sc.Nodes {
			health.Nodes = append(health.Nodes, ClusterNodeHealth(n))
		}
		byID[sc.ClusterID] = health
	}
	c.cache.Replace(byID)
	logger.S().Infow("cluster: health snapshot applied", "clusters", len(byID))
	return nil
}