package accelerator

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// testNode builds a cache node with the given signals and its composite
// health derived from them (AD2).
func testNode(id, name, vendor, readiness, devicePlugin, driver string, resources []AcceleratorResource) *AcceleratorNode {
	n := &AcceleratorNode{
		NodeID:            id,
		Name:              name,
		Vendor:            vendor,
		CardTypes:         []string{"gpu"},
		DriverVersion:     driver,
		DevicePluginState: devicePlugin,
		Readiness:         readiness,
		Resources:         resources,
		LastUpdatedAt:     time.Now().UTC(),
	}
	n.Health = computeHealth(n)
	return n
}

func TestComputeHealth(t *testing.T) {
	cases := []struct {
		name     string
		node     *AcceleratorNode
		expected string
	}{
		{
			name:     "healthy when every signal holds",
			node:     testNode("n1", "node-1", VendorNvidia, ReadinessReady, DevicePluginHealthy, "535.104.05", nil),
			expected: HealthHealthy,
		},
		{
			name:     "degraded when device plugin degraded",
			node:     testNode("n1", "node-1", VendorNvidia, ReadinessReady, DevicePluginDegraded, "535.104.05", nil),
			expected: HealthDegraded,
		},
		{
			// The driver version is informational. A cluster whose nodes do
			// not publish it is not unhealthy, and reporting "degraded" on a
			// ready node with a healthy device plugin trains operators to
			// ignore the column.
			name:     "healthy when only the driver version is unknown",
			node:     testNode("n1", "node-1", VendorNvidia, ReadinessReady, DevicePluginHealthy, "", nil),
			expected: HealthHealthy,
		},
		{
			name:     "degraded when node not ready",
			node:     testNode("n1", "node-1", VendorNvidia, ReadinessNotReady, DevicePluginHealthy, "535.104.05", nil),
			expected: HealthDegraded,
		},
		{
			name:     "unknown when readiness unknown",
			node:     testNode("n1", "node-1", VendorNvidia, ReadinessUnknown, DevicePluginHealthy, "535.104.05", nil),
			expected: HealthUnknown,
		},
		{
			name:     "unknown when device plugin unknown",
			node:     testNode("n1", "node-1", VendorNvidia, ReadinessReady, DevicePluginUnknown, "535.104.05", nil),
			expected: HealthUnknown,
		},
		{
			name:     "unspecified vendor healthy on ready",
			node:     testNode("n1", "node-1", VendorUnspecified, ReadinessReady, DevicePluginUnknown, "", nil),
			expected: HealthHealthy,
		},
		{
			name:     "unspecified vendor degraded on not ready",
			node:     testNode("n1", "node-1", VendorUnspecified, ReadinessNotReady, DevicePluginUnknown, "", nil),
			expected: HealthDegraded,
		},
		{
			name:     "unspecified vendor unknown on unknown readiness",
			node:     testNode("n1", "node-1", VendorUnspecified, ReadinessUnknown, DevicePluginUnknown, "", nil),
			expected: HealthUnknown,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.expected, computeHealth(tc.node))
		})
	}
}

func TestProjectionCacheReplaceAndGet(t *testing.T) {
	cache := NewProjectionCache()
	n := testNode("n1", "node-1", VendorNvidia, ReadinessReady, DevicePluginHealthy, "535", nil)
	cache.Replace(map[string]*AcceleratorNode{"n1": n})

	got, ok := cache.Get("n1")
	require.True(t, ok)
	assert.Equal(t, "node-1", got.Name)

	_, ok = cache.Get("missing")
	assert.False(t, ok)
}

