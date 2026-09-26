// Package accelerator implements the read-only accelerator fleet
// inventory (feature #18). It maintains an in-memory projection cache of
// Kubernetes node + extended-resource state, refreshed by the
// Controller's periodic full-snapshot publish, and serves three admin
// RPCs over the gateway.
package accelerator

import (
	"sort"
	"strings"
	"sync"
	"time"
)

// Vendor set (closed, AD3). A node with no accelerator label is
// "unspecified".
const (
	VendorNvidia      = "nvidia"
	VendorIluvatar    = "iluvatar"
	VendorMetax       = "metax"
	VendorUnspecified = "unspecified"
)

// Health values (composite, AD2).
const (
	HealthHealthy  = "healthy"
	HealthDegraded = "degraded"
	HealthUnknown  = "unknown"
)

// Device-plugin states.
const (
	DevicePluginHealthy  = "healthy"
	DevicePluginDegraded = "degraded"
	DevicePluginUnknown  = "unknown"
)

// Readiness values.
const (
	ReadinessReady    = "ready"
	ReadinessNotReady = "not_ready"
	ReadinessUnknown  = "unknown"
)

// AcceleratorNode is one node's projection in the cache.
type AcceleratorNode struct { //nolint:revive // accelerator.AcceleratorNode is the documented domain name
	NodeID            string
	Name              string
	Vendor            string // nvidia | iluvatar | metax | unspecified
	CardTypes         []string
	GPUsAllocated     int64
	GPUsFree          int64
	DriverVersion     string
	DevicePluginState string // healthy | degraded | unknown
	Readiness         string // ready | not_ready | unknown
	Health            string // healthy | degraded | unknown
	GPUs              []AcceleratorGPU
	Labels            map[string]string // accelerator-relevant labels
	Taints            []string
	Resources         []AcceleratorResource // per card type
	LastUpdatedAt     time.Time
}

// AcceleratorGPU is one GPU on a node.
type AcceleratorGPU struct { //nolint:revive // accelerator.AcceleratorGPU is the documented domain name
	Index     int64
	Model     string
	Allocated bool
	Free      bool
	Note      string
}

// AcceleratorResource is one card type's allocatable vs allocated.
type AcceleratorResource struct { //nolint:revive // accelerator.AcceleratorResource is the documented domain name
	CardType    string
	Allocatable int64
	Allocated   int64
}

// CardTypeSummary is one (vendor, card_type) capacity row.
type CardTypeSummary struct {
	Vendor    string
	CardType  string
	Total     int64
	Allocated int64
	Free      int64
	NodeCount int64
}

// ProjectionCache is the in-memory accelerator inventory (AD1, AD11). It
// is a sync.RWMutex-guarded map keyed by node id, replaced wholesale on
// each snapshot.
type ProjectionCache struct {
	mu    sync.RWMutex
	nodes map[string]*AcceleratorNode
}

// NewProjectionCache constructs an empty cache.
func NewProjectionCache() *ProjectionCache {
	return &ProjectionCache{nodes: make(map[string]*AcceleratorNode)}
}

// Replace swaps the whole cache under a write lock (AD1).
func (c *ProjectionCache) Replace(nodes map[string]*AcceleratorNode) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.nodes = nodes
}

// Get returns the node with the given id under a read lock.
func (c *ProjectionCache) Get(nodeID string) (*AcceleratorNode, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	n, ok := c.nodes[nodeID]
	return n, ok
}

// List returns the nodes matching the vendor/health filters and the
// node-name search, paginated by offset/limit. It returns the filtered
// slice and the total count before pagination. Empty filter values match
// everything. Invalid filter values are treated as empty (AD6 validation
// matrix).
func (c *ProjectionCache) List(vendor, health, search string, offset, limit int) ([]*AcceleratorNode, int64, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	vendor = normalizeFilter(vendor)
	health = normalizeFilter(health)
	search = strings.TrimSpace(search)

	var matched []*AcceleratorNode
	for _, n := range c.nodes {
		if vendor != "" && n.Vendor != vendor {
			continue
		}
		if health != "" && n.Health != health {
			continue
		}
		if search != "" && !strings.Contains(strings.ToLower(n.Name), strings.ToLower(search)) {
			continue
		}
		matched = append(matched, n)
	}

	// Stable ordering by node name for deterministic pagination.
	sort.Slice(matched, func(i, j int) bool { return matched[i].Name < matched[j].Name })

	total := int64(len(matched))
	if offset < 0 {
		offset = 0
	}
	if limit <= 0 {
		limit = 20
	}
	if offset >= len(matched) {
		return []*AcceleratorNode{}, total, nil
	}
	end := offset + limit
	if end > len(matched) {
		end = len(matched)
	}
	return matched[offset:end], total, nil
}

// CardTypeSummary derives the (vendor, card_type) capacity summary from
// the cache at request time (AD8). It groups all nodes by
// (vendor, card_type), sums total/allocated/free, counts distinct nodes,
// omits card types with zero total, and sorts by vendor then free
// ascending (most constrained first).
func (c *ProjectionCache) CardTypeSummary() []CardTypeSummary {
	c.mu.RLock()
	defer c.mu.RUnlock()

	type key struct {
		vendor, cardType string
	}
	agg := make(map[key]*CardTypeSummary)
	for _, n := range c.nodes {
		for _, r := range n.Resources {
			if r.Allocatable <= 0 {
				continue
			}
			k := key{n.Vendor, r.CardType}
			row, ok := agg[k]
			if !ok {
				row = &CardTypeSummary{Vendor: n.Vendor, CardType: r.CardType}
				agg[k] = row
			}
			row.Total += r.Allocatable
			row.Allocated += r.Allocated
			row.Free += r.Allocatable - r.Allocated
			row.NodeCount++
		}
	}

	out := make([]CardTypeSummary, 0, len(agg))
	for _, row := range agg {
		if row.Total <= 0 {
			continue
		}
		out = append(out, *row)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Vendor != out[j].Vendor {
			return out[i].Vendor < out[j].Vendor
		}
		if out[i].Free != out[j].Free {
			return out[i].Free < out[j].Free
		}
		return out[i].CardType < out[j].CardType
	})
	return out
}

// normalizeFilter returns the filter value if it is a known enum value,
// otherwise "" (invalid values are treated as empty).
func normalizeFilter(v string) string {
	switch v {
	case VendorNvidia, VendorIluvatar, VendorMetax, VendorUnspecified,
		HealthHealthy, HealthDegraded, HealthUnknown:
		return v
	default:
		return ""
	}
}
