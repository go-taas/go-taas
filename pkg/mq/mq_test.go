package mq

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/go-taas/go-taas/pkg/config"
)

func TestFakeClientPubSub(t *testing.T) {
	client := NewFake()
	defer func() { require.NoError(t, client.Close()) }()

	var got []Message
	require.NoError(t, client.Subscribe(context.Background(), "a.b", func(msg Message) error {
		got = append(got, msg)
		return nil
	}))

	require.NoError(t, client.Publish(context.Background(), "a.b", []byte("hello"), map[string]string{"k": "v"}))
	require.Len(t, got, 1)
	assert.Equal(t, "a.b", got[0].Subject)
	assert.Equal(t, "hello", string(got[0].Body))
	assert.Equal(t, "v", got[0].Headers["k"])
	assert.False(t, got[0].Timestamp.IsZero())

	// No subscriber on other subjects.
	require.NoError(t, client.Publish(context.Background(), "x.y", []byte("nobody"), nil))
	require.Len(t, got, 1)
}

func TestFakeClientClosed(t *testing.T) {
	client := NewFake()
	require.NoError(t, client.Close())
	assert.Error(t, client.Publish(context.Background(), "a", nil, nil))
	assert.Error(t, client.Subscribe(context.Background(), "a", func(Message) error { return nil }))
}

func TestPermanent(t *testing.T) {
	cause := errors.New("bad payload")
	wrapped := Permanent(cause)
	assert.True(t, IsPermanent(wrapped))
	assert.True(t, errors.Is(wrapped, cause))
	assert.False(t, IsPermanent(cause))
	assert.Nil(t, Permanent(nil))
}

func TestNewClientDriverSelection(t *testing.T) {
	// Empty driver -> fake.
	c, err := NewClient(&config.MQConfig{})
	require.NoError(t, err)
	require.NoError(t, c.Close())

	// Explicit fake.
	c, err = NewClient(&config.MQConfig{Driver: "fake"})
	require.NoError(t, err)
	require.NoError(t, c.Close())

	// Unknown driver -> error.
	_, err = NewClient(&config.MQConfig{Driver: "pigeon"})
	assert.Error(t, err)

	// NATS with empty URL -> error (no network attempt).
	_, err = NewClient(&config.MQConfig{Driver: "nats", URL: ""})
	assert.Error(t, err)
}

func TestDefaultSubjects(t *testing.T) {
	s := DefaultSubjects()
	assert.NotEmpty(t, s.InferServiceChanges)
	assert.NotEmpty(t, s.ImageWarmups)
	assert.NotEmpty(t, s.MeteringEvents)
	assert.NotEmpty(t, s.Settlements)
}

func TestHandlerErrorPropagatesOnFake(t *testing.T) {
	client := NewFake()
	defer func() { _ = client.Close() }()

	boom := errors.New("boom")
	require.NoError(t, client.Subscribe(context.Background(), "err.topic", func(Message) error {
		return boom
	}))
	err := client.Publish(context.Background(), "err.topic", []byte("x"), nil)
	assert.ErrorIs(t, err, boom)
}

func TestMessageTimestampSet(t *testing.T) {
	client := NewFake()
	defer func() { _ = client.Close() }()

	before := time.Now()
	var ts time.Time
	require.NoError(t, client.Subscribe(context.Background(), "ts", func(m Message) error {
		ts = m.Timestamp
		return nil
	}))
	require.NoError(t, client.Publish(context.Background(), "ts", nil, nil))
	assert.False(t, ts.Before(before.Add(-time.Second)))
}