// TestProjectionCacheListCardTypes covers the narrow card-type read the
// image module consumes for the compatibility matrix (feature #19,
// AD11): distinct (vendor, card_type) pairs, sorted, omitting zero-
// allocatable card types.
func TestProjectionCacheListCardTypes(t *testing.T) {
	cache := NewProjectionCache()
	cache.Replace(map[string]*AcceleratorNode{
		"n1": testNode("n1", "node-1", VendorNvidia, ReadinessReady, DevicePluginHealthy, "535",
			[]AcceleratorResource{{CardType: "A800", Allocatable: 8, Allocated: 2}}),
		"n2": testNode("n2", "node-2", VendorNvidia, ReadinessReady, DevicePluginHealthy, "535",
			[]AcceleratorResource{{CardType: "H800", Allocatable: 4, Allocated: 0}}),
		"n3": testNode("n3", "node-3", VendorMetax, ReadinessReady, DevicePluginHealthy, "1.0",
			[]AcceleratorResource{{CardType: "M100", Allocatable: 0, Allocated: 0}}),
	})

	cardTypes := cache.ListCardTypes()
	require.Len(t, cardTypes, 2)
	// Sorted by vendor then card type; M100 (zero allocatable) omitted.
	assert.Equal(t, "A800", cardTypes[0].CardType)
	assert.Equal(t, VendorNvidia, cardTypes[0].Vendor)
	assert.Equal(t, "H800", cardTypes[1].CardType)
	assert.Equal(t, VendorNvidia, cardTypes[1].Vendor)
}

func TestProjectionCacheListFiltersAndPagination(t *testing.T) {
	cache := NewProjectionCache()
	cards := func(total int64) []AcceleratorResource {
		return []AcceleratorResource{{CardType: "gpu", Allocatable: total}}
	}
	nodes := map[string]*AcceleratorNode{
		"n1": testNode("n1", "node-a", VendorNvidia, ReadinessReady, DevicePluginHealthy, "535", cards(2)),
		"n2": testNode("n2", "node-b", VendorNvidia, ReadinessReady, DevicePluginDegraded, "535", cards(1)),
		"n3": testNode("n3", "node-c", VendorIluvatar, ReadinessReady, DevicePluginHealthy, "1.0", cards(4)),
		"n4": testNode("n4", "node-d", VendorUnspecified, ReadinessReady, DevicePluginUnknown, "", cards(0)),
		// A node with no card at all: it must not become a table row that
		// reads "0 / 0", and it is counted separately instead.
		"n5": testNode("n5", "node-cpu", VendorUnspecified, ReadinessReady, DevicePluginUnknown, "", nil),
	}
	cache.Replace(nodes)

	// No filters returns every node with cards, sorted by name. n4 has the
	// resource declared with zero allocatable, n5 declares none.
	all, total, err := cache.List("", "", "", 0, 100)
	require.NoError(t, err)
	assert.EqualValues(t, 3, total)
	assert.Len(t, all, 3)
	assert.Equal(t, "node-a", all[0].Name)
	assert.EqualValues(t, 2, cache.CPUNodeCount(), "n4 and n5 carry no usable card")

	// Vendor filter.
	nvidia, total, err := cache.List(VendorNvidia, "", "", 0, 100)
	require.NoError(t, err)
	assert.EqualValues(t, 2, total)
	assert.Len(t, nvidia, 2)

	// Health filter: node-a and node-c are healthy; node-b is degraded.
	healthy, total, err := cache.List("", HealthHealthy, "", 0, 100)
	require.NoError(t, err)
	assert.EqualValues(t, 2, total)
	assert.Len(t, healthy, 2)

	// Search.
	searched, total, err := cache.List("", "", "node-b", 0, 100)
	require.NoError(t, err)
	assert.EqualValues(t, 1, total)
	assert.Equal(t, "node-b", searched[0].Name)

	// Pagination.
	page, total, err := cache.List("", "", "", 0, 2)
	require.NoError(t, err)
	assert.EqualValues(t, 3, total)
	assert.Len(t, page, 2)
	assert.Equal(t, "node-a", page[0].Name)
	assert.Equal(t, "node-b", page[1].Name)

	// Offset beyond the end returns empty.
	empty, total, err := cache.List("", "", "", 100, 20)
	require.NoError(t, err)
	assert.EqualValues(t, 3, total)
	assert.Len(t, empty, 0)

	// Invalid filter values are treated as empty.
	all2, total, err := cache.List("bogus", "bogus", "", 0, 100)
	require.NoError(t, err)
	assert.EqualValues(t, 3, total)
	assert.Len(t, all2, 3)
}

