package accelerator

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/go-taas/go-taas/pkg/mq"
	acceleratorv1 "github.com/go-taas/go-taas/proto/taas/accelerator/v1"
)

func TestSnapshotConsumerAppliesSnapshot(t *testing.T) {
	cache := NewProjectionCache()
	consumer := NewSnapshotConsumer(mq.NewFake(), cache, 1)

	snap := inventorySnapshot{
		ReportedAt: time.Now().UTC(),
		Nodes: []snapshotNode{
			{
				NodeID:            "n1",
				Name:              "node-a",
				Vendor:            VendorNvidia,
				CardTypes:         []string{"gpu"},
				GPUsAllocated:     2,
				GPUsFree:          6,
				DriverVersion:     "535",
				DevicePluginState: DevicePluginHealthy,
				Readiness:         ReadinessReady,
				Resources: []snapshotResource{
					{CardType: "A800", Allocatable: 8, Allocated: 2},
				},
			},
			{
				NodeID:            "n2",
				Name:              "node-b",
				Vendor:            VendorNvidia,
				CardTypes:         []string{"gpu"},
				DriverVersion:     "535",
				DevicePluginState: DevicePluginDegraded,
				Readiness:         ReadinessReady,
			},
		},
	}
	body, err := json.Marshal(snap)
	require.NoError(t, err)

	err = consumer.handle(context.Background(), mq.Message{Subject: "accelerator.inventory", Body: body})
	require.NoError(t, err)

	n1, ok := cache.Get("n1")
	require.True(t, ok)
	assert.Equal(t, HealthHealthy, n1.Health)
	assert.EqualValues(t, 2, n1.GPUsAllocated)
	require.Len(t, n1.Resources, 1)

	n2, ok := cache.Get("n2")
	require.True(t, ok)
	assert.Equal(t, HealthDegraded, n2.Health)

	// A second snapshot replaces the cache wholesale.
	snap2 := inventorySnapshot{
		ReportedAt: time.Now().UTC(),
		Nodes: []snapshotNode{
			{NodeID: "n3", Name: "node-c", Vendor: VendorIluvatar, Readiness: ReadinessReady},
		},
	}
	body2, err := json.Marshal(snap2)
	require.NoError(t, err)
	require.NoError(t, consumer.handle(context.Background(), mq.Message{Body: body2}))

	_, ok = cache.Get("n1")
	assert.False(t, ok, "old node must be removed on full snapshot replace")
	_, ok = cache.Get("n3")
	assert.True(t, ok)
}

func TestSnapshotConsumerSkipsMalformed(t *testing.T) {
	cache := NewProjectionCache()
	consumer := NewSnapshotConsumer(mq.NewFake(), cache, 1)

	// A malformed snapshot is skipped, never retried, and does not
	// disturb the existing cache.
	err := consumer.handle(context.Background(), mq.Message{Body: []byte("{not json")})
	assert.NoError(t, err)
	assert.Empty(t, cache.nodes)
}

func TestSnapshotConsumerSkipsEmptyNodeID(t *testing.T) {
	cache := NewProjectionCache()
	consumer := NewSnapshotConsumer(mq.NewFake(), cache, 1)

	snap := inventorySnapshot{
		ReportedAt: time.Now().UTC(),
		Nodes: []snapshotNode{
			{NodeID: "", Name: "no-id", Vendor: VendorNvidia, Readiness: ReadinessReady},
		},
	}
	body, err := json.Marshal(snap)
	require.NoError(t, err)
	require.NoError(t, consumer.handle(context.Background(), mq.Message{Body: body}))
	assert.Empty(t, cache.nodes)
}

// TestSnapshotConsumerSharesCacheWithService guards BUG-ACCEL-001: the
// snapshot consumer and the accelerator service must share the SAME
// ProjectionCache instance, so the consumer's Replace() populates the
// cache the RPCs read. If they were separate caches, the service would
// keep serving an empty inventory after a valid snapshot.
func TestSnapshotConsumerSharesCacheWithService(t *testing.T) {
	// One cache, wired into both the service and the consumer — exactly
	// how apps/taas-server/main.go constructs them.
	cache := NewProjectionCache()
	svc := NewWithCache(cache)
	consumer := NewSnapshotConsumer(mq.NewFake(), cache, 1)

	snap := inventorySnapshot{
		ReportedAt: time.Now().UTC(),
		Nodes: []snapshotNode{
			{
				NodeID:            "node-nvidia-a800-01",
				Name:              "node-nvidia-a800-01",
				Vendor:            VendorNvidia,
				CardTypes:         []string{"gpu"},
				GPUsAllocated:     2,
				GPUsFree:          6,
				DriverVersion:     "535",
				DevicePluginState: DevicePluginHealthy,
				Readiness:         ReadinessReady,
				Resources: []snapshotResource{
					{CardType: "A800", Allocatable: 8, Allocated: 2},
				},
			},
		},
	}
	body, err := json.Marshal(snap)
	require.NoError(t, err)

	// The consumer applies the snapshot to the shared cache.
	require.NoError(t, consumer.handle(context.Background(), mq.Message{Subject: "accelerator.inventory", Body: body}))

	// The service reads the SAME cache, so the fleet list and the node
	// detail must reflect the ingested snapshot.
	listResp, err := svc.ListAcceleratorNodes(context.Background(), &acceleratorv1.ListAcceleratorNodesRequest{})
	require.NoError(t, err)
	require.Len(t, listResp.Nodes, 1)
	assert.Equal(t, "node-nvidia-a800-01", listResp.Nodes[0].Name)
	assert.EqualValues(t, 1, listResp.PageMeta.Total)

	getResp, err := svc.GetAcceleratorNode(context.Background(), &acceleratorv1.GetAcceleratorNodeRequest{NodeId: "node-nvidia-a800-01"})
	require.NoError(t, err)
	require.NotNil(t, getResp.Node)
	assert.Equal(t, "node-nvidia-a800-01", getResp.Node.Summary.Name)

	cardResp, err := svc.ListCardTypeSummary(context.Background(), &acceleratorv1.ListCardTypeSummaryRequest{})
	require.NoError(t, err)
	require.Len(t, cardResp.CardTypes, 1)
	assert.Equal(t, "A800", cardResp.CardTypes[0].CardType)
}
