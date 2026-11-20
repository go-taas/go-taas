// Package warmup defines the payloads exchanged between the model service,
// the controller and the KServe LocalModel resources.
package warmup

import "time"

// Cache states.
const (
	StateQueued    = "queued"
	StateWarming   = "warming"
	StateReady     = "ready"
	StateFailed    = "failed"
	StateCancelled = "cancelled"
)

// Terminal reports whether a state can no longer change.
func Terminal(state string) bool {
	switch state {
	case StateReady, StateFailed, StateCancelled:
		return true
	}
	return false
}

// NodeGroup is one node pool that caches weights.
type NodeGroup struct {
	Name         string            `json:"name"`
	NodeSelector map[string]string `json:"nodeSelector,omitempty"`
	StorageLimit string            `json:"storageLimit,omitempty"`
	HostPath     string            `json:"hostPath,omitempty"`
	StorageClass string            `json:"storageClass,omitempty"`
}

// Request asks the controller to populate the node-local cache of a model.
type Request struct {
	ID         string      `json:"id"`
	ModelID    string      `json:"modelId"`
	ModelName  string      `json:"modelName,omitempty"`
	Version    string      `json:"version"`
	Backend    string      `json:"backend,omitempty"`
	WeightPath string      `json:"weightPath"`
	SourceURI  string      `json:"sourceUri"`
	NodeGroups []NodeGroup `json:"nodeGroups,omitempty"`
	SizeBytes  int64       `json:"sizeBytes,omitempty"`
}

// CacheName returns the deterministic LocalModelCache resource name for a
// request. Using the row id keeps a retry idempotent even when two catalog
// entries share a name.
func CacheName(id string) string {
	return "taas-model-" + id
}

// Status is the controller's observation of one cache.
type Status struct {
	ID          string    `json:"id"`
	CacheName   string    `json:"cacheName,omitempty"`
	State       string    `json:"state"`
	Message     string    `json:"message,omitempty"`
	NodeGroups  []string  `json:"nodeGroups,omitempty"`
	WarmedNodes []string  `json:"warmedNodes,omitempty"`
	TotalNodes  int       `json:"totalNodes,omitempty"`
	SizeBytes   int64     `json:"sizeBytes,omitempty"`
	UpdatedAt   time.Time `json:"updatedAt"`
}

// NodeGroupEvent is a platform-managed node-group create/update/delete. The
// controller reconciles the matching KServe LocalModelNodeGroup; the model
// service publishes it whenever an administrator edits the group.
type NodeGroupEvent struct {
	NodeGroup NodeGroup `json:"nodeGroup"`
	Deleted   bool      `json:"deleted,omitempty"`
	// Snapshot carries the complete desired set. The model service publishes
	// it periodically so a controller that restarted converges, including
	// node groups that were deleted while it was down.
	Snapshot   bool        `json:"snapshot,omitempty"`
	NodeGroups []NodeGroup `json:"nodeGroups,omitempty"`
}

// NodeModel is one model copy recorded on a LocalModelNode.
type NodeModel struct {
	CacheName string `json:"cacheName"`
	ModelName string `json:"modelName,omitempty"`
	NodeGroup string `json:"nodeGroup,omitempty"`
	State     string `json:"state,omitempty"`
}

// Node is the controller's observation of one LocalModelNode.
type Node struct {
	Name            string      `json:"name"`
	NodeGroups      []string    `json:"nodeGroups,omitempty"`
	Models          []NodeModel `json:"models,omitempty"`
	ModelCount      int         `json:"modelCount"`
	DownloadedCount int         `json:"downloadedCount"`
	ObservedAt      time.Time   `json:"observedAt"`
}

// NodeSnapshot is the full set of LocalModelNodes the controller owns. It is
// published as a complete snapshot so the server can drop nodes that no
// longer exist.
type NodeSnapshot struct {
	Nodes []Node `json:"nodes"`
}
