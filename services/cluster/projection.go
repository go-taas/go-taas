package cluster

import (
	"sync"
)

// ClusterHealth is one cluster's health projection (AD4).
type ClusterHealth struct { //nolint:revive // cluster.ClusterHealth is the documented domain name
	ClusterID     string
	Health        string
	NodeCount     int64
	LastCheckedAt int64
	Nodes         []ClusterNodeHealth
}

// ClusterNodeHealth is one node's health in a cluster.
type ClusterNodeHealth struct { //nolint:revive // cluster.ClusterNodeHealth is the documented domain name
	NodeID string
	Name   string
	Status string
}

// ProjectionCache is the in-memory cluster-health projection cache
// (AD4). It is replaced wholesale on each snapshot, following the
// accelerator-inventory AD1/AD11 pattern.
type ProjectionCache struct {
	mu     sync.RWMutex
	byID   map[string]*ClusterHealth
}

// NewProjectionCache constructs an empty ProjectionCache.
func NewProjectionCache() *ProjectionCache {
	return &ProjectionCache{byID: make(map[string]*ClusterHealth)}
}

// Replace replaces the entire cache with the given snapshot.
func (c *ProjectionCache) Replace(snapshot map[string]*ClusterHealth) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.byID = snapshot
}

// Get returns one cluster's health, or nil when absent.
func (c *ProjectionCache) Get(clusterID string) *ClusterHealth {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.byID[clusterID]
}

// All returns a copy of the whole cache.
func (c *ProjectionCache) All() map[string]*ClusterHealth {
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := make(map[string]*ClusterHealth, len(c.byID))
	for k, v := range c.byID {
		out[k] = v
	}
	return out
}