package runtime

import (
	"sort"
	"strings"
	"sync"
	"time"
)

// Availability values (AD4).
const (
	AvailabilityAvailable   = "available"
	AvailabilityDegraded    = "degraded"
	AvailabilityUnavailable = "unavailable"
	AvailabilityUnknown     = "unknown"
)

// Component kinds (AD4).
const (
	KindControlPlane = "control_plane"
	KindGateway      = "gateway"
	KindFallback     = "fallback"
)

// Component is one runtime component's projection (AD4).
type Component struct {
	ComponentID     string    `json:"component_id"`
	Name            string    `json:"name"`
	Kind            string    `json:"kind"` // control_plane | gateway | fallback
	Namespace       string    `json:"namespace"`
	Status          string    `json:"status"` // ready | progressing | degraded | unavailable | unknown
	ReadyReplicas   int64     `json:"ready_replicas"`
	DesiredReplicas int64     `json:"desired_replicas"`
	LastCheckedAt   time.Time `json:"last_checked_at"`
}

// ModelCondition mirrors an AIBrix ModelClaim status condition.
type ModelCondition struct {
	Type               string    `json:"type"`
	Status             string    `json:"status"`
	Reason             string    `json:"reason"`
	Message            string    `json:"message"`
	LastTransitionTime time.Time `json:"last_transition_time"`
}

// RegisteredModel is one ModelClaim's projection (AD5).
type RegisteredModel struct {
	ModelName       string           `json:"model_name"`
	Engine          string           `json:"engine"` // vllm | sglang
	Replicas        int64            `json:"replicas"`
	ReadyReplicas   int64            `json:"ready_replicas"`
	ReadyCandidates int64            `json:"ready_candidates"`
	Conditions      []ModelCondition `json:"conditions"`
	Status          string           `json:"status"` // ready | progressing | degraded | failed | unknown
	CreatedAt       time.Time        `json:"created_at"`
	Origin          string           `json:"origin"` // service | registration
}

// HealthSnapshot is the controller's full runtime snapshot (AD4). It is
// the wire message published on runtime.health and the value held by the
// projection cache.
type HealthSnapshot struct {
	Scheme         string            `json:"scheme"`
	Availability   string            `json:"availability"`
	RuntimeName    string            `json:"runtime_name"`
	RuntimeVersion string            `json:"runtime_version"`
	ClusterName    string            `json:"cluster_name"`
	Reason         string            `json:"reason"`
	Components     []Component       `json:"components"`
	Models         []RegisteredModel `json:"models"`
	ReportedAt     time.Time         `json:"reported_at"`
}

// ProjectionCache is the in-memory runtime projection (AD11). It holds
// the latest controller snapshot and is replaced wholesale on each
// publish.
type ProjectionCache struct {
	mu       sync.RWMutex
	snapshot *HealthSnapshot
}

// NewProjectionCache constructs an empty ProjectionCache.
func NewProjectionCache() *ProjectionCache { return &ProjectionCache{} }

// Replace swaps the whole snapshot under a write lock (AD11).
func (c *ProjectionCache) Replace(snapshot *HealthSnapshot) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.snapshot = snapshot
}

// Snapshot returns a shallow copy of the current snapshot, or nil when
// none has arrived yet.
func (c *ProjectionCache) Snapshot() *HealthSnapshot {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.snapshot == nil {
		return nil
	}
	out := *c.snapshot
	return &out
}

// Availability returns the current availability, or unknown when no
// snapshot has arrived.
func (c *ProjectionCache) Availability() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.snapshot == nil || c.snapshot.Availability == "" {
		return AvailabilityUnknown
	}
	return c.snapshot.Availability
}

// Components returns the components matching the kind/status filters in
// stable name order. Empty filter values match everything.
func (c *ProjectionCache) Components(kind, status string) []Component {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.snapshot == nil {
		return nil
	}
	out := make([]Component, 0, len(c.snapshot.Components))
	for _, comp := range c.snapshot.Components {
		if kind != "" && comp.Kind != kind {
			continue
		}
		if status != "" && comp.Status != status {
			continue
		}
		out = append(out, comp)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Models returns the registered models matching the name/engine/status
// filters, paginated by offset/limit, plus the total count before
// pagination. Empty filter values match everything.
func (c *ProjectionCache) Models(name, engine, status string, offset, limit int) ([]RegisteredModel, int64) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.snapshot == nil {
		return []RegisteredModel{}, 0
	}
	name = strings.ToLower(strings.TrimSpace(name))
	engine = strings.TrimSpace(engine)
	status = strings.TrimSpace(status)

	matched := make([]RegisteredModel, 0, len(c.snapshot.Models))
	for _, m := range c.snapshot.Models {
		if name != "" && !strings.Contains(strings.ToLower(m.ModelName), name) {
			continue
		}
		if engine != "" && m.Engine != engine {
			continue
		}
		if status != "" && m.Status != status {
			continue
		}
		matched = append(matched, m)
	}
	sort.Slice(matched, func(i, j int) bool { return matched[i].ModelName < matched[j].ModelName })

	total := int64(len(matched))
	if offset < 0 {
		offset = 0
	}
	if limit <= 0 {
		limit = 20
	}
	if offset >= len(matched) {
		return []RegisteredModel{}, total
	}
	end := offset + limit
	if end > len(matched) {
		end = len(matched)
	}
	return matched[offset:end], total
}

// FindModel returns the registered model with the given name, or false
// when absent.
func (c *ProjectionCache) FindModel(name string) (*RegisteredModel, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.snapshot == nil {
		return nil, false
	}
	for i := range c.snapshot.Models {
		if c.snapshot.Models[i].ModelName == name {
			m := c.snapshot.Models[i]
			return &m, true
		}
	}
	return nil, false
}
