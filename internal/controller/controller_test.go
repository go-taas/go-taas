package controller

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/go-taas/go-taas/pkg/mq"
)

// fakeReconciler records applied messages.
type fakeReconciler struct {
	inferChanges []mq.Message
	warmups      []mq.Message
}

func (f *fakeReconciler) ApplyInferServiceChange(_ context.Context, msg mq.Message) error {
	f.inferChanges = append(f.inferChanges, msg)
	return nil
}

func (f *fakeReconciler) ApplyImageWarmup(_ context.Context, msg mq.Message) error {
	f.warmups = append(f.warmups, msg)
	return nil
}

func TestControllerDispatchesSubjects(t *testing.T) {
	client := mq.NewFake()
	rec := &fakeReconciler{}
	ctrl := New(client, rec)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- ctrl.Run(ctx) }()

	// Keep publishing until the subscription has registered and the
	// message is actually delivered (the fake drops publishes to
	// subjects without subscribers).
	require.Eventually(t, func() bool {
		_ = client.Publish(ctx, "infer.services.changes", []byte(`{"id":1}`), nil)
		return len(rec.inferChanges) > 0
	}, 2*time.Second, 10*time.Millisecond)

	require.Eventually(t, func() bool {
		_ = client.Publish(ctx, "image.warmups", []byte(`{"image":"x"}`), nil)
		return len(rec.warmups) > 0
	}, 2*time.Second, 10*time.Millisecond)

	cancel()
	select {
	case err := <-done:
		assert.NoError(t, err)
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return after cancel")
	}

	require.GreaterOrEqual(t, len(rec.inferChanges), 1)
	require.GreaterOrEqual(t, len(rec.warmups), 1)
	assert.Equal(t, "infer.services.changes", rec.inferChanges[0].Subject)
	assert.Equal(t, "image.warmups", rec.warmups[0].Subject)
}

func TestControllerReturnsSubscriptionError(t *testing.T) {
	client := mq.NewFake()
	ctrl := New(client, &fakeReconciler{})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- ctrl.Run(ctx) }()

	// Run keeps consuming until the context is cancelled.
	select {
	case err := <-done:
		t.Fatalf("Run returned early: %v", err)
	case <-time.After(50 * time.Millisecond):
	}

	cancel()
	select {
	case err := <-done:
		assert.NoError(t, err)
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return after cancel")
	}
}
