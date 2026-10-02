package cluster

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/go-taas/go-taas/pkg/mq"
)

// TestSnapshotConsumerApplies verifies the consumer applies a full
// snapshot to the cache.
func TestSnapshotConsumerApplies(t *testing.T) {
	cache := NewProjectionCache()
	client := mq.NewFake()
	consumer := NewSnapshotConsumer(client, cache)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- consumer.Run(ctx) }()

	// Publish a snapshot.
	body := []byte(`{"clusters":[{"cluster_id":"c1","health":"healthy","node_count":2,"last_checked_at":100,"nodes":[{"node_id":"n1","name":"node-1","status":"ready"}]}]}`)
	require.Eventually(t, func() bool {
		_ = client.Publish(ctx, mq.DefaultSubjects().ClusterHealth, body, nil)
		return cache.Get("c1") != nil
	}, 2*time.Second, 10*time.Millisecond)

	health := cache.Get("c1")
	require.NotNil(t, health)
	assert.Equal(t, "healthy", health.Health)
	require.Len(t, health.Nodes, 1)

	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return after cancel")
	}
}

// TestSnapshotConsumerMalformed verifies a malformed snapshot is skipped.
func TestSnapshotConsumerMalformed(t *testing.T) {
	cache := NewProjectionCache()
	client := mq.NewFake()
	consumer := NewSnapshotConsumer(client, cache)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- consumer.Run(ctx) }()

	require.Eventually(t, func() bool {
		_ = client.Publish(ctx, mq.DefaultSubjects().ClusterHealth, []byte(`not-json`), nil)
		return true
	}, 2*time.Second, 10*time.Millisecond)
	// The cache stays empty.
	assert.Nil(t, cache.Get("c1"))

	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return after cancel")
	}
}

// fakeMQComponent provides an MQ component for the runner test.
type fakeMQComponent struct{ client mq.Client }

func (f *fakeMQComponent) Client() any { return f.client }

func (f *fakeMQComponent) Publish(ctx context.Context, subject string, body []byte, headers map[string]string) error {
	return f.client.Publish(ctx, subject, body, headers)
}

// TestNewSnapshotConsumerRunner verifies the runner construction from
// server components.
func TestNewSnapshotConsumerRunner(t *testing.T) {
	cache := NewProjectionCache()
	client := mq.NewFake()
	components := &fakeComponents{mq: &fakeMQComponent{client: client}}
	consumer := NewSnapshotConsumerRunner(components, cache)
	require.NotNil(t, consumer)
	assert.Equal(t, client, consumer.client)

	// Nil components -> nil.
	assert.Nil(t, NewSnapshotConsumerRunner(nil, cache))
}