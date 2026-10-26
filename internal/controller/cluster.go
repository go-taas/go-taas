package controller

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
	"gorm.io/gorm"

	"github.com/go-taas/go-taas/pkg/logger"
	"github.com/go-taas/go-taas/pkg/mq"
)

// localKubeconfigRef is the stored kubeconfig reference of the local
// cluster: it is served by the platform's own service account, not a
// file. It mirrors the cluster module's constant.
const localKubeconfigRef = "in-cluster"

// clusterHealthNode is one node's health in the published snapshot
// (feature #40, AD4). It mirrors the cluster module's snapshotNode.
type clusterHealthNode struct {
	NodeID string `json:"node_id"`
	Name   string `json:"name"`
	Status string `json:"status"`
}

// clusterHealthEntry is one cluster's health in the published snapshot.
type clusterHealthEntry struct {
	ClusterID     string              `json:"cluster_id"`
	Health        string              `json:"health"`
	NodeCount     int64               `json:"node_count"`
	LastCheckedAt int64               `json:"last_checked_at"`
	Nodes         []clusterHealthNode `json:"nodes"`
}

// clusterHealthSnapshot is the full cluster-health snapshot published on
// cluster.health (feature #40, AD4).
type clusterHealthSnapshot struct {
	Clusters []clusterHealthEntry `json:"clusters"`
}

// registeredCluster is one row of the clusters table read by the
// collector.
type registeredCluster struct {
	ID            string
	KubeconfigRef string
	State         string
}

// ClusterCollector lists each registered cluster's nodes on an interval
// and publishes a full snapshot to cluster.health (feature #40, AD4). It
// is best-effort telemetry: transient Kubernetes errors are logged, the
// last-known projection is retained by the consumer, and the loop retries
// on the next tick.
type ClusterCollector struct {
	db        *gorm.DB
	clientset kubernetes.Interface
	publisher mq.Client
	interval  time.Duration
	// kubeconfigDir is the base directory remote kubeconfig references
	// are resolved against. Empty disables remote-cluster collection.
	kubeconfigDir string
}

// NewClusterCollector builds a ClusterCollector over the clusters table,
// the controller's own clientset (for the local cluster), and a
// publish-capable MQ client.
func NewClusterCollector(db *gorm.DB, clientset kubernetes.Interface, publisher mq.Client, interval time.Duration, kubeconfigDir string) *ClusterCollector {
	if interval <= 0 {
		interval = 30 * time.Second
	}
	return &ClusterCollector{
		db:            db,
		clientset:     clientset,
		publisher:     publisher,
		interval:      interval,
		kubeconfigDir: kubeconfigDir,
	}
}

// Run collects and publishes the full snapshot on the interval until ctx
// is cancelled.
func (c *ClusterCollector) Run(ctx context.Context) {
	ticker := time.NewTicker(c.interval)
	defer ticker.Stop()

	// Publish once immediately so the cache is populated without
	// waiting a full interval.
	c.collectAndPublish(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			c.collectAndPublish(ctx)
		}
	}
}

// collectAndPublish lists each registered cluster's nodes and publishes
// the full snapshot. Errors are logged; the loop retries on the next
// tick.
func (c *ClusterCollector) collectAndPublish(ctx context.Context) {
	clusters, err := c.listRegisteredClusters(ctx)
	if err != nil {
		logger.S().Warnw("controller: cluster health collection failed to list clusters", "err", err)
		return
	}
	snap := clusterHealthSnapshot{Clusters: make([]clusterHealthEntry, 0, len(clusters))}
	for _, cl := range clusters {
		if cl.State != "active" {
			continue
		}
		entry := c.collectCluster(ctx, cl)
		snap.Clusters = append(snap.Clusters, entry)
	}
	body, err := json.Marshal(snap)
	if err != nil {
		logger.S().Warnw("controller: marshal cluster health snapshot failed", "err", err)
		return
	}
	if err := c.publisher.Publish(ctx, mq.DefaultSubjects().ClusterHealth, body, nil); err != nil {
		logger.S().Warnw("controller: publish cluster health snapshot failed", "err", err)
		return
	}
	logger.S().Infow("controller: cluster health snapshot published", "clusters", len(snap.Clusters))
}

// collectCluster lists one cluster's nodes and builds its health entry.
func (c *ClusterCollector) collectCluster(ctx context.Context, cl registeredCluster) clusterHealthEntry {
	entry := clusterHealthEntry{
		ClusterID:     cl.ID,
		Health:        "unknown",
		LastCheckedAt: time.Now().UTC().Unix(),
	}
	client, err := c.clientFor(cl)
	if err != nil {
		logger.S().Warnw("controller: cluster health collection client unavailable",
			"cluster_id", cl.ID, "err", err)
		return entry
	}
	nodes, err := client.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	if err != nil {
		logger.S().Warnw("controller: cluster health collection failed to list nodes",
			"cluster_id", cl.ID, "err", err)
		return entry
	}
	entry.NodeCount = int64(len(nodes.Items))
	entry.Health = "healthy"
	entry.Nodes = make([]clusterHealthNode, 0, len(nodes.Items))
	for _, n := range nodes.Items {
		entry.Nodes = append(entry.Nodes, clusterHealthNode{
			NodeID: string(n.UID),
			Name:   n.Name,
			Status: nodeStatus(n),
		})
	}
	logger.S().Infow("controller: cluster health collected",
		"cluster_id", cl.ID, "node_count", entry.NodeCount, "nodes", len(entry.Nodes))
	return entry
}

// clientFor returns the Kubernetes client for a registered cluster. The
// local cluster uses the controller's own clientset; a remote cluster
// resolves its kubeconfig reference against the configured base
// directory.
func (c *ClusterCollector) clientFor(cl registeredCluster) (kubernetes.Interface, error) {
	if cl.KubeconfigRef == "" || cl.KubeconfigRef == localKubeconfigRef {
		return c.clientset, nil
	}
	if c.kubeconfigDir == "" {
		return nil, errRemoteClusterDisabled
	}
	path := filepath.Join(c.kubeconfigDir, cl.KubeconfigRef)
	cfg, err := clientcmd.BuildConfigFromFlags("", path)
	if err != nil {
		return nil, err
	}
	return kubernetes.NewForConfig(cfg)
}

// errRemoteClusterDisabled reports that remote-cluster collection is not
// configured.
var errRemoteClusterDisabled = errors.New("remote cluster collection disabled: controller.cluster.kubeconfigDir is empty")

// listRegisteredClusters reads the active clusters from the clusters
// table.
func (c *ClusterCollector) listRegisteredClusters(ctx context.Context) ([]registeredCluster, error) {
	var rows []registeredCluster
	err := c.db.WithContext(ctx).Table("clusters").
		Select("id, kubeconfig_ref, state").
		Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	return rows, nil
}

// nodeStatus derives a node's health status from its conditions. The
// values match the console's state enum (ready / not_ready / unknown).
func nodeStatus(n corev1.Node) string {
	for _, cond := range n.Status.Conditions {
		if cond.Type == corev1.NodeReady {
			if cond.Status == corev1.ConditionTrue {
				return "ready"
			}
			return "not_ready"
		}
	}
	return "unknown"
}