package accelerator

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/go-taas/go-taas/pkg/errors"
	acceleratorv1 "github.com/go-taas/go-taas/proto/taas/accelerator/v1"
	commonv1 "github.com/go-taas/go-taas/proto/taas/common/v1"
	"github.com/go-taas/go-taas/services/image"
)

// fakeWarmupProvider returns a fixed set of warmup tasks.
type fakeWarmupProvider struct {
	tasks []*image.WarmupTask
}

func (f *fakeWarmupProvider) ListWarmupTasksForNode(_ context.Context, _ string, _ int) ([]*image.WarmupTask, error) {
	return f.tasks, nil
}

func TestServiceListAcceleratorNodes(t *testing.T) {
	cache := NewProjectionCache()
	cache.Replace(map[string]*AcceleratorNode{
		"n1": testNode("n1", "node-a", VendorNvidia, ReadinessReady, DevicePluginHealthy, "535", nil),
		"n2": testNode("n2", "node-b", VendorNvidia, ReadinessReady, DevicePluginDegraded, "535", nil),
	})
	svc := NewWithCache(cache)

	resp, err := svc.ListAcceleratorNodes(context.Background(), &acceleratorv1.ListAcceleratorNodesRequest{})
	require.NoError(t, err)
	require.NotNil(t, resp.Response)
	assert.EqualValues(t, 0, resp.Response.Code)
	require.Len(t, resp.Nodes, 2)
	assert.Equal(t, "node-a", resp.Nodes[0].Name)
	assert.Equal(t, VendorNvidia, resp.Nodes[0].Vendor)
	assert.Equal(t, HealthHealthy, resp.Nodes[0].Health)
	assert.EqualValues(t, 2, resp.PageMeta.Total)

	// Vendor filter.
	resp, err = svc.ListAcceleratorNodes(context.Background(), &acceleratorv1.ListAcceleratorNodesRequest{
		Vendor: VendorIluvatar,
	})
	require.NoError(t, err)
	assert.Len(t, resp.Nodes, 0)
	assert.EqualValues(t, 0, resp.PageMeta.Total)

	// Pagination defaults.
	resp, err = svc.ListAcceleratorNodes(context.Background(), &acceleratorv1.ListAcceleratorNodesRequest{
		Page: &commonv1.PageRequest{Offset: 0, Limit: 1},
	})
	require.NoError(t, err)
	assert.Len(t, resp.Nodes, 1)
	assert.EqualValues(t, 1, resp.PageMeta.Limit)
	assert.EqualValues(t, 2, resp.PageMeta.Total)
}

func TestServiceGetAcceleratorNode(t *testing.T) {
	cache := NewProjectionCache()
	cache.Replace(map[string]*AcceleratorNode{
		"n1": {
			NodeID:            "n1",
			Name:              "node-a",
			Vendor:            VendorNvidia,
			CardTypes:         []string{"gpu"},
			GPUsAllocated:     2,
			GPUsFree:          6,
			DriverVersion:     "535",
			DevicePluginState: DevicePluginHealthy,
			Readiness:         ReadinessReady,
			Health:            HealthHealthy,
			GPUs: []AcceleratorGPU{
				{Index: 0, Model: "A800", Allocated: true, Free: false, Note: ""},
				{Index: 1, Model: "A800", Allocated: false, Free: true, Note: ""},
			},
			Labels: map[string]string{"go-taas.io/accelerator": "nvidia"},
			Taints: []string{"dedicated=gpu:NoSchedule"},
			Resources: []AcceleratorResource{
				{CardType: "A800", Allocatable: 8, Allocated: 2},
			},
			LastUpdatedAt: time.Now().UTC(),
		},
	})
	svc := NewWithCache(cache)
	svc.SetWarmupProvider(&fakeWarmupProvider{tasks: []*image.WarmupTask{
		{ID: "task-1", State: "succeeded", NodeResults: []byte(`[{"node":"node-a","state":"succeeded"}]`)},
	}})

	resp, err := svc.GetAcceleratorNode(context.Background(), &acceleratorv1.GetAcceleratorNodeRequest{NodeId: "n1"})
	require.NoError(t, err)
	require.NotNil(t, resp.Node)
	assert.Equal(t, "node-a", resp.Node.Summary.Name)
	require.Len(t, resp.Node.Gpus, 2)
	assert.EqualValues(t, 0, resp.Node.Gpus[0].Index)
	assert.True(t, resp.Node.Gpus[0].Allocated)
	assert.Equal(t, "nvidia", resp.Node.Labels["go-taas.io/accelerator"])
	require.Len(t, resp.Node.Taints, 1)
	require.Len(t, resp.Node.Resources, 1)
	assert.EqualValues(t, 8, resp.Node.Resources[0].Allocatable)
	// Warmup context.
	require.Len(t, resp.Node.WarmupTasks, 1)
	assert.Equal(t, "task-1", resp.Node.WarmupTasks[0].TaskId)
	assert.Equal(t, "succeeded", resp.Node.WarmupTasks[0].NodeOutcome)

	// Missing node -> 10208.
	_, err = svc.GetAcceleratorNode(context.Background(), &acceleratorv1.GetAcceleratorNodeRequest{NodeId: "missing"})
	require.Error(t, err)
	assert.Equal(t, errors.CodeAcceleratorNodeNotFound, errors.CodeOf(err))
}

func TestServiceListCardTypeSummary(t *testing.T) {
	cache := NewProjectionCache()
	cache.Replace(map[string]*AcceleratorNode{
		"n1": testNode("n1", "node-a", VendorNvidia, ReadinessReady, DevicePluginHealthy, "535", []AcceleratorResource{
			{CardType: "A800", Allocatable: 8, Allocated: 2},
		}),
		"n2": testNode("n2", "node-b", VendorNvidia, ReadinessReady, DevicePluginHealthy, "535", []AcceleratorResource{
			{CardType: "A800", Allocatable: 8, Allocated: 8},
		}),
	})
	svc := NewWithCache(cache)

	resp, err := svc.ListCardTypeSummary(context.Background(), &acceleratorv1.ListCardTypeSummaryRequest{})
	require.NoError(t, err)
	require.Len(t, resp.CardTypes, 1)
	assert.Equal(t, VendorNvidia, resp.CardTypes[0].Vendor)
	assert.Equal(t, "A800", resp.CardTypes[0].CardType)
	assert.EqualValues(t, 16, resp.CardTypes[0].Total)
	assert.EqualValues(t, 10, resp.CardTypes[0].Allocated)
	assert.EqualValues(t, 6, resp.CardTypes[0].Free)
	assert.EqualValues(t, 2, resp.CardTypes[0].NodeCount)
}