func TestProjectionCacheCardTypeSummary(t *testing.T) {
	cache := NewProjectionCache()
	nodes := map[string]*AcceleratorNode{
		"n1": testNode("n1", "node-a", VendorNvidia, ReadinessReady, DevicePluginHealthy, "535", []AcceleratorResource{
			{CardType: "A800", Allocatable: 8, Allocated: 2},
		}),
		"n2": testNode("n2", "node-b", VendorNvidia, ReadinessReady, DevicePluginHealthy, "535", []AcceleratorResource{
			{CardType: "A800", Allocatable: 8, Allocated: 8},
		}),
		"n3": testNode("n3", "node-c", VendorIluvatar, ReadinessReady, DevicePluginHealthy, "1.0", []AcceleratorResource{
			{CardType: "BI-V150", Allocatable: 4, Allocated: 0},
		}),
		"n4": testNode("n4", "node-d", VendorNvidia, ReadinessReady, DevicePluginHealthy, "535", []AcceleratorResource{
			{CardType: "H800", Allocatable: 0, Allocated: 0}, // zero total omitted
		}),
	}
	cache.Replace(nodes)

	rows := cache.CardTypeSummary()
	// The H800 card type has zero total and is omitted (FR1.2), so only
	// the iluvatar BI-V150 and nvidia A800 rows remain.
	require.Len(t, rows, 2)

	// Sorted by vendor then free ascending.
	assert.Equal(t, VendorIluvatar, rows[0].Vendor)
	assert.Equal(t, "BI-V150", rows[0].CardType)
	assert.EqualValues(t, 4, rows[0].Total)
	assert.EqualValues(t, 0, rows[0].Allocated)
	assert.EqualValues(t, 4, rows[0].Free)
	assert.EqualValues(t, 1, rows[0].NodeCount)

	// nvidia A800: total 16, allocated 10, free 6, 2 nodes.
	assert.Equal(t, VendorNvidia, rows[1].Vendor)
	assert.Equal(t, "A800", rows[1].CardType)
	assert.EqualValues(t, 16, rows[1].Total)
	assert.EqualValues(t, 10, rows[1].Allocated)
	assert.EqualValues(t, 6, rows[1].Free)
	assert.EqualValues(t, 2, rows[1].NodeCount)
}

// TestCPUNodeCountIgnoresCardedNodes pins the fleet summary the page shows
// next to the card table.
func TestCPUNodeCountIgnoresCardedNodes(t *testing.T) {
	cache := NewProjectionCache()
	cache.Replace(map[string]*AcceleratorNode{
		"n1": testNode("n1", "gpu-1", VendorNvidia, ReadinessReady, DevicePluginHealthy, "535",
			[]AcceleratorResource{{CardType: "A100", Allocatable: 8}}),
		"n2": testNode("n2", "gpu-2", VendorNvidia, ReadinessReady, DevicePluginHealthy, "535",
			[]AcceleratorResource{{CardType: "A100", Allocatable: 8}}),
		"c1": testNode("c1", "cpu-1", VendorUnspecified, ReadinessReady, DevicePluginUnknown, "", nil),
		"c2": testNode("c2", "cpu-2", VendorUnspecified, ReadinessReady, DevicePluginUnknown, "", nil),
		"c3": testNode("c3", "cpu-3", VendorUnspecified, ReadinessReady, DevicePluginUnknown, "", nil),
	})

	assert.EqualValues(t, 3, cache.CPUNodeCount())

	list, total, err := cache.List("", "", "", 0, 100)
	require.NoError(t, err)
	assert.EqualValues(t, 2, total, "only nodes with cards are listed")
	assert.Len(t, list, 2)
	for _, n := range list {
		assert.Equal(t, VendorNvidia, n.Vendor, "a CPU node must not appear as a row")
	}
}
