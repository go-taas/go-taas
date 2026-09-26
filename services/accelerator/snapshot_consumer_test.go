package accelerator

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/go-taas/go-taas/pkg/mq"
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
