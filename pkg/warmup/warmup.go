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
