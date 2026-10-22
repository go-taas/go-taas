package vgpu

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/go-taas/go-taas/pkg/mq"

	apierrors "github.com/go-taas/go-taas/pkg/errors"
)

func TestMembershipConsumerHandle(t *testing.T) {
	proj := NewMembershipProjection()
	consumer := NewMembershipConsumer(mq.NewFake(), proj)

	// A valid snapshot applies to the projection.
	snap := inventorySnapshot{
		Nodes: []acceleratorNode{
			{
				NodeID: "n1", Name: "node-a",
				Labels: map[string]string{"go-taas.com/vgpu-pool": "pool-a"},
				GPUs: []acceleratorGPU{{
					Index: 0, Model: "A800",
					Slices: []sliceAllocation{{TemplateName: "small", ServiceID: "s1", MemoryGiB: 8, ComputePct: 25}},
				}},
			},
		},
	}
	body, err := json.Marshal(snap)
	require.NoError(t, err)
	require.NoError(t, consumer.handle(context.Background(), mq.Message{Subject: "accelerator.inventory", Body: body}))

	assert.Equal(t, int64(1), proj.NodeCount("pool-a", "go-taas.com/vgpu-pool", "pool-a"))

	// A malformed snapshot is skipped without error.
	require.NoError(t, consumer.handle(context.Background(), mq.Message{Subject: "accelerator.inventory", Body: []byte("{bad")}))
}

func TestMembershipUtilization(t *testing.T) {
	proj := NewMembershipProjection()
	proj.Replace(map[string]*acceleratorNode{
		"n1": {
			NodeID: "n1", Name: "node-a",
			Labels: map[string]string{"go-taas.com/vgpu-pool": "pool-a"},
			GPUs: []acceleratorGPU{{
				Index: 0, Model: "A800",
				Slices: []sliceAllocation{{TemplateName: "small", ServiceID: "s1", MemoryGiB: 8, ComputePct: 25}},
			}},
		},
	})
	// No total memory is tracked in the projection, so utilization is 0.
	assert.Equal(t, int64(0), proj.Utilization("pool-a", "go-taas.com/vgpu-pool", "pool-a"))
}

func TestMembershipConsumerRun(_ *testing.T) {
	client := mq.NewFake()
	proj := NewMembershipProjection()
	consumer := NewMembershipConsumer(client, proj)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- consumer.Run(ctx)
	}()
	cancel()
	<-done
}

func TestRepositoryCreateUniqueViolation(t *testing.T) {
	db := newTestDB(t)
	repo := NewRepository(db)
	ctx := context.Background()

	pool := &VGPUPool{ID: "p1", Name: "pool-a", LabelKey: "k", LabelValue: "v", PlacementPolicy: PolicyBinpack}
	require.NoError(t, repo.CreateVGPUPool(ctx, pool))
	// Duplicate name maps to CodeVGPUPoolInvalid.
	err := repo.CreateVGPUPool(ctx, &VGPUPool{ID: "p2", Name: "pool-a", LabelKey: "k", LabelValue: "v"})
	assert.EqualValues(t, apierrors.CodeVGPUPoolInvalid, apierrors.CodeOf(err))
}
